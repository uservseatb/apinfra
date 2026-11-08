package docker

import (
	"errors"
	"fmt"
	"log"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

const (
	defaultLoopbackBind = "127.0.0.1"

	// Disallowed compose host IPs: publish on all interfaces.
	disallowedHostIPv4All = "0.0.0.0"
	disallowedHostIPv6All = "::"

	// proxyContainerName is the nginx reverse-proxy container from the base template.
	proxyContainerName = "apinfra-proxy"

	// proxyNetworkName is the bridge shared by apinfra-proxy and proxy.target services only.
	proxyNetworkName = "apinfra_proxy_net"

	// composeProjectPrefix is the docker compose -p prefix for app stacks.
	composeProjectPrefix = "apinfra_"

	noNewPrivilegesOpt = "no-new-privileges:true"

	proxyHTTPPort  = 80
	proxyHTTPSPort = 443
)

var (
	errDisallowedPublishedHostIP = errors.New("disallowed published host IP")
	errInvalidPublishedHostIP    = errors.New("invalid published host IP")
)

// applyPublishedPortsPolicy strips host ports unless allow-ports-mapping is true.
// When allowed: missing ports stay missing; mappings without a host IP get
// 127.0.0.1; mappings that already name a host IP are left unchanged unless the
// IP is unspecified (0.0.0.0 / :: and aliases) or invalid — those reject the cycle.
// Bare container ports (no published) also get published=target so bind matches
// the historical short-form 127.0.0.1:<port>:<port>.
func applyPublishedPortsPolicy(project *types.Project, projectName string, allow bool) error {
	if !allow {
		stripPublishedPorts(project, projectName)

		return nil
	}

	err := rejectDisallowedPublishedHostIPs(project, projectName)
	if err != nil {
		return err
	}

	ensureLoopbackWhenUnbound(project)

	return nil
}

// stripPublishedPorts clears ports on every service so nothing is published on
// the host except what configureProxy sets on apinfra-proxy. Logs a WARN for
// each service that had published ports.
func stripPublishedPorts(project *types.Project, projectName string) {
	forEachService(project, func(name string, svc *types.ServiceConfig) {
		if len(svc.Ports) == 0 {
			return
		}

		log.Printf(
			"[docker] WARN project=%s: stripping published ports service=%s ports=%s (allow-ports-mapping=false)",
			projectName,
			name,
			formatPublishedPorts(svc.Ports),
		)
		svc.Ports = nil
	})
}

func rejectDisallowedPublishedHostIPs(project *types.Project, projectName string) error {
	if project == nil {
		return nil
	}

	for name, svc := range project.Services {
		for _, port := range svc.Ports {
			hostIP := strings.TrimSpace(port.HostIP)
			if hostIP == "" {
				continue
			}

			err := validatePublishedHostIP(hostIP)
			if err == nil {
				continue
			}

			log.Printf(
				"[docker] WARN project=%s: rejected host IP %q on service=%s port=%s: %v",
				projectName,
				hostIP,
				name,
				formatPublishedPort(port),
				err,
			)

			return fmt.Errorf(
				"project %s: service %s: host IP %q: %w (not allowed: %s, %s)",
				projectName,
				name,
				hostIP,
				err,
				disallowedHostIPv4All,
				disallowedHostIPv6All,
			)
		}
	}

	return nil
}

// validatePublishedHostIP accepts a concrete interface address and rejects
// unspecified (0.0.0.0 / :: and aliases) or unparseable values.
func validatePublishedHostIP(hostIP string) error {
	ip := net.ParseIP(normalizePublishedHostIP(hostIP))
	if ip == nil {
		return errInvalidPublishedHostIP
	}

	if ip.IsUnspecified() {
		return errDisallowedPublishedHostIP
	}

	return nil
}

func normalizePublishedHostIP(hostIP string) string {
	hostIP = strings.TrimSpace(hostIP)
	hostIP = strings.TrimPrefix(hostIP, "[")
	hostIP = strings.TrimSuffix(hostIP, "]")

	return hostIP
}

func formatPublishedPorts(ports []types.ServicePortConfig) string {
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, formatPublishedPort(port))
	}

	return strings.Join(parts, ",")
}

func formatPublishedPort(port types.ServicePortConfig) string {
	hostIP := strings.TrimSpace(port.HostIP)
	published := strings.TrimSpace(port.Published)
	proto := strings.TrimSpace(port.Protocol)

	if proto == "" {
		proto = "tcp"
	}

	target := strconv.FormatUint(uint64(port.Target), 10)

	switch {
	case hostIP != "" && published != "":
		return hostIP + ":" + published + ":" + target + "/" + proto
	case published != "":
		return published + ":" + target + "/" + proto
	default:
		return target + "/" + proto
	}
}

func ensureLoopbackWhenUnbound(project *types.Project) {
	forEachService(project, func(_ string, svc *types.ServiceConfig) {
		if len(svc.Ports) == 0 {
			return
		}

		ports := slices.Clone(svc.Ports)
		for i := range ports {
			if strings.TrimSpace(ports[i].HostIP) != "" {
				continue
			}

			ports[i].HostIP = defaultLoopbackBind

			if strings.TrimSpace(ports[i].Published) == "" && ports[i].Target != 0 {
				ports[i].Published = strconv.FormatUint(uint64(ports[i].Target), 10)
			}
		}

		svc.Ports = ports
	})
}

// stripContainerNames clears container_name on every service. Host uniqueness
// comes from compose project (-p apinfra_<name>); fixed names collide across
// projects and break per-stack isolation.
func stripContainerNames(project *types.Project) {
	forEachService(project, func(_ string, svc *types.ServiceConfig) {
		svc.ContainerName = ""
	})
}

// stripNetworkMode clears any network_mode on every service so stacks stay on
// managed bridge networks (project_net / proxy net). Values like host, bridge,
// service:*, and container:* would otherwise skip networks: attachment.
func stripNetworkMode(project *types.Project, projectName string) {
	forEachService(project, func(name string, svc *types.ServiceConfig) {
		mode := strings.TrimSpace(svc.NetworkMode)
		if mode == "" {
			return
		}

		log.Printf(
			"[docker] WARN project=%s: stripping network_mode=%s service=%s",
			projectName,
			mode,
			name,
		)

		svc.NetworkMode = ""
	})
}

// ensureContainerHardening forces privileged=false and security_opt
// no-new-privileges:true on every service. Other security_opt entries are kept;
// any no-new-privileges* variant (including :false) is replaced. Idempotent.
func ensureContainerHardening(project *types.Project) {
	forEachService(project, func(_ string, svc *types.ServiceConfig) {
		svc.Privileged = false
		svc.SecurityOpt = forceNoNewPrivileges(svc.SecurityOpt)
	})
}

// stripCompositionHooks clears Extends so SkipExtends leftovers are not written
// back into host compose YAML for docker compose up to resolve.
func stripCompositionHooks(project *types.Project) {
	forEachService(project, func(_ string, svc *types.ServiceConfig) {
		svc.Extends = nil
	})
}

func forceNoNewPrivileges(opts []string) []string {
	out := make([]string, 0, len(opts)+1)

	for _, opt := range opts {
		trimmed := strings.TrimSpace(opt)
		if strings.HasPrefix(strings.ToLower(trimmed), "no-new-privileges") {
			continue
		}

		out = append(out, opt)
	}

	return append(out, noNewPrivilegesOpt)
}

// resolveServiceKey returns the YAML services map key for serviceName
// (matched by container_name or YAML key).
func resolveServiceKey(project *types.Project, projectName, serviceName string) (string, bool) {
	if project == nil {
		return "", false
	}

	for _, candidate := range serviceNameCandidates(serviceName, projectName) {
		if key, ok := findServiceExact(project, candidate); ok {
			return key, true
		}
	}

	return "", false
}

// configureProxy sets bridge networking on apinfra-proxy:
// publishes 80/443 plus any inboundPorts, and attaches only proxyNetworkName
// (project networks stay off the proxy). The proxy network is created with a
// fixed Docker name (not project-prefixed) so app stacks can join it as external.
func configureProxy(project *types.Project, inboundPorts []string) {
	key, svc, ok := findProxyService(project)
	if !ok {
		return
	}

	svc.NetworkMode = ""
	svc.Ports = buildProxyPorts(inboundPorts)

	ensureProxyNetworkDeclared(project)

	svc.Networks = map[string]*types.ServiceNetworkConfig{
		proxyNetworkName: nil,
	}
	project.Services[key] = svc
}

// attachProxyNetwork adds proxyNetworkName to the proxy.target service
// (matched by container_name or YAML key). Declares the network as external:true
// so the app stack joins the proxy-created bridge, and sets a DNS alias
// service.apinfra_project so nginx (another compose project) can resolve it.
// Other project services and custom networks are unchanged. Returns false if the
// service is missing or has network_mode set.
func attachProxyNetwork(project *types.Project, projectName, serviceName string) bool {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" || project == nil {
		return false
	}

	key, ok := resolveServiceKey(project, projectName, serviceName)
	if !ok {
		return false
	}

	svc := project.Services[key]
	if strings.TrimSpace(svc.NetworkMode) != "" {
		return false
	}

	ensureExternalNetwork(project, proxyNetworkName)
	attachServiceToNetworkWithAlias(&svc, proxyNetworkName, proxyDNSHost(key, projectName))
	project.Services[key] = svc

	return true
}

// proxyDNSHost is how nginx reaches a service on apinfra_proxy_net:
// <service_key>.apinfra_<project>.
func proxyDNSHost(serviceKey, projectName string) string {
	return strings.TrimSpace(serviceKey) + "." + composeProjectName(projectName)
}

// composeProjectName returns the docker compose project name (-p) for an app.
func composeProjectName(projectName string) string {
	return composeProjectPrefix + strings.TrimSpace(projectName)
}

func findProxyService(project *types.Project) (string, types.ServiceConfig, bool) {
	if project == nil {
		return "", types.ServiceConfig{}, false //nolint:exhaustruct_v5 // zero value is the not-found sentinel
	}

	for key, svc := range project.Services {
		if strings.TrimSpace(svc.ContainerName) == proxyContainerName {
			return key, svc, true
		}
	}

	return "", types.ServiceConfig{}, false //nolint:exhaustruct_v5 // zero value is the not-found sentinel
}

func buildProxyPorts(inboundPorts []string) []types.ServicePortConfig {
	ports := []types.ServicePortConfig{
		newPublishedPort(proxyHTTPPort, "80"),
		newPublishedPort(proxyHTTPSPort, "443"),
	}
	seenPort := map[string]struct{}{"80": {}, "443": {}}

	for _, port := range inboundPorts {
		port = strings.TrimSpace(port)
		if port == "" {
			continue
		}

		if _, dup := seenPort[port]; dup {
			continue
		}

		seenPort[port] = struct{}{}

		target, err := strconv.ParseUint(port, 10, 32)
		if err != nil {
			// Non-numeric inbound ports are skipped; settings validation owns the schema.
			continue
		}

		ports = append(ports, newPublishedPort(uint32(target), port))
	}

	return ports
}

func newPublishedPort(target uint32, published string) types.ServicePortConfig {
	return types.ServicePortConfig{ //nolint:exhaustruct_v5 // host_ip/name/app_protocol unused for proxy defaults
		Target:    target,
		Published: published,
		Protocol:  "tcp",
		Mode:      "ingress",
	}
}

// ensureProxyNetworkDeclared creates proxyNetworkName with an explicit Docker
// name so Compose does not prefix it with the project directory (e.g. infra_).
// App stacks reference the same name via external: true.
func ensureProxyNetworkDeclared(project *types.Project) {
	ensureNetworksMap(project)
	//nolint:exhaustruct_v5 // only fixed Docker name is required
	project.Networks[proxyNetworkName] = types.NetworkConfig{Name: proxyNetworkName}
}

// ensureExternalNetwork declares netName at the top level with external: true.
// Idempotent: overwrites a prior non-external declaration of the same name.
func ensureExternalNetwork(project *types.Project, netName string) {
	netName = strings.TrimSpace(netName)
	if netName == "" {
		return
	}

	ensureNetworksMap(project)
	project.Networks[netName] = types.NetworkConfig{External: true} //nolint:exhaustruct_v5 // external-only declaration
}

// ensureProjectNetwork attaches every service to "<project>_net".
// Declares the network at the top level and adds it to each service's networks
// list/map without removing any custom networks already present.
// Services with network_mode set are skipped (mutually exclusive with networks).
// Idempotent.
func ensureProjectNetwork(project *types.Project, projectName string) {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" || project == nil {
		return
	}

	netName := projectName + "_net"
	ensureNetworkDeclared(project, netName)

	forEachService(project, func(_ string, svc *types.ServiceConfig) {
		if strings.TrimSpace(svc.NetworkMode) != "" {
			return
		}

		attachServiceToNetwork(svc, netName)
	})
}

func ensureNetworkDeclared(project *types.Project, netName string) {
	ensureNetworksMap(project)

	if _, exists := project.Networks[netName]; !exists {
		project.Networks[netName] = types.NetworkConfig{} //nolint:exhaustruct_v5 // empty declaration; compose defaults apply
	}
}

func ensureNetworksMap(project *types.Project) {
	if project.Networks == nil {
		project.Networks = types.Networks{}
	}
}

func attachServiceToNetwork(svc *types.ServiceConfig, netName string) {
	if svc.Networks == nil {
		svc.Networks = map[string]*types.ServiceNetworkConfig{}
	}

	if _, exists := svc.Networks[netName]; !exists {
		svc.Networks[netName] = nil
	}
}

// attachServiceToNetworkWithAlias joins netName and ensures alias is listed under
// networks.<netName>.aliases.
func attachServiceToNetworkWithAlias(svc *types.ServiceConfig, netName, alias string) {
	alias = strings.TrimSpace(alias)

	if svc.Networks == nil {
		svc.Networks = map[string]*types.ServiceNetworkConfig{}
	}

	entry := svc.Networks[netName]
	if entry == nil {
		entry = &types.ServiceNetworkConfig{} //nolint:exhaustruct_v5 // aliases filled below
	}

	if alias != "" && !slices.Contains(entry.Aliases, alias) {
		entry.Aliases = append(slices.Clone(entry.Aliases), alias)
	}

	svc.Networks[netName] = entry
}

func findServiceExact(project *types.Project, name string) (string, bool) {
	for key, svc := range project.Services {
		if strings.TrimSpace(svc.ContainerName) == name {
			return key, true
		}
	}

	if _, exists := project.Services[name]; exists {
		return name, true
	}

	return "", false
}

func serviceNameCandidates(name, projectName string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}

	out := []string{name}
	seen := map[string]struct{}{name: {}}
	add := func(str string) {
		str = strings.TrimSpace(str)
		if str == "" {
			return
		}

		if _, exists := seen[str]; exists {
			return
		}

		seen[str] = struct{}{}
		out = append(out, str)
	}

	projectName = strings.TrimSpace(projectName)
	if projectName != "" {
		prefix := projectName + "_"
		if after, found := strings.CutPrefix(name, prefix); found {
			add(after)
		} else {
			add(prefix + name)
		}
	}

	return out
}

func forEachService(project *types.Project, mutate func(name string, svc *types.ServiceConfig)) {
	if project == nil {
		return
	}

	for name, svc := range project.Services {
		mutate(name, &svc)
		project.Services[name] = svc
	}
}
