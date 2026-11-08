package adapters_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/domain/nginx"
	"github.com/uservseatb/apinfra/internal/infrastructure/adapters"
)

const (
	dockerStub = `#!/bin/sh
printf '%s\n' "$*" >> "$STUB_CALLS"
if [ "$1" = "login" ]; then
  IFS= read -r line
  printf '%s' "$line" > "$STUB_STDIN"
fi
case "$*" in
  *"nginx -t"*) rc=$STUB_T_RC; msg=$STUB_T_ERR ;;
  *"nginx -s reload"*) rc=$STUB_R_RC; msg=$STUB_R_ERR ;;
  *) rc=$STUB_RC; msg=$STUB_ERR ;;
esac
if [ -n "$msg" ]; then printf '%s\n' "$msg" >&2; fi
if [ -n "$STUB_SLEEP" ]; then exec sleep "$STUB_SLEEP"; fi
exit "${rc:-0}"
`
	digStub = `#!/bin/sh
printf '%s\n' "$*" >> "$STUB_CALLS"
if [ -n "$STUB_ERR" ]; then printf '%s\n' "$STUB_ERR" >&2; fi
printf '%s' "$STUB_OUT"
exit "${STUB_RC:-0}"
`
	cliTimeout = 10 * time.Second
	shopName   = "shop"
	proxyNot   = "Error response from daemon: Container abc is not running"
)

type stubEnv struct {
	calls string
	stdin string
}

// newStubEnv puts fake docker and dig binaries first in PATH. It uses t.Setenv,
// so callers must not be parallel.
func newStubEnv(t *testing.T) *stubEnv {
	t.Helper()

	binDir := t.TempDir()
	stateDir := t.TempDir()

	for name, script := range map[string]string{"docker": dockerStub, "dig": digStub} {
		path := filepath.Join(binDir, name)

		err := os.WriteFile(path, []byte(script), 0o600)
		if err != nil {
			t.Fatalf("write stub %s: %v", name, err)
		}

		err = os.Chmod(path, 0o700)
		if err != nil {
			t.Fatalf("chmod stub %s: %v", name, err)
		}
	}

	env := &stubEnv{calls: filepath.Join(stateDir, "calls"), stdin: filepath.Join(stateDir, "stdin")}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STUB_CALLS", env.calls)
	t.Setenv("STUB_STDIN", env.stdin)

	for _, key := range []string{
		"STUB_RC", "STUB_ERR", "STUB_OUT", "STUB_SLEEP", "STUB_T_RC", "STUB_T_ERR", "STUB_R_RC", "STUB_R_ERR",
	} {
		t.Setenv(key, "")
	}

	return env
}

func (e *stubEnv) recorded(t *testing.T) []string {
	t.Helper()

	data, err := os.ReadFile(e.calls)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		t.Fatalf("read calls: %v", err)
	}

	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestCliRunner_ComposeCommands_BuildExpectedArgv(t *testing.T) {
	infra := t.TempDir()
	runner := adapters.NewCliRunner(infra, false)
	appCompose := filepath.Join(infra, "apps", shopName, "docker-compose.yml")

	tests := []struct {
		name string
		run  func(ctx context.Context) error
		want string
	}{
		{
			name: "pull_proxy",
			run:  func(ctx context.Context) error { return runner.ComposePullProxy(ctx, cliTimeout) },
			want: "compose -f " + filepath.Join(infra, "docker-compose.yml") + " pull",
		},
		{
			name: "up_proxy",
			run:  func(ctx context.Context) error { return runner.ComposeUpProxy(ctx, cliTimeout) },
			want: "compose -f " + filepath.Join(infra, "docker-compose.yml") + " up -d --remove-orphans",
		},
		{
			name: "pull_app_uses_project_prefix",
			run:  func(ctx context.Context) error { return runner.ComposePullApp(ctx, shopName, cliTimeout) },
			want: "compose -f " + appCompose + " -p apinfra_shop pull",
		},
		{
			name: "up_app_uses_project_prefix",
			run:  func(ctx context.Context) error { return runner.ComposeUpApp(ctx, shopName, cliTimeout) },
			want: "compose -f " + appCompose + " -p apinfra_shop up -d --remove-orphans",
		},
		{
			name: "down_app_with_compose_file",
			run:  func(ctx context.Context) error { return runner.ComposeDownApp(ctx, shopName, true, cliTimeout) },
			want: "compose -f " + appCompose + " -p apinfra_shop down",
		},
		{
			name: "down_removed_app_without_compose_file",
			run:  func(ctx context.Context) error { return runner.ComposeDownApp(ctx, shopName, false, cliTimeout) },
			want: "compose -p apinfra_shop down",
		},
		{
			name: "prune_images",
			run:  func(ctx context.Context) error { return runner.PruneImages(ctx, cliTimeout) },
			want: "image prune -af",
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			stubs := newStubEnv(t)

			err := tcase.run(t.Context())
			if err != nil {
				t.Fatalf("run: %v", err)
			}

			if got := stubs.recorded(t); !slices.Equal(got, []string{tcase.want}) {
				t.Fatalf("docker called with %q, want %q", got, tcase.want)
			}
		})
	}
}

func TestCliRunner_ComposeFailure_NamesProjectAndKeepsCause(t *testing.T) {
	stubs := newStubEnv(t)

	t.Setenv("STUB_RC", "1")
	t.Setenv("STUB_ERR", "pull access denied")

	runner := adapters.NewCliRunner(t.TempDir(), false)

	err := runner.ComposePullApp(t.Context(), shopName, cliTimeout)
	if err == nil {
		t.Fatal("want error")
	}

	if !strings.Contains(err.Error(), "project="+shopName) {
		t.Fatalf("error must name the project: %v", err)
	}

	if len(stubs.recorded(t)) != 1 {
		t.Fatalf("want a single docker call, got %v", stubs.recorded(t))
	}
}

func TestCliRunner_Exec_TimeoutIsEnforced(t *testing.T) {
	newStubEnv(t)

	t.Setenv("STUB_SLEEP", "30")

	runner := adapters.NewCliRunner(t.TempDir(), false)
	start := time.Now()

	err := runner.ComposeUpProxy(t.Context(), 200*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout not enforced: %s", elapsed)
	}
}

func TestCliRunner_RegistryLogin_SendsTokenOnStdinOnly(t *testing.T) {
	const token = "ghp_super_secret"

	t.Run("success", func(t *testing.T) {
		stubs := newStubEnv(t)
		runner := adapters.NewCliRunner(t.TempDir(), false)

		err := runner.RegistryLogin(t.Context(), "ghcr.io", "bot", token, cliTimeout)
		if err != nil {
			t.Fatalf("RegistryLogin: %v", err)
		}

		calls := stubs.recorded(t)
		if !slices.Equal(calls, []string{"login ghcr.io -u bot --password-stdin"}) {
			t.Fatalf("docker argv = %q", calls)
		}

		got, err := os.ReadFile(stubs.stdin)
		if err != nil || string(got) != token {
			t.Fatalf("stdin = %q err=%v, want token", got, err)
		}
	})

	t.Run("failure_does_not_leak_token", func(t *testing.T) {
		newStubEnv(t)
		t.Setenv("STUB_RC", "1")
		t.Setenv("STUB_ERR", "unauthorized")

		runner := adapters.NewCliRunner(t.TempDir(), false)

		err := runner.RegistryLogin(t.Context(), "ghcr.io", "bot", token, cliTimeout)
		if err == nil {
			t.Fatal("want error")
		}

		if strings.Contains(err.Error(), token) {
			t.Fatalf("token leaked into error: %v", err)
		}
	})
}

func TestCliRunner_NginxTestAndReload(t *testing.T) {
	tests := []struct {
		name         string
		testRC       string
		testErr      string
		reloadRC     string
		reloadErr    string
		wantTestErr  error
		wantReload   error
		wantReloadOK bool
		wantCalls    []string
	}{
		{
			name:         "healthy_proxy",
			wantReloadOK: true,
			wantCalls: []string{
				"exec apinfra-proxy nginx -t",
				"exec apinfra-proxy nginx -t",
				"exec apinfra-proxy nginx -s reload",
			},
		},
		{
			name:        "container_not_running_is_not_ready",
			testRC:      "1",
			testErr:     proxyNot,
			wantTestErr: nginx.ErrProxyNotReady,
			wantReload:  nginx.ErrProxyNotReady,
			wantCalls:   []string{"exec apinfra-proxy nginx -t", "exec apinfra-proxy nginx -t"},
		},
		{
			name:        "container_restarting_is_not_ready",
			testRC:      "1",
			testErr:     "Error response from daemon: Container abc is restarting",
			wantTestErr: nginx.ErrProxyNotReady,
			wantReload:  nginx.ErrProxyNotReady,
			wantCalls:   []string{"exec apinfra-proxy nginx -t", "exec apinfra-proxy nginx -t"},
		},
		{
			name:        "container_missing_is_not_ready",
			testRC:      "1",
			testErr:     "Error: No such container: apinfra-proxy",
			wantTestErr: nginx.ErrProxyNotReady,
			wantReload:  nginx.ErrProxyNotReady,
			wantCalls:   []string{"exec apinfra-proxy nginx -t", "exec apinfra-proxy nginx -t"},
		},
		{
			name:        "invalid_config_is_a_real_failure",
			testRC:      "1",
			testErr:     "nginx: [emerg] unknown directive \"bogus\"",
			wantTestErr: nginx.ErrNginxTestFailed,
			wantReload:  nginx.ErrNginxTestFailed,
			wantCalls:   []string{"exec apinfra-proxy nginx -t", "exec apinfra-proxy nginx -t"},
		},
		{
			name:       "reload_when_proxy_dies_between_test_and_reload",
			reloadRC:   "1",
			reloadErr:  proxyNot,
			wantReload: nginx.ErrProxyNotReady,
			wantCalls: []string{
				"exec apinfra-proxy nginx -t",
				"exec apinfra-proxy nginx -t",
				"exec apinfra-proxy nginx -s reload",
			},
			wantReloadOK: false,
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			stubs := newStubEnv(t)

			t.Setenv("STUB_T_RC", tcase.testRC)
			t.Setenv("STUB_T_ERR", tcase.testErr)
			t.Setenv("STUB_R_RC", tcase.reloadRC)
			t.Setenv("STUB_R_ERR", tcase.reloadErr)

			runner := adapters.NewCliRunner(t.TempDir(), false)

			testErr := runner.TestConfiguration(t.Context(), cliTimeout)
			if !errors.Is(testErr, tcase.wantTestErr) {
				t.Fatalf("TestConfiguration = %v, want %v", testErr, tcase.wantTestErr)
			}

			reloadErr := runner.Reload(t.Context(), cliTimeout)

			switch {
			case tcase.wantReloadOK && reloadErr != nil:
				t.Fatalf("Reload: %v", reloadErr)
			case !tcase.wantReloadOK && !errors.Is(reloadErr, tcase.wantReload):
				t.Fatalf("Reload = %v, want %v", reloadErr, tcase.wantReload)
			}

			if got := stubs.recorded(t); !slices.Equal(got, tcase.wantCalls) {
				t.Fatalf("docker calls = %q, want %q", got, tcase.wantCalls)
			}
		})
	}
}

func TestCliRunner_LookupShortIPs_ParsesDigOutput(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []string
	}{
		{name: "two_a_records", out: "1.2.3.4\n5.6.7.8\n", want: []string{"1.2.3.4", "5.6.7.8"}},
		{name: "cname_chain_is_skipped", out: "alias.example.com.\n9.9.9.9\n", want: []string{"9.9.9.9"}},
		{name: "ipv6_and_blank_lines", out: "\n2001:db8::1\n\n  10.0.0.1  \n", want: []string{"2001:db8::1", "10.0.0.1"}},
		{name: "no_records", out: "", want: []string{}},
		{name: "garbage_only", out: ";; connection timed out\n", want: []string{}},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			stubs := newStubEnv(t)

			t.Setenv("STUB_OUT", tcase.out)

			runner := adapters.NewCliRunner(t.TempDir(), false)

			got, err := runner.LookupShortIPs(t.Context(), "app.example.com", cliTimeout)
			if err != nil {
				t.Fatalf("LookupShortIPs: %v", err)
			}

			if !slices.Equal(got, tcase.want) {
				t.Fatalf("ips = %q, want %q", got, tcase.want)
			}

			wantCall := "+short +time=2 +tries=1 app.example.com"
			if calls := stubs.recorded(t); !slices.Equal(calls, []string{wantCall}) {
				t.Fatalf("dig called with %q, want %q", calls, wantCall)
			}
		})
	}
}

func TestCliRunner_LookupShortIPs_Errors(t *testing.T) {
	tests := []struct {
		name       string
		domain     string
		rc         string
		stderr     string
		wantErr    error
		wantDetail string
		wantCalled bool
	}{
		{name: "empty_domain", domain: "", wantErr: nginx.ErrEmptyDomain},
		{name: "shell_metacharacters_rejected", domain: "a.com;rm -rf /", wantErr: nginx.ErrDNSPreflightFailed},
		{name: "backtick_rejected", domain: "`id`.example.com", wantErr: nginx.ErrDNSPreflightFailed},
		{
			name: "dig_failure_reports_stderr", domain: "app.example.com", rc: "9", stderr: "no servers could be reached",
			wantErr: nginx.ErrDNSPreflightFailed, wantDetail: "no servers could be reached", wantCalled: true,
		},
		{
			name: "dig_failure_without_stderr_reports_exit", domain: "app.example.com", rc: "1",
			wantErr: nginx.ErrDNSPreflightFailed, wantDetail: "exit status 1", wantCalled: true,
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			stubs := newStubEnv(t)

			t.Setenv("STUB_RC", tcase.rc)
			t.Setenv("STUB_ERR", tcase.stderr)

			runner := adapters.NewCliRunner(t.TempDir(), false)

			_, err := runner.LookupShortIPs(t.Context(), tcase.domain, cliTimeout)
			if !errors.Is(err, tcase.wantErr) {
				t.Fatalf("err = %v, want %v", err, tcase.wantErr)
			}

			if tcase.wantDetail != "" && !strings.Contains(err.Error(), tcase.wantDetail) {
				t.Fatalf("err = %v, want detail %q", err, tcase.wantDetail)
			}

			if called := len(stubs.recorded(t)) > 0; called != tcase.wantCalled {
				t.Fatalf("dig invoked = %v, want %v", called, tcase.wantCalled)
			}
		})
	}
}
