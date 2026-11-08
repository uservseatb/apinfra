package deployment

import (
	"context"

	"github.com/uservseatb/apinfra/internal/domain/docker"
	"github.com/uservseatb/apinfra/internal/domain/nginx"
)

// ProjectSpec is one enabled settings.yml project plus in-memory compose/env content.
type ProjectSpec struct {
	Name              string
	Namespace         string
	NamespacePrefix   string
	DomainName        string
	InboundPort       string
	EntryPoint        string
	EntryPointPort    string
	CertIssueType     string
	CertUpdateSec     int64
	CertAcmeEmail     string
	AllowPortsMapping bool
	ComposeYAML       string
	SidecarEnvRaw     string
}

// Workspace is the loaded GitOps configuration for one update cycle.
type Workspace struct {
	Projects     []ProjectSpec
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
