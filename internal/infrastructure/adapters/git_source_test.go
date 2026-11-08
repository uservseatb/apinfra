package adapters_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/config"
	"github.com/uservseatb/apinfra/internal/domain/deployment"
	"github.com/uservseatb/apinfra/internal/events"
	"github.com/uservseatb/apinfra/internal/infrastructure/adapters"
)

const (
	settingsDev = `version: "1"
projects:
  - name: shop
    proxy:
      inbound:
        domain-name: shop.example.com
      target:
        service-name: web
        port: 8080
    config:
      cert-issue-type: none
  - name: worker
    proxy:
      inbound:
        domain-name: worker.example.com
      target:
        service-name: app
        port: 9000
    config:
      cert-issue-type: none
`
	shopCompose = "services:\n  web:\n    image: nginx\n"
	shopEnv     = "TOKEN=from-repo\n"
	gitTimeout  = 30 * time.Second
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	full := append([]string{
		"-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false",
	}, args...)

	cmd := exec.CommandContext(t.Context(), "git", full...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeFileAt(t *testing.T, path, content string) {
	t.Helper()

	err := os.MkdirAll(filepath.Dir(path), 0o750)
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	err = os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newConfigRepo(t *testing.T) string {
	t.Helper()

	_, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not available")
	}

	dir := t.TempDir()

	runGit(t, dir, "init", "-b", "main")
	writeFileAt(t, filepath.Join(dir, "dev", "settings.yml"), settingsDev)
	writeFileAt(t, filepath.Join(dir, "dev", "shop", "docker-compose.yml"), shopCompose)
	writeFileAt(t, filepath.Join(dir, "dev", "shop", ".env"), shopEnv)
	writeFileAt(t, filepath.Join(dir, "prod", "settings.yml"), "version: \"1\"\nprojects: []\n")
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "init")

	return "file://" + dir
}

func newHostConfig(t *testing.T, repoURL string) *config.HostConfig {
	t.Helper()

	hostDir := t.TempDir()
	envPath := filepath.Join(hostDir, ".env")
	configPath := filepath.Join(hostDir, ".apinfra")

	writeFileAt(t, envPath, "SHOP_TOKEN=from-host\n")
	writeFileAt(t, configPath, "namespace=dev\n")

	return &config.HostConfig{
		Namespace:  "dev",
		RepoURL:    repoURL,
		RepoBranch: "main",
		GitTimeout: gitTimeout,
		EnvPath:    envPath,
		ConfigPath: configPath,
	}
}

func loadWorkspace(t *testing.T, cfg *config.HostConfig, namespace string) (*deployment.Workspace, error) {
	t.Helper()

	source := adapters.NewGitConfigSource(cfg, events.NewFileStore(filepath.Join(t.TempDir(), ".events")))

	workspace, err := source.LoadWorkspace(t.Context(), namespace)
	if err != nil {
		return nil, fmt.Errorf("load workspace: %w", err)
	}

	return workspace, nil
}

func TestGitConfigSource_LoadWorkspace_AssemblesProjectsAndHostMaps(t *testing.T) {
	t.Parallel()

	cfg := newHostConfig(t, newConfigRepo(t))

	workspace, err := loadWorkspace(t, cfg, "")
	if err != nil {
		t.Fatalf("LoadWorkspace: %v", err)
	}

	if len(workspace.Projects) != 2 {
		t.Fatalf("want 2 projects, got %+v", workspace.Projects)
	}

	shop, worker := workspace.Projects[0], workspace.Projects[1]

	if shop.Name != "shop" || shop.DomainName != "shop.example.com" || shop.EntryPoint != "web" || shop.EntryPointPort != "8080" {
		t.Fatalf("shop spec = %+v", shop)
	}

	if shop.Namespace != "dev" || shop.NamespacePrefix != "dev" {
		t.Fatalf("shop namespace = %q/%q", shop.Namespace, shop.NamespacePrefix)
	}

	if shop.ComposeYAML != shopCompose || shop.SidecarEnvRaw != shopEnv {
		t.Fatalf("shop files: compose=%q env=%q", shop.ComposeYAML, shop.SidecarEnvRaw)
	}

	if worker.Name != "worker" || worker.ComposeYAML != "" || worker.SidecarEnvRaw != "" {
		t.Fatalf("worker without files must load with empty compose/env, got %+v", worker)
	}

	if workspace.HostEnv["SHOP_TOKEN"] != "from-host" || workspace.HostSettings["namespace"] != "dev" {
		t.Fatalf("host maps: env=%v settings=%v", workspace.HostEnv, workspace.HostSettings)
	}
}

func TestGitConfigSource_LoadWorkspace_NamespaceSelection(t *testing.T) {
	t.Parallel()

	cfg := newHostConfig(t, newConfigRepo(t))

	prod, err := loadWorkspace(t, cfg, "prod")
	if err != nil {
		t.Fatalf("LoadWorkspace(prod): %v", err)
	}

	if len(prod.Projects) != 0 {
		t.Fatalf("explicit namespace prod must win over host config, got %+v", prod.Projects)
	}
}

func TestGitConfigSource_LoadWorkspace_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(t *testing.T, cfg *config.HostConfig)
		namespace string
		wantErr   string
	}{
		{
			name: "placeholder_repo_url",
			mutate: func(_ *testing.T, cfg *config.HostConfig) {
				cfg.RepoURL = "https://github.com/example/infra-config.git"
			},
			wantErr: "placeholder",
		},
		{
			name: "unreachable_repo",
			mutate: func(t *testing.T, cfg *config.HostConfig) {
				t.Helper()

				cfg.RepoURL = "file://" + filepath.Join(t.TempDir(), "absent")
			},
			wantErr: "load config repo",
		},
		{
			name:      "namespace_folder_missing",
			mutate:    func(_ *testing.T, _ *config.HostConfig) {},
			namespace: "staging",
			wantErr:   "staging",
		},
		{
			name: "host_env_missing",
			mutate: func(t *testing.T, cfg *config.HostConfig) {
				t.Helper()

				cfg.EnvPath = filepath.Join(t.TempDir(), "absent.env")
			},
			wantErr: "read host env",
		},
		{
			name: "host_config_missing",
			mutate: func(t *testing.T, cfg *config.HostConfig) {
				t.Helper()

				cfg.ConfigPath = filepath.Join(t.TempDir(), "absent.apinfra")
			},
			wantErr: "read host config",
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			cfg := newHostConfig(t, newConfigRepo(t))
			tcase.mutate(t, cfg)

			ws, err := loadWorkspace(t, cfg, tcase.namespace)
			if err == nil {
				t.Fatalf("want error, got workspace %+v", ws)
			}

			if !strings.Contains(err.Error(), tcase.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tcase.wantErr)
			}
		})
	}
}
