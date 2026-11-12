package deployment

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/uservseatb/apinfra/internal/domain/docker"
	"github.com/uservseatb/apinfra/internal/domain/nginx"
)

// GitOpsDeploymentService orchestrates one GitOps update cycle.
type GitOpsDeploymentService struct {
	namespace string
	publicIP  string
	layout    LayoutInitializer
	source    ConfigSource
	docker    DockerOrchestrator
	ingress   IngressOrchestrator
}

// NewGitOpsDeploymentService wires the update-cycle dependencies.
func NewGitOpsDeploymentService(
	namespace, publicIP string,
	layout LayoutInitializer,
	source ConfigSource,
	dockerSvc DockerOrchestrator,
	ingress IngressOrchestrator,
) *GitOpsDeploymentService {
	return &GitOpsDeploymentService{
		namespace: namespace,
		publicIP:  publicIP,
		layout:    layout,
		source:    source,
		docker:    dockerSvc,
		ingress:   ingress,
	}
}

// ExecuteUpdateCycle runs prepare → docker → nginx → prune.
// Failures are returned (joined when several steps fail); the caller decides whether
// they are fatal. A failing app stack does not block nginx while the proxy is running,
// except docker login failures — pull/up never ran, so nginx is skipped.
func (s *GitOpsDeploymentService) ExecuteUpdateCycle(ctx context.Context) error {
	err := s.layout.EnsureLayout()
	if err != nil {
		return fmt.Errorf("prepare infra: %w", err)
	}

	workspace, err := s.source.LoadWorkspace(ctx, s.namespace)
	if err != nil {
		return fmt.Errorf("load workspace: %w", err)
	}

	dockerProjects := toDockerProjects(workspace.Projects)

	err = s.docker.ValidateConfigDirEnv(dockerProjects, workspace.HostEnv)
	if err != nil {
		return fmt.Errorf("config dir validation: %w", err)
	}

	log.Printf("[update] validating docker-compose.yml files")

	err = s.docker.ValidateProjects(ctx, dockerProjects, workspace.HostEnv)
	if err != nil {
		return fmt.Errorf("compose validation (no changes applied): %w", err)
	}

	log.Printf("[update] compose validation ok")

	log.Printf("[update] ensuring nginx host files")

	var errs []error

	nginxReady := true

	err = s.ingress.EnsureHostFiles()
	if err != nil {
		nginxReady = false

		errs = append(errs, fmt.Errorf("nginx host files not ready (compose written, pull/up skipped): %w", err))
	}

	log.Printf("[update] docker processor: start apply=%v", nginxReady)

	applyResult, dockerErr := s.docker.Apply(
		ctx, dockerProjects, workspace.HostEnv, workspace.HostSettings, nginxReady,
	)
	if dockerErr != nil {
		errs = append(errs, fmt.Errorf("docker processor: %w", dockerErr))
	} else {
		log.Printf("[update] docker processor: done restarted=%v", applyResult.Restarted)
	}

	errs = append(errs, s.applyNginxAndPrune(ctx, workspace.Projects, nginxReady, applyResult, dockerErr))

	log.Printf("[update] completed namespace=%s", s.namespace)

	return errors.Join(errs...)
}

func (s *GitOpsDeploymentService) applyNginxAndPrune(
	ctx context.Context,
	specs []ProjectSpec,
	nginxReady bool,
	applyResult docker.ApplyResult,
	dockerErr error,
) error {
	if !nginxReady {
		log.Printf("[update] nginx processor: skipped (host files not ready)")
		log.Printf("[update] pruning skipped (nginx host files not ready)")

		return nil
	}

	switch {
	case errors.Is(dockerErr, docker.ErrLoginFailed):
		log.Printf("[update] nginx processor: skipped (docker login failed)")
		log.Printf("[update] pruning skipped (docker login failed)")

		return nil
	case dockerErr != nil && !applyResult.ProxyRunning:
		log.Printf("[update] nginx processor: skipped (docker processor failed and proxy is not running)")
		log.Printf("[update] pruning skipped (docker processor failed)")

		return nil
	}

	var errs []error

	if dockerErr != nil {
		log.Printf("[update] nginx processor: running despite docker errors (proxy is running)")
	}

	log.Printf(
		"[update] nginx processor: start forceReload=%v proxyRunning=%v proxyReady=%v",
		applyResult.Restarted, applyResult.ProxyRunning, applyResult.ProxyReady,
	)

	err := s.ingress.ProcessRouting(ctx, nginx.RoutingParams{
		Projects:     toNginxProjects(specs, applyResult.ProxyHosts),
		ProxyRunning: applyResult.ProxyRunning,
		ProxyReady:   applyResult.ProxyReady,
		ForceReload:  applyResult.Restarted,
		PublicIP:     s.publicIP,
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("nginx processor: %w", err))
	} else {
		log.Printf("[update] nginx processor: done")
	}

	if dockerErr != nil {
		log.Printf("[update] pruning skipped (docker processor failed)")

		return errors.Join(errs...)
	}

	log.Printf("[update] pruning unused docker images")

	err = s.docker.PruneImages(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("docker image prune: %w", err))
	} else {
		log.Printf("[update] docker image prune ok")
	}

	return errors.Join(errs...)
}

func toDockerProjects(specs []ProjectSpec) []docker.Project {
	out := make([]docker.Project, 0, len(specs))
	for _, spec := range specs {
		out = append(out, docker.Project{
			Name:              spec.Name,
			NamespacePrefix:   spec.NamespacePrefix,
			ComposeYAML:       spec.ComposeYAML,
			SidecarEnvRaw:     spec.SidecarEnvRaw,
			InboundPort:       spec.InboundPort,
			EntryPoint:        spec.EntryPoint,
			ProxyHost:         "",
			AllowPortsMapping: spec.AllowPortsMapping,
		})
	}

	return out
}

func toNginxProjects(specs []ProjectSpec, proxyHosts map[string]string) []nginx.Project {
	out := make([]nginx.Project, 0, len(specs))
	for _, spec := range specs {
		proxyHost := ""
		if proxyHosts != nil {
			proxyHost = proxyHosts[spec.Name]
		}

		out = append(out, nginx.Project{
			Name:           spec.Name,
			Namespace:      spec.Namespace,
			DomainName:     spec.DomainName,
			InboundPort:    spec.InboundPort,
			EntryPointPort: spec.EntryPointPort,
			ProxyHost:      proxyHost,
			CertIssueType:  spec.CertIssueType,
			CertUpdateSec:  spec.CertUpdateSec,
			CertAcmeEmail:  spec.CertAcmeEmail,
		})
	}

	return out
}
