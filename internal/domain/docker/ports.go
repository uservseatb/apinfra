package docker

import (
	"context"
	"time"
)

// StackFileSystem persists proxy and per-app compose stacks on the host infra tree.
type StackFileSystem interface {
	WriteProxyCompose(content string) (changed bool, err error)
	WriteAppStack(projectName, composeContent, envContent string) (changed bool, err error)
	ListAppDirs() ([]string, error)
	AppComposeExists(projectName string) (bool, error)
	RemoveAppDir(projectName string) error
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
	Write(kind string, fields map[string]string) error
}
