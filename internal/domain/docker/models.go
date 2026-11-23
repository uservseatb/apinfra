package docker

import (
	"strings"
	"time"
)

// Project is the docker-domain view of one enabled settings.yml project.
// ComposeYAML and SidecarEnvRaw are loaded by the caller (config source);
// the domain does not read the config repo.
type Project struct {
	Name              string
	NamespacePrefix   string
	ComposeYAML       string
	SidecarEnvRaw     string
	InboundPorts      []string          // union of route inbound.port values
	EntryPoints       []string          // unique proxy.target.service.name values
	ProxyHosts        map[string]string // service name → Docker DNS host (filled during prepare)
	AllowPortsMapping bool              // settings config.allow-ports-mapping; false → strip host ports
	BindAssets        BindLoad
}

func (p Project) envPrefix() string {
	return strings.ToUpper(p.Name) + "__"
}

// Container is a host container summary from the Docker Engine API.
type Container struct {
	ID             string
	Name           string
	Image          string
	State          string // Docker state: running, restarting, exited, …
	Status         string
	UpTime         time.Duration
	ComposeProject string // com.docker.compose.project label; empty if not compose-managed
	ComposeService string // com.docker.compose.service label; empty if not compose-managed
}

// ApplyTimeouts bounds each docker CLI / API call. Zero fields use package defaults.
type ApplyTimeouts struct {
	Login           time.Duration
	ComposePull     time.Duration
	ComposeUp       time.Duration
	ComposeDown     time.Duration
	Prune           time.Duration
	API             time.Duration
	ProxyReadyAfter time.Duration
}

// Config holds construction-time settings for Service.
type Config struct {
	Timeouts          ApplyTimeouts
	TrustedImagesPath string
	ProxyNetworkMTU   int
	ProjectNetworkMTU int
}

// ApplyResult is the outcome of Apply for the deployment orchestrator / nginx.
// ProxyRunning/ProxyReady come from the same ContainerList used for start planning
// (or from a successful proxy up in this cycle) — no extra Docker API list.
// ProxyHosts maps project name → service name → computed Docker DNS host for nginx proxy_pass.
type ApplyResult struct {
	Restarted    bool
	ProxyRunning bool
	ProxyReady   bool
	ProxyHosts   map[string]map[string]string
}
