package deployment

import (
	"context"

	"github.com/uservseatb/apinfra/internal/domain/docker"
	"github.com/uservseatb/apinfra/internal/domain/nginx"
)

// Route is one Nginx ingress for a project (from proxies[] or singular proxy).
type Route struct {
	DomainName     string
	InboundPort    string
	EntryPoint     string
	EntryPointPort string
	TargetURL      string
	CertIssueType  string
	CertUpdateSec  int64
	CertAcmeEmail  string
}

// ProjectSpec is one enabled settings.yml project plus in-memory compose/env content.
type ProjectSpec struct {
	Name              string
	Namespace         string
	NamespacePrefix   string
	Routes            []Route
	AllowPortsMapping bool
	ComposeYAML       string
	SidecarEnvRaw     string
	BindAssets        docker.BindLoad
}

// StreamTarget is a Compose service referenced by stream.sni.default or routes[].
type StreamTarget struct {
	Project string
	Service string
	Port    string
}

// StreamSNIRoute is one exact SNI → Compose passthrough entry.
type StreamSNIRoute struct {
	StreamTarget

	ServerName string
}

// StreamSNI is the optional settings.yml stream.sni block for the update cycle.
type StreamSNI struct {
	Enabled       bool
	RejectDefault bool
	Default       StreamTarget
	Routes        []StreamSNIRoute
}

// Workspace is the loaded GitOps configuration for one update cycle.
type Workspace struct {
	Projects     []ProjectSpec
	Stream       StreamSNI
	HostEnv      map[string]string
	HostSettings map[string]string
}

// ConfigSource loads the config repository and host property maps.
type ConfigSource interface {
	LoadWorkspace(ctx context.Context, namespace string) (*Workspace, error)
}

// LayoutInitializer prepares the host infra directory tree.
type LayoutInitializer interface {
	EnsureLayout() error
}

// DockerOrchestrator is the docker domain surface used by the update cycle.
type DockerOrchestrator interface {
	ValidateConfigDirEnv(projects []docker.Project, hostEnv map[string]string) error
	ValidateProjects(ctx context.Context, projects []docker.Project, hostEnv map[string]string) error
	Apply(
		ctx context.Context,
		projects []docker.Project,
		hostEnv, hostSettings map[string]string,
		applyContainers bool,
	) (docker.ApplyResult, error)
	PruneImages(ctx context.Context) error
}

// IngressOrchestrator is the nginx domain surface used by the update cycle.
type IngressOrchestrator interface {
	EnsureHostFiles() error
	ProcessRouting(ctx context.Context, params nginx.RoutingParams) error
}
