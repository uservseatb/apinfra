package docker

import (
	"context"
	"time"
)

// ProxyStackFileSystem persists the shared proxy compose stack under infra/.
type ProxyStackFileSystem interface {
	WriteProxyCompose(content string) (changed bool, err error)
	HasProxyApplyPending() (bool, error)
	ClearProxyApplyPending() error
}

// AppStackFileSystem persists per-project compose stacks under infra/apps/<name>/.
type AppStackFileSystem interface {
	WriteAppStack(projectName, composeContent, envContent string) (changed bool, err error)
	SyncProjectBindAssets(
		projectName string,
		files map[string][]byte,
		dirs map[string]struct{},
	) (appDir string, changed bool, err error)
	HasApplyPending(projectName string) (bool, error)
	ClearApplyPending(projectName string) error
	ListAppDirs() ([]string, error)
	AppComposeExists(projectName string) (bool, error)
	RemoveAppDir(projectName string) error
}

// StackFileSystem is the host infra persistence port used by Service.
// Implemented by infrastructure.StackFS.
type StackFileSystem interface {
	ProxyStackFileSystem
	AppStackFileSystem
}

// CLIExecutor runs docker compose / login / prune operations with bounded timeouts.
type CLIExecutor interface {
	ComposePullProxy(ctx context.Context, timeout time.Duration) error
	ComposeUpProxy(ctx context.Context, timeout time.Duration) error
	ComposePullApp(ctx context.Context, projectName string, timeout time.Duration) error
	ComposeUpApp(ctx context.Context, projectName string, timeout time.Duration) error
	ComposeDownApp(ctx context.Context, projectName string, useComposeFile bool, timeout time.Duration) error
	RegistryLogin(ctx context.Context, host, user, token string, timeout time.Duration) error
	PruneImages(ctx context.Context, timeout time.Duration) error
}

// ContainerLister reports the containers present on the Docker host.
type ContainerLister interface {
	ListContainers(ctx context.Context) ([]Container, error)
}

// EventRecorder records operational events (e.g. registry login failures).
type EventRecorder interface {
	AddEvent(eventType string, fields map[string]string) error
}
