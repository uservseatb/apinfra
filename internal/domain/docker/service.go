package docker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/uservseatb/apinfra/internal/assets"
)

const (
	eventDockerLoginFailed = "docker_login_failed"

	keyRegistryHost  = "containerRegistryHost"
	keyRegistryUser  = "containerRegistryUser"
	keyRegistryToken = "containerRegistryToken"
)

var (
	errMissingComposePartial   = errors.New("missing docker-compose.yml (refusing partial apply)")
	errUnsafeRegistryCred      = errors.New("unsafe registry credential")
	errComposeValidationFailed = errors.New("compose validation failed")
	errEmptyComposeFile        = errors.New("docker-compose.yml is empty")
	errComposeConfigFailed     = errors.New("compose config invalid")
	errProxyTargetMissing      = errors.New("proxy.target.service-name not found in compose")
	errNilContext              = errors.New("nil context")

	// appNameFromLabelRE matches settings-style project names derived from compose -p labels.
	appNameFromLabelRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
)

// Service orchestrates proxy and per-project docker compose stacks.
type Service struct {
	fs                StackFileSystem
	cli               CLIExecutor
	lister            ContainerLister
	events            EventRecorder
	timeouts          ApplyTimeouts
	trustedImagesPath string
	trustedSocks      map[string][]trustedSockRef
	trustedOnce       sync.Once
	trustedSet        map[string]struct{}
}

// NewService builds a docker domain Service. events may be nil.
// Empty cfg.TrustedImagesPath defaults to $HOME/.trusted_images.
func NewService(
	stackFS StackFileSystem,
	cli CLIExecutor,
	lister ContainerLister,
	events EventRecorder,
	cfg Config,
) *Service {
	trustedPath := strings.TrimSpace(cfg.TrustedImagesPath)
	if trustedPath == "" {
		home := strings.TrimSpace(os.Getenv("HOME"))
		if home != "" {
			trustedPath = filepath.Join(home, trustedImagesFileName)
		}
	}

	return &Service{ //nolint:exhaustruct_v5 // trustedSocks/trustedSet filled lazily per cycle
		fs:                stackFS,
		cli:               cli,
		lister:            lister,
		events:            events,
		timeouts:          cfg.Timeouts.withDefaults(),
		trustedImagesPath: trustedPath,
	}
}

// Apply writes the proxy stack and per-project stacks via StackFileSystem.
// When applyContainers is false, only writes compose/.env files (no pull/up/down).
// When applyContainers is true, starts stacks whose content changed, that have
// .apply_pending, or that are not fully running (including deleted services),
// sequentially, and downs disabled/removed apps.
func (s *Service) Apply(
	ctx context.Context,
	projects []Project,
	hostEnv, hostSettings map[string]string,
	applyContainers bool,
) (ApplyResult, error) {
	if ctx == nil {
		return ApplyResult{}, errNilContext
	}

	log.Printf(
		"[docker] start services=%d apply=%v features=per-project-stacks",
		len(projects), applyContainers,
	)

	hostEnv = nonemptyStringMap(hostEnv)
	hostSettings = nonemptyStringMap(hostSettings)

	var result ApplyResult

	proxyChanged, err := s.writeProxyStack(ctx, projects)
	if err != nil {
		return result, err
	}

	enabled, stackChanged, applyPending, desiredServices, err := s.writeAllAppStacks(ctx, projects, hostEnv)
	if err != nil {
		return result, err
	}

	result.ProxyHosts = collectProxyHosts(projects)

	if !applyContainers {
		log.Printf("[docker] compose files written; skip pull/up (applyContainers=false)")
		log.Printf("[docker] done")

		return result, nil
	}

	result, err = s.applyRunningStacks(
		ctx, hostSettings, proxyChanged, enabled, stackChanged, applyPending, desiredServices, result.ProxyHosts,
	)
	if err != nil {
		return result, err
	}

	log.Printf(
		"[docker] done restarted=%v proxyRunning=%v proxyReady=%v",
		result.Restarted, result.ProxyRunning, result.ProxyReady,
	)

	return result, nil
}

// ValidateProjects validates each project's compose file with compose-go
// (schema + consistency + env interpolation from sidecar/host).
func (s *Service) ValidateProjects(ctx context.Context, projects []Project, hostEnv map[string]string) error {
	if hostEnv == nil {
		hostEnv = map[string]string{}
	}

	var errs []string

	for _, project := range projects {
		err := s.validateOneProjectCompose(ctx, project, hostEnv)
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) == 0 {
		return nil
	}

	return fmt.Errorf("%w:\n  %s", errComposeValidationFailed, strings.Join(errs, "\n  "))
}

// PruneImages removes unused docker images after a successful deploy cycle.
func (s *Service) PruneImages(ctx context.Context) error {
	timeout := s.timeouts.Prune
	if timeout <= 0 {
		timeout = defaultPruneTimeout
	}

	err := s.cli.PruneImages(ctx, timeout)
	if err != nil {
		return fmt.Errorf("docker image prune: %w", err)
	}

	return nil
}

func nonemptyStringMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}

	return m
}

func collectProxyHosts(projects []Project) map[string]string {
	out := make(map[string]string, len(projects))
	for i := range projects {
		if projects[i].ProxyHost == "" {
			continue
		}

		out[projects[i].Name] = projects[i].ProxyHost
	}

	return out
}

func (s *Service) writeAllAppStacks(
	ctx context.Context,
	projects []Project,
	hostEnv map[string]string,
) ([]string, map[string]struct{}, map[string]struct{}, map[string]map[string]struct{}, error) {
	enabled := make([]string, 0, len(projects))
	stackChanged := map[string]struct{}{}
	applyPending := map[string]struct{}{}
	desiredServices := make(map[string]map[string]struct{}, len(projects))

	for i := range projects {
		project := &projects[i]
		enabled = append(enabled, project.Name)

		changed, services, err := s.writeAppStack(ctx, project, hostEnv)
		if err != nil {
			return nil, nil, nil, nil, err
		}

		desiredServices[composeProjectName(project.Name)] = services

		if changed {
			stackChanged[project.Name] = struct{}{}
		}

		pending, pendingErr := s.fs.HasApplyPending(project.Name)
		if pendingErr != nil {
			return nil, nil, nil, nil, fmt.Errorf("project %s apply pending: %w", project.Name, pendingErr)
		}

		if pending {
			applyPending[project.Name] = struct{}{}
		}
	}

	return enabled, stackChanged, applyPending, desiredServices, nil
}

func (s *Service) applyRunningStacks(
	ctx context.Context,
	hostSettings map[string]string,
	proxyChanged bool,
	enabled []string,
	stackChanged, applyPending map[string]struct{},
	desiredServices map[string]map[string]struct{},
	proxyHosts map[string]string,
) (ApplyResult, error) {
	result := ApplyResult{
		Restarted:    false,
		ProxyRunning: false,
		ProxyReady:   false,
		ProxyHosts:   proxyHosts,
	}

	enabledSet := enabledNameSet(enabled)
	appsToStart, needProxy, proxyRunning, proxyReady, containers := s.planStacksToStart(
		ctx, proxyChanged, enabled, stackChanged, applyPending, desiredServices,
	)
	result.ProxyRunning = proxyRunning
	result.ProxyReady = proxyReady

	appsToDown, err := s.unusedAppNames(enabledSet, containers)
	if err != nil {
		return result, err
	}

	logComposePlan(needProxy, proxyChanged, appsToStart, stackChanged, applyPending, appsToDown)

	if needProxy || len(appsToStart) > 0 {
		err = s.dockerLogin(ctx, hostSettings)
		if err != nil {
			return result, err
		}
	}

	downed := s.downAppNames(ctx, appsToDown)

	proxyStarted, appsRestarted, err := s.startProxyAndApps(ctx, proxyChanged, needProxy, appsToStart)
	result.Restarted = proxyStarted || appsRestarted || downed

	if proxyStarted {
		// Proxy was just (re)started this cycle — running, but not yet ready for ACME.
		result.ProxyRunning = true
		result.ProxyReady = false
	}

	return result, err
}

func logComposePlan(
	needProxy, proxyChanged bool,
	appsToStart []string,
	stackChanged, applyPending map[string]struct{},
	appsToDown []string,
) {
	var proxyReason string

	switch {
	case needProxy && proxyChanged:
		proxyReason = "compose_changed"
	case needProxy:
		proxyReason = "not_running"
	default:
		proxyReason = "skip"
	}

	log.Printf(
		"[docker] plan proxy=%s start=%s down=%s",
		proxyReason,
		formatNameList(appsToStart),
		formatNameList(appsToDown),
	)

	for _, name := range appsToStart {
		reason := startReason(name, stackChanged, applyPending)
		log.Printf("[docker] plan start project=%s reason=%s", name, reason)
	}

	for _, name := range appsToDown {
		log.Printf("[docker] plan down project=%s", name)
	}
}

func startReason(name string, stackChanged, applyPending map[string]struct{}) string {
	switch {
	case hasKey(stackChanged, name):
		return "stack_changed"
	case hasKey(applyPending, name):
		return "apply_pending"
	default:
		return "not_running"
	}
}

func hasKey(set map[string]struct{}, key string) bool {
	_, ok := set[key]

	return ok
}

func formatNameList(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}

	return strings.Join(names, ",")
}

func (s *Service) unusedAppNames(
	enabled map[string]struct{},
	containers []Container,
) ([]string, error) {
	fromDirs, err := s.unusedAppNamesFromDirs(enabled)
	if err != nil {
		return nil, err
	}

	return mergeUnusedAppNames(fromDirs, unusedAppNamesFromDocker(containers, enabled)), nil
}

func (s *Service) unusedAppNamesFromDirs(enabled map[string]struct{}) ([]string, error) {
	names, err := s.fs.ListAppDirs()
	if err != nil {
		return nil, fmt.Errorf("list apps dir: %w", err)
	}

	var out []string

	for _, name := range names {
		if _, ok := enabled[name]; ok {
			continue
		}

		out = append(out, name)
	}

	return out, nil
}

func unusedAppNamesFromDocker(containers []Container, enabled map[string]struct{}) []string {
	if len(containers) == 0 {
		return nil
	}

	seen := make(map[string]struct{})

	for _, c := range containers {
		name, ok := appNameFromComposeProject(c.ComposeProject)
		if !ok {
			continue
		}

		if _, en := enabled[name]; en {
			continue
		}

		seen[name] = struct{}{}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}

	slices.Sort(out)

	return out
}

func mergeUnusedAppNames(a, b []string) []string {
	switch {
	case len(a) == 0:
		return b
	case len(b) == 0:
		return a
	}

	seen := make(map[string]struct{}, len(a)+len(b))
	for _, name := range a {
		seen[name] = struct{}{}
	}

	for _, name := range b {
		seen[name] = struct{}{}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}

	slices.Sort(out)

	return out
}

// appNameFromComposeProject maps compose -p apinfra_<name> back to settings project name.
func appNameFromComposeProject(composeProject string) (string, bool) {
	if !strings.HasPrefix(composeProject, composeProjectPrefix) {
		return "", false
	}

	name := strings.TrimPrefix(composeProject, composeProjectPrefix)
	if name == "" || !appNameFromLabelRE.MatchString(name) {
		return "", false
	}

	return name, true
}

func (s *Service) startProxyAndApps(
	ctx context.Context,
	proxyChanged, needProxy bool,
	appsToStart []string,
) (bool, bool, error) {
	proxyStarted, err := s.startProxyIfNeeded(ctx, proxyChanged, needProxy)
	if err != nil {
		return false, false, err
	}

	appsRestarted, err := s.startApps(ctx, appsToStart)

	return proxyStarted, appsRestarted, err
}

func enabledNameSet(enabled []string) map[string]struct{} {
	out := make(map[string]struct{}, len(enabled))
	for _, name := range enabled {
		out[name] = struct{}{}
	}

	return out
}

func (s *Service) startProxyIfNeeded(ctx context.Context, proxyChanged, needProxy bool) (bool, error) {
	if !needProxy {
		log.Printf("[docker] proxy compose unchanged and running, skipping compose up")

		return false, nil
	}

	reason := "not running"
	if proxyChanged {
		reason = "compose changed"
	}

	log.Printf("[docker] proxy will update (%s)", reason)

	err := s.pullAndUpProxy(ctx)
	if err != nil {
		return false, err
	}

	return true, nil
}

func (s *Service) planStacksToStart(
	ctx context.Context,
	proxyChanged bool,
	enabled []string,
	stackChanged, applyPending map[string]struct{},
	desiredServices map[string]map[string]struct{},
) ([]string, bool, bool, bool, []Container) {
	listCtx, cancel := context.WithTimeout(ctx, s.timeouts.API)
	defer cancel()

	containers, err := s.lister.ListContainers(listCtx)
	if err != nil {
		log.Printf(
			"[docker] WARN container list: %v (state unknown; starting only changed/pending stacks; nginx certs deferred)",
			err,
		)

		return appsWithStackMarkers(enabled, stackChanged, applyPending), proxyChanged, false, false, nil
	}

	runningProjects := runningComposeProjects(containers, desiredServices)
	proxyRunning := proxyRunningAmong(containers)
	proxyReady := proxyReadyAmong(containers)

	return appsNeedingStart(enabled, stackChanged, applyPending, runningProjects),
		proxyChanged || !proxyRunning,
		proxyRunning,
		proxyReady,
		containers
}

func appsWithStackMarkers(enabled []string, stackChanged, applyPending map[string]struct{}) []string {
	out := make([]string, 0, len(enabled))

	for _, name := range enabled {
		if hasKey(stackChanged, name) || hasKey(applyPending, name) {
			out = append(out, name)
		}
	}

	return out
}

func appsNeedingStart(
	enabled []string,
	stackChanged, applyPending map[string]struct{},
	runningProjects map[string]struct{},
) []string {
	out := make([]string, 0, len(enabled))

	for _, name := range enabled {
		if hasKey(stackChanged, name) || hasKey(applyPending, name) {
			out = append(out, name)

			continue
		}

		if _, running := runningProjects[composeProjectName(name)]; running {
			continue
		}

		out = append(out, name)
	}

	return out
}

func (s *Service) startApps(ctx context.Context, appsToStart []string) (bool, error) {
	if len(appsToStart) == 0 {
		log.Printf("[docker] no app stacks to start")

		return false, nil
	}

	var (
		restarted bool
		errs      []error
	)

	for _, name := range appsToStart {
		upAttempted, err := s.pullAndUpApp(ctx, name)
		if upAttempted {
			restarted = true
		}

		if err != nil {
			log.Printf("[docker] WARN start project=%s: %v (continuing with remaining stacks)", name, err)

			errs = append(errs, err)

			continue
		}

		if upAttempted {
			s.emitTrustedImageDeployed(name)
		}
	}

	return restarted, errors.Join(errs...)
}

func (s *Service) writeProxyStack(ctx context.Context, projects []Project) (bool, error) {
	baseTemplate, err := assets.LoadText("templates/docker-compose-app.tpl")
	if err != nil {
		return false, fmt.Errorf("load docker-compose template: %w", err)
	}

	composeProject, err := loadCompose(ctx, baseTemplate, "proxy", nil, loadMutate)
	if err != nil {
		return false, fmt.Errorf("parse docker-compose template: %w", err)
	}

	configureProxy(composeProject, collectInboundPorts(projects))
	ensureContainerHardening(composeProject)
	stripCompositionHooks(composeProject)

	content, err := marshalCompose(composeProject)
	if err != nil {
		return false, fmt.Errorf("marshal proxy compose: %w", err)
	}

	changed, err := s.fs.WriteProxyCompose(content)
	if err != nil {
		return false, fmt.Errorf("write proxy docker-compose.yml: %w", err)
	}

	return changed, nil
}

func (s *Service) writeAppStack(
	ctx context.Context,
	project *Project,
	hostEnv map[string]string,
) (bool, map[string]struct{}, error) {
	composeProject, sidecar, err := s.prepareProjectCompose(ctx, project, hostEnv)
	if err != nil {
		return false, nil, err
	}

	composeContent, err := marshalCompose(composeProject)
	if err != nil {
		return false, nil, fmt.Errorf("project %s marshal compose: %w", project.Name, err)
	}

	envContent := strings.Join(projectEnvFileLines(*project, sidecar, hostEnv), "\n")

	changed, err := s.fs.WriteAppStack(project.Name, composeContent, envContent)
	if err != nil {
		return false, nil, fmt.Errorf("project %s write app stack: %w", project.Name, err)
	}

	return changed, composeServiceNameSet(composeProject), nil
}

func composeServiceNameSet(composeProject *types.Project) map[string]struct{} {
	if composeProject == nil {
		return nil
	}

	out := make(map[string]struct{}, len(composeProject.Services))
	for name := range composeProject.Services {
		out[name] = struct{}{}
	}

	return out
}

func (s *Service) prepareProjectCompose(
	ctx context.Context,
	project *Project,
	hostEnv map[string]string,
) (*types.Project, map[string]string, error) {
	raw := project.ComposeYAML
	if strings.TrimSpace(raw) == "" {
		return nil, nil, fmt.Errorf("project %s: %w", project.Name, errMissingComposePartial)
	}

	sidecar, err := projectSidecarEnv(*project)
	if err != nil {
		return nil, nil, fmt.Errorf("project %s sidecar .env: %w", project.Name, err)
	}

	envMap := map[string]string{}
	maps.Copy(envMap, sidecar)
	maps.Copy(envMap, hostProjectEnv(project.envPrefix(), hostEnv))

	composeProject, err := loadCompose(ctx, raw, project.Name, envMap, loadMutate)
	if err != nil {
		return nil, nil, fmt.Errorf("project %s parse compose: %w", project.Name, err)
	}

	err = applyPublishedPortsPolicy(composeProject, project.Name, project.AllowPortsMapping)
	if err != nil {
		return nil, nil, err
	}

	err = s.validateProjectVolumes(project.Name, composeProject)
	if err != nil {
		return nil, nil, err
	}

	s.rememberTrustedSocks(project.Name, composeProject)

	stripNetworkMode(composeProject, project.Name)
	ensureProjectNetwork(composeProject, project.Name)
	resolveProxyHost(project, composeProject)
	attachProxyTargetNetwork(project, composeProject)
	stripContainerNames(composeProject)
	ensureContainerHardening(composeProject)
	stripCompositionHooks(composeProject)

	return composeProject, sidecar, nil
}

func resolveProxyHost(project *Project, composeProject *types.Project) {
	if project.EntryPoint == "" {
		return
	}

	key, ok := resolveServiceKey(composeProject, project.Name, project.EntryPoint)
	if !ok {
		log.Printf(
			"[docker] WARN project=%s: target %q not found in compose (need container_name or YAML service key)",
			project.Name, project.EntryPoint,
		)

		return
	}

	project.ProxyHost = proxyDNSHost(key, project.Name)
}

func attachProxyTargetNetwork(project *Project, composeProject *types.Project) {
	if project.EntryPoint == "" {
		return
	}

	if attachProxyNetwork(composeProject, project.Name, project.EntryPoint) {
		return
	}

	log.Printf(
		"[docker] WARN project=%s: cannot attach %s to proxy.target %q",
		project.Name, proxyNetworkName, project.EntryPoint,
	)
}

func collectInboundPorts(projects []Project) []string {
	var ports []string

	for i := range projects {
		if projects[i].InboundPort != "" {
			ports = append(ports, projects[i].InboundPort)
		}
	}

	return ports
}

func (s *Service) pullAndUpProxy(ctx context.Context) error {
	log.Printf("[docker] pulling proxy images (timeout=%s)...", s.timeouts.ComposePull)

	err := s.cli.ComposePullProxy(ctx, s.timeouts.ComposePull)
	if err != nil {
		return fmt.Errorf("docker compose pull (proxy): %w", err)
	}

	log.Printf("[docker] starting proxy (compose up -d --remove-orphans; timeout=%s)...", s.timeouts.ComposeUp)

	err = s.cli.ComposeUpProxy(ctx, s.timeouts.ComposeUp)
	if err != nil {
		return fmt.Errorf("docker compose up (proxy): %w", err)
	}

	log.Printf("[docker] proxy up ok")

	return nil
}

// pullAndUpApp reports whether compose up was attempted (containers may have changed even if up failed).
// .apply_pending is cleared only after a successful up.
func (s *Service) pullAndUpApp(ctx context.Context, projectName string) (bool, error) {
	log.Printf("[docker] pulling images project=%s (timeout=%s)...", projectName, s.timeouts.ComposePull)

	err := s.cli.ComposePullApp(ctx, projectName, s.timeouts.ComposePull)
	if err != nil {
		return false, fmt.Errorf("docker compose pull project=%s: %w", projectName, err)
	}

	log.Printf(
		"[docker] starting project=%s (up -d --remove-orphans; timeout=%s)...",
		projectName, s.timeouts.ComposeUp,
	)

	err = s.cli.ComposeUpApp(ctx, projectName, s.timeouts.ComposeUp)
	if err != nil {
		return true, fmt.Errorf("docker compose up project=%s: %w", projectName, err)
	}

	clearErr := s.fs.ClearApplyPending(projectName)
	if clearErr != nil {
		log.Printf("[docker] WARN clear apply pending project=%s: %v", projectName, clearErr)
	}

	log.Printf("[docker] project=%s up ok", projectName)

	return true, nil
}

func (s *Service) downAppNames(ctx context.Context, names []string) bool {
	if len(names) == 0 {
		return false
	}

	downed := false

	for _, name := range names {
		useComposeFile, existsErr := s.fs.AppComposeExists(name)
		if existsErr != nil {
			log.Printf("[docker] WARN compose exists project=%s: %v (down by project name)", name, existsErr)

			useComposeFile = false
		}

		log.Printf(
			"[docker] down unused project=%s (useComposeFile=%v; timeout=%s)",
			name, useComposeFile, s.timeouts.ComposeDown,
		)

		execErr := s.cli.ComposeDownApp(ctx, name, useComposeFile, s.timeouts.ComposeDown)
		if execErr != nil {
			log.Printf("[docker] WARN compose down project=%s: %v (retry next cycle)", name, execErr)

			continue
		}

		removeErr := s.fs.RemoveAppDir(name)
		if removeErr != nil {
			log.Printf("[docker] WARN remove app dir project=%s: %v", name, removeErr)

			continue
		}

		downed = true
	}

	return downed
}

func (s *Service) dockerLogin(ctx context.Context, envs map[string]string) error {
	host := strings.TrimSpace(envs[keyRegistryHost])
	user := strings.TrimSpace(envs[keyRegistryUser])
	token := strings.TrimSpace(envs[keyRegistryToken])

	if host == "" || user == "" || token == "" {
		log.Printf("[docker] skip registry login (%s/%s/%s not fully set)",
			keyRegistryHost, keyRegistryUser, keyRegistryToken)

		return nil
	}

	if strings.ContainsAny(host, " \t\r\n") || strings.ContainsAny(user, " \t\r\n") {
		return fmt.Errorf("%w: registry host/user must not contain whitespace", errUnsafeRegistryCred)
	}

	log.Printf("[docker] logging into registry %s as %s (timeout=%s)...", host, user, s.timeouts.Login)

	err := s.cli.RegistryLogin(ctx, host, user, token, s.timeouts.Login)
	if err != nil {
		if s.events != nil {
			writeErr := s.events.Write(eventDockerLoginFailed, map[string]string{"registry": host})
			if writeErr != nil {
				log.Printf("[docker] WARN write login-failed event: %v", writeErr)
			}
		}

		return fmt.Errorf("docker login %s: %w", host, err)
	}

	log.Printf("[docker] registry login ok")

	return nil
}

func (s *Service) validateOneProjectCompose(ctx context.Context, project Project, host map[string]string) error {
	raw, err := projectComposeRaw(project)
	if err != nil {
		return err
	}

	envMap, err := buildProjectValidateEnv(project, host)
	if err != nil {
		return err
	}

	composeProject, err := loadCompose(ctx, raw, project.Name, envMap, loadValidate)
	if err != nil {
		return fmt.Errorf("project=%s: %w: %w", project.Name, errComposeConfigFailed, err)
	}

	err = validateProxyTargetService(project, composeProject)
	if err != nil {
		return err
	}

	err = s.validateProjectVolumes(project.Name, composeProject)
	if err != nil {
		return err
	}

	s.rememberTrustedSocks(project.Name, composeProject)

	log.Printf("[compose-validate] ok project=%s", project.Name)

	return nil
}

func validateProxyTargetService(project Project, composeProject *types.Project) error {
	if project.EntryPoint == "" {
		return nil
	}

	if _, ok := resolveServiceKey(composeProject, project.Name, project.EntryPoint); ok {
		return nil
	}

	return fmt.Errorf(
		"project=%s: %w %q (need container_name or YAML service key)",
		project.Name, errProxyTargetMissing, project.EntryPoint,
	)
}

func projectComposeRaw(project Project) (string, error) {
	raw := project.ComposeYAML
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("project=%s: %w", project.Name, errEmptyComposeFile)
	}

	return raw, nil
}

func buildProjectValidateEnv(project Project, host map[string]string) (map[string]string, error) {
	sidecar, err := projectSidecarEnv(project)
	if err != nil {
		return nil, fmt.Errorf("project=%s: sidecar .env: %w", project.Name, err)
	}

	envMap := map[string]string{}
	maps.Copy(envMap, sidecar)
	maps.Copy(envMap, hostProjectEnv(project.envPrefix(), host))

	return envMap, nil
}
