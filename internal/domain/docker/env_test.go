package docker_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uservseatb/apinfra/internal/domain/docker"
)

const (
	composeWithTag = "services:\n  web:\n    image: nginx:${TAG}\n"
	errMissingVars = "missing env vars"
)

func TestValidateConfigDirEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		projects []docker.Project
		hostEnv  map[string]string
		wantErrs []string // all substrings; empty = valid
	}{
		{
			name:     "no_variables",
			projects: []docker.Project{shopProject(baseCompose)},
		},
		{
			name:     "empty_compose_skipped",
			projects: []docker.Project{shopProject("")},
		},
		{
			name: "covered_by_sidecar",
			projects: []docker.Project{func() docker.Project {
				p := shopProject(composeWithTag)
				p.SidecarEnvRaw = "TAG=1.30\n"

				return p
			}()},
		},
		{
			name: "sidecar_dotted_key_normalized",
			projects: []docker.Project{func() docker.Project {
				p := shopProject(composeWithTag)
				p.SidecarEnvRaw = "tag=1.30\n"

				return p
			}()},
		},
		{
			name: "sidecar_dot_replaced_with_underscore",
			projects: []docker.Project{func() docker.Project {
				p := shopProject("services:\n  web:\n    image: nginx:${DB_TAG}\n")
				p.SidecarEnvRaw = "db.tag=1\n"

				return p
			}()},
		},
		{
			name:     "covered_by_host_prefix",
			projects: []docker.Project{shopProject(composeWithTag)},
			hostEnv:  map[string]string{"SHOP__TAG": "1.30"},
		},
		{
			name:     "missing_everywhere",
			projects: []docker.Project{shopProject(composeWithTag)},
			wantErrs: []string{"project=shop", errMissingVars, "TAG", "SHOP__VAR"},
		},
		{
			name:     "other_project_prefix_does_not_cover",
			projects: []docker.Project{shopProject(composeWithTag)},
			hostEnv:  map[string]string{"BLOG__TAG": "1", "TAG": "unprefixed"},
			wantErrs: []string{errMissingVars, "TAG"},
		},
		{
			name:     "empty_host_value_counts_as_defined",
			projects: []docker.Project{shopProject(composeWithTag)},
			hostEnv:  map[string]string{"SHOP__TAG": ""},
		},
		{
			name: "nested_project_host_key_does_not_cover_shorter",
			projects: []docker.Project{
				{Name: "app", ComposeYAML: "services:\n  web:\n    image: nginx:${BILLING_TOKEN}\n"},
				{Name: "app_billing", ComposeYAML: baseCompose},
			},
			hostEnv:  map[string]string{"APP_BILLING__TOKEN": "secret"},
			wantErrs: []string{"project=app", errMissingVars, "BILLING_TOKEN"},
		},
		{
			name:     "legacy_single_underscore_host_key_does_not_cover",
			projects: []docker.Project{shopProject(composeWithTag)},
			hostEnv:  map[string]string{"SHOP_TAG": "1.30"},
			wantErrs: []string{errMissingVars, "TAG"},
		},
		{
			name: "modifiers_are_not_required",
			projects: []docker.Project{shopProject(
				"services:\n  web:\n    image: nginx:${TAG:-latest}\n    command: ${CMD:?must be set}\n",
			)},
		},
		{
			name: "sidecar_key_collision",
			projects: []docker.Project{func() docker.Project {
				p := shopProject(composeWithTag)
				p.SidecarEnvRaw = "a.b=1\nA_B=2\n"

				return p
			}()},
			wantErrs: []string{"project=shop", "collision", "in sidecar .env"},
		},
		{
			name: "invalid_compose_yaml",
			projects: []docker.Project{
				shopProject("services: [oops"),
			},
			wantErrs: []string{"invalid compose YAML"},
		},
		{
			name: "aggregates_all_failing_projects",
			projects: []docker.Project{
				shopProject(composeWithTag),
				{Name: "blog", ComposeYAML: "services:\n  web:\n    image: ${IMG}\n"},
				{Name: "ok", ComposeYAML: baseCompose},
			},
			wantErrs: []string{"project=shop", "project=blog", "IMG"},
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newDockerEnv(t)

			err := env.svc.ValidateConfigDirEnv(tcase.projects, tcase.hostEnv)
			if len(tcase.wantErrs) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("want error containing %v, got nil", tcase.wantErrs)
			}

			for _, want := range tcase.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %q, want containing %q", err.Error(), want)
				}
			}
		})
	}
}

func TestApply_ProjectEnvFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		sidecar string
		hostEnv map[string]string
		want    []string
	}{
		{
			name: "empty_when_no_sources",
			want: nil,
		},
		{
			name:    "sidecar_keys_normalized_and_sorted",
			sidecar: "db.pass=secret\nAPP_NAME=shop\n",
			want:    []string{"APP_NAME=shop", "DB_PASS=secret"},
		},
		{
			name:    "host_prefix_is_stripped",
			hostEnv: map[string]string{"SHOP__TOKEN": "abc"},
			want:    []string{"TOKEN=abc"},
		},
		{
			name:    "other_projects_host_keys_excluded",
			hostEnv: map[string]string{"SHOP__TOKEN": "abc", "BLOG__TOKEN": "zzz", "PLAIN": "x", "SHOP__": "empty-suffix"},
			want:    []string{"TOKEN=abc"},
		},
		{
			name:    "host_wins_on_collision_and_sources_merge",
			sidecar: "TOKEN=from-sidecar\nKEEP=k\n",
			hostEnv: map[string]string{"SHOP__TOKEN": "from-host", "SHOP__EXTRA": "e"},
			want:    []string{"EXTRA=e", "KEEP=k", "TOKEN=from-host"},
		},
		{
			name:    "legacy_single_underscore_host_key_ignored",
			hostEnv: map[string]string{"SHOP_TOKEN": "abc"},
			want:    nil,
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newDockerEnv(t)
			project := shopProject(baseCompose)
			project.SidecarEnvRaw = tcase.sidecar

			_, err := env.apply(t, []docker.Project{project}, tcase.hostEnv, nil, true)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			got := strings.TrimSpace(env.read(t, env.appFile(".env")))

			want := strings.Join(tcase.want, "\n")
			if got != want {
				t.Fatalf(".env = %q, want %q", got, want)
			}
		})
	}
}

func TestApply_HostEnvNestedProjectPrefixes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		projects []docker.Project
		hostEnv  map[string]string
		wantEnv  map[string]string // project name → apps/<name>/.env content (trimmed)
	}{
		{
			name: "app_and_app_billing_no_cross_leak",
			projects: []docker.Project{
				namedProject("app"),
				namedProject("app_billing"),
			},
			hostEnv: map[string]string{
				"APP__TOKEN":         "app-secret",
				"APP_BILLING__TOKEN": "billing-secret",
			},
			wantEnv: map[string]string{
				"app":         "TOKEN=app-secret",
				"app_billing": "TOKEN=billing-secret",
			},
		},
		{
			name: "underscore_nested_A_A_B_A_B_C",
			projects: []docker.Project{
				namedProject("A"),
				namedProject("A_B"),
				namedProject("A_B_C"),
			},
			hostEnv: map[string]string{
				"A__TOKEN":     "a",
				"A_B__TOKEN":   "ab",
				"A_B_C__TOKEN": "abc",
			},
			wantEnv: map[string]string{
				"A":     "TOKEN=a",
				"A_B":   "TOKEN=ab",
				"A_B_C": "TOKEN=abc",
			},
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newDockerEnv(t)

			_, err := env.apply(t, tcase.projects, tcase.hostEnv, nil, true)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			for project, want := range tcase.wantEnv {
				path := filepath.Join(env.infra, "apps", project, ".env")
				got := strings.TrimSpace(env.read(t, path))
				if got != want {
					t.Fatalf("project %s .env = %q, want %q", project, got, want)
				}
			}
		})
	}
}

func TestApply_ComposeInterpolation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		compose   string
		sidecar   string
		hostEnv   map[string]string
		wantImage string
		wantErr   bool
	}{
		{
			name:      "from_sidecar",
			compose:   composeWithTag,
			sidecar:   "TAG=1.30\n",
			wantImage: "nginx:1.30",
		},
		{
			name:      "from_host_prefix",
			compose:   composeWithTag,
			hostEnv:   map[string]string{"SHOP__TAG": "1.29"},
			wantImage: "nginx:1.29",
		},
		{
			name:      "host_beats_sidecar",
			compose:   composeWithTag,
			sidecar:   "TAG=sidecar\n",
			hostEnv:   map[string]string{"SHOP__TAG": "host"},
			wantImage: "nginx:host",
		},
		{
			name:      "default_applies_when_unset",
			compose:   "services:\n  web:\n    image: nginx:${TAG:-stable}\n",
			wantImage: "nginx:stable",
		},
		{
			name:      "process_environment_is_ignored",
			compose:   "services:\n  web:\n    image: nginx:${APINFRA_TEST_ONLY_PATH_LIKE:-fallback}\n",
			wantImage: "nginx:fallback",
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newDockerEnv(t)
			project := shopProject(tcase.compose)
			project.SidecarEnvRaw = tcase.sidecar

			_, err := env.apply(t, []docker.Project{project}, tcase.hostEnv, nil, true)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			got, _ := serviceOf(appCompose(t, env), "web")["image"].(string)
			if got != tcase.wantImage {
				t.Fatalf("image = %q, want %q", got, tcase.wantImage)
			}
		})
	}
}

func TestApply_UnresolvedVariableFailsBeforeDocker(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)

	_, err := env.svc.Apply(
		context.Background(), []docker.Project{shopProject("services:\n  web:\n    image: nginx:${TAG:?TAG is required}\n")},
		nil, nil, true,
	)
	if err == nil {
		t.Fatal("expected error for unresolved required variable")
	}

	if len(env.cli.calls) != 0 {
		t.Fatalf("docker must not be touched: %v", env.cli.calls)
	}
}
