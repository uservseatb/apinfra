package deployment_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/uservseatb/apinfra/internal/domain/deployment"
	"github.com/uservseatb/apinfra/internal/domain/docker"
	"github.com/uservseatb/apinfra/internal/domain/nginx"
)

const (
	testNamespace = "dev"
	testPublicIP  = "203.0.113.7"
	projShop      = "shop"
	shopProxyHost = "web.apinfra_shop"
)

var errBoom = errors.New("boom")

// fakeEnv implements all four deployment ports as a single state-based stub:
// it records what the cycle did and returns scripted outcomes.
type fakeEnv struct {
	layoutErr, loadErr, envErr, validateErr, hostFilesErr, applyErr, routingErr, pruneErr error

	apply docker.ApplyResult

	workspace *deployment.Workspace

	applied         bool
	appliedContains bool
	routed          bool
	routing         nginx.RoutingParams
	pruned          bool
	dockerProjects  []docker.Project
}

func (f *fakeEnv) EnsureLayout() error { return f.layoutErr }

func (f *fakeEnv) LoadWorkspace(context.Context, string) (*deployment.Workspace, error) {
	return f.workspace, f.loadErr
}

func (f *fakeEnv) ValidateConfigDirEnv(_ []docker.Project, _ map[string]string) error {
	return f.envErr
}

func (f *fakeEnv) ValidateProjects(context.Context, []docker.Project, map[string]string) error {
	return f.validateErr
}

func (f *fakeEnv) Apply(
	_ context.Context, projects []docker.Project, _, _ map[string]string, applyContainers bool,
) (docker.ApplyResult, error) {
	f.applied = true
	f.appliedContains = applyContainers
	f.dockerProjects = projects

	return f.apply, f.applyErr
}

func (f *fakeEnv) PruneImages(context.Context) error {
	f.pruned = true

	return f.pruneErr
}

func (f *fakeEnv) EnsureHostFiles() error { return f.hostFilesErr }

func (f *fakeEnv) ProcessRouting(_ context.Context, params nginx.RoutingParams) error {
	f.routed = true
	f.routing = params

	return f.routingErr
}

func newFake(mutate func(*fakeEnv)) *fakeEnv {
	env := &fakeEnv{
		workspace: &deployment.Workspace{
			Projects: []deployment.ProjectSpec{{
				Name: projShop, Namespace: testNamespace, NamespacePrefix: testNamespace,
				DomainName: "shop.example.com", EntryPoint: "web", EntryPointPort: "8080",
				CertIssueType: "prod", CertUpdateSec: 60, CertAcmeEmail: "ops@example.com",
				ComposeYAML: "services: {}",
			}},
			HostEnv: map[string]string{"SHOP__X": "1"},
		},
		apply: docker.ApplyResult{
			ProxyRunning: true, ProxyReady: true, Restarted: false,
			ProxyHosts: map[string]string{projShop: shopProxyHost},
		},
	}
	if mutate != nil {
		mutate(env)
	}

	return env
}

func TestExecuteUpdateCycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mut  func(*fakeEnv)

		wantErr         string // substring; "" = nil
		wantApplied     bool
		wantApplyStarts bool // applyContainers flag
		wantRouted      bool
		wantPruned      bool
	}{
		{
			name:            "happy_path_runs_every_stage",
			wantApplied:     true,
			wantApplyStarts: true,
			wantRouted:      true,
			wantPruned:      true,
		},
		{
			name:    "layout_failure_aborts_everything",
			mut:     func(f *fakeEnv) { f.layoutErr = errBoom },
			wantErr: "prepare infra",
		},
		{
			name:    "workspace_load_failure_aborts",
			mut:     func(f *fakeEnv) { f.loadErr = errBoom },
			wantErr: "load workspace",
		},
		{
			name:    "config_dir_env_failure_aborts_before_apply",
			mut:     func(f *fakeEnv) { f.envErr = errBoom },
			wantErr: "config dir validation",
		},
		{
			name:    "compose_validation_failure_aborts_before_apply",
			mut:     func(f *fakeEnv) { f.validateErr = errBoom },
			wantErr: "no changes applied",
		},
		{
			name:            "host_files_failure_writes_stacks_but_skips_nginx_and_prune",
			mut:             func(f *fakeEnv) { f.hostFilesErr = errBoom },
			wantErr:         "nginx host files not ready",
			wantApplied:     true,
			wantApplyStarts: false,
		},
		{
			name: "docker_failure_with_proxy_running_still_routes_but_skips_prune",
			mut: func(f *fakeEnv) {
				f.applyErr = errBoom
			},
			wantErr:         "docker processor",
			wantApplied:     true,
			wantApplyStarts: true,
			wantRouted:      true,
		},
		{
			name: "docker_login_failure_skips_nginx_and_prune_even_if_proxy_running",
			mut: func(f *fakeEnv) {
				f.applyErr = fmt.Errorf("%w: ghcr.io: %w", docker.ErrLoginFailed, errBoom)
			},
			wantErr:         "docker processor",
			wantApplied:     true,
			wantApplyStarts: true,
		},
		{
			name: "docker_failure_with_proxy_down_skips_nginx_and_prune",
			mut: func(f *fakeEnv) {
				f.applyErr = errBoom
				f.apply.ProxyRunning = false
			},
			wantErr:         "docker processor",
			wantApplied:     true,
			wantApplyStarts: true,
		},
		{
			name:            "routing_failure_is_reported_but_prune_still_runs",
			mut:             func(f *fakeEnv) { f.routingErr = errBoom },
			wantErr:         "nginx processor",
			wantApplied:     true,
			wantApplyStarts: true,
			wantRouted:      true,
			wantPruned:      true,
		},
		{
			name:            "prune_failure_is_reported",
			mut:             func(f *fakeEnv) { f.pruneErr = errBoom },
			wantErr:         "docker image prune",
			wantApplied:     true,
			wantApplyStarts: true,
			wantRouted:      true,
			wantPruned:      true,
		},
		{
			name: "multiple_failures_are_joined",
			mut: func(f *fakeEnv) {
				f.hostFilesErr = errBoom
				f.applyErr = errBoom
			},
			wantErr:     "nginx host files not ready",
			wantApplied: true,
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newFake(tcase.mut)
			svc := deployment.NewGitOpsDeploymentService(testNamespace, testPublicIP, env, env, env, env)

			err := svc.ExecuteUpdateCycle(context.Background())

			switch {
			case tcase.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tcase.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tcase.wantErr)):
				t.Fatalf("err = %v, want containing %q", err, tcase.wantErr)
			}

			if env.applied != tcase.wantApplied {
				t.Fatalf("applied = %v, want %v", env.applied, tcase.wantApplied)
			}

			if tcase.wantApplied && env.appliedContains != tcase.wantApplyStarts {
				t.Fatalf("applyContainers = %v, want %v", env.appliedContains, tcase.wantApplyStarts)
			}

			if env.routed != tcase.wantRouted {
				t.Fatalf("routed = %v, want %v", env.routed, tcase.wantRouted)
			}

			if env.pruned != tcase.wantPruned {
				t.Fatalf("pruned = %v, want %v", env.pruned, tcase.wantPruned)
			}
		})
	}
}

func TestExecuteUpdateCycle_PassesDockerResultToRouting(t *testing.T) {
	t.Parallel()

	env := newFake(func(f *fakeEnv) {
		f.apply = docker.ApplyResult{
			Restarted: true, ProxyRunning: true, ProxyReady: false,
			ProxyHosts: map[string]string{projShop: shopProxyHost},
		}
	})
	svc := deployment.NewGitOpsDeploymentService(testNamespace, testPublicIP, env, env, env, env)

	err := svc.ExecuteUpdateCycle(context.Background())
	if err != nil {
		t.Fatalf("ExecuteUpdateCycle: %v", err)
	}

	got := env.routing
	if !got.ForceReload || !got.ProxyRunning || got.ProxyReady || got.PublicIP != testPublicIP {
		t.Fatalf("routing params = %+v", got)
	}

	if len(got.Projects) != 1 {
		t.Fatalf("projects = %+v", got.Projects)
	}

	want := nginx.Project{
		Name: projShop, Namespace: testNamespace, DomainName: "shop.example.com",
		EntryPointPort: "8080", ProxyHost: shopProxyHost, CertIssueType: "prod",
		CertUpdateSec: 60, CertAcmeEmail: "ops@example.com",
	}
	if got.Projects[0] != want {
		t.Fatalf("nginx project = %+v, want %+v", got.Projects[0], want)
	}

	if len(env.dockerProjects) != 1 || env.dockerProjects[0].EntryPoint != "web" || env.dockerProjects[0].ProxyHost != "" {
		t.Fatalf("docker projects = %+v", env.dockerProjects)
	}
}
