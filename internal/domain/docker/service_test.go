package docker_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/domain/docker"
	"github.com/uservseatb/apinfra/internal/infrastructure"
	"gopkg.in/yaml.v3"
)

const (
	projShop       = "shop"
	proxyContainer = "apinfra-proxy"
	shopComposeID  = "apinfra_shop"
	baseCompose    = "services:\n  web:\n    image: nginx:1.30-alpine\n"
	multiCompose   = "services:\n  web:\n    image: nginx:1.30-alpine\n  db:\n    image: postgres:16-alpine\n"

	keyRegHost  = "containerRegistryHost"
	keyRegUser  = "containerRegistryUser"
	keyRegToken = "containerRegistryToken"
)

var (
	errTestPull  = errors.New("test: pull failed")
	errTestLogin = errors.New("test: login failed")
	errTestList  = errors.New("test: docker api down")
	errTestDown  = errors.New("test: down failed")
)

// fakeCLI records the docker CLI operations the service performs.
type fakeCLI struct {
	calls     []string
	errs      map[string]error
	downFile  map[string]bool
	loginUser string
}

func newFakeCLI() *fakeCLI {
	return &fakeCLI{errs: map[string]error{}, downFile: map[string]bool{}}
}

func doOp(f *fakeCLI, op string) error {
	f.calls = append(f.calls, op)

	return f.errs[op]
}

func (f *fakeCLI) ComposePullProxy(context.Context, time.Duration) error {
	return doOp(f, "pull-proxy")
}
func (f *fakeCLI) ComposeUpProxy(context.Context, time.Duration) error { return doOp(f, "up-proxy") }

func (f *fakeCLI) ComposePullApp(_ context.Context, name string, _ time.Duration) error {
	return doOp(f, "pull-app:"+name)
}

func (f *fakeCLI) ComposeUpApp(_ context.Context, name string, _ time.Duration) error {
	return doOp(f, "up-app:"+name)
}

func (f *fakeCLI) ComposeDownApp(_ context.Context, name string, useFile bool, _ time.Duration) error {
	f.downFile[name] = useFile

	return doOp(f, "down:"+name)
}

func (f *fakeCLI) RegistryLogin(_ context.Context, host, user, _ string, _ time.Duration) error {
	f.loginUser = user

	return doOp(f, "login:"+host)
}

func (f *fakeCLI) PruneImages(context.Context, time.Duration) error { return doOp(f, "prune") }

type fakeLister struct {
	containers []docker.Container
	err        error
}

func (l *fakeLister) ListContainers(context.Context) ([]docker.Container, error) {
	return l.containers, l.err
}

type recordedEvent struct {
	kind   string
	fields map[string]string
}

type fakeEvents struct{ got []recordedEvent }

func (e *fakeEvents) Write(kind string, fields map[string]string) error {
	e.got = append(e.got, recordedEvent{kind: kind, fields: fields})

	return nil
}

type dockerEnv struct {
	infra  string
	cli    *fakeCLI
	lister *fakeLister
	events *fakeEvents
	svc    *docker.Service
}

func newDockerEnv(t *testing.T) *dockerEnv {
	t.Helper()

	return newDockerEnvWithTrusted(t, "")
}

func newDockerEnvWithTrusted(t *testing.T, trustedImagesPath string) *dockerEnv {
	t.Helper()

	infra := filepath.Join(t.TempDir(), "infra")
	cli := newFakeCLI()
	lister := &fakeLister{}
	evs := &fakeEvents{}

	return &dockerEnv{
		infra:  infra,
		cli:    cli,
		lister: lister,
		events: evs,
		svc: docker.NewService(
			infrastructure.NewStackFS(infra),
			cli,
			lister,
			evs,
			docker.Config{TrustedImagesPath: trustedImagesPath},
		),
	}
}

func writeTrustedImages(t *testing.T, lines ...string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), ".trusted_images")
	content := strings.Join(lines, "\n") + "\n"

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("write trusted images: %v", err)
	}

	return path
}

func (e *dockerEnv) apply(
	t *testing.T, projects []docker.Project, hostEnv, hostSettings map[string]string, applyContainers bool,
) (docker.ApplyResult, error) {
	t.Helper()

	res, err := e.svc.Apply(context.Background(), projects, hostEnv, hostSettings, applyContainers)
	if err != nil {
		return res, fmt.Errorf("apply: %w", err)
	}

	return res, nil
}

func (e *dockerEnv) applyAll(t *testing.T, projects ...docker.Project) (docker.ApplyResult, error) {
	t.Helper()

	return e.apply(t, projects, nil, nil, true)
}

func (e *dockerEnv) appFile(name string) string {
	return filepath.Join(e.infra, "apps", projShop, name)
}

func (e *dockerEnv) read(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

func shopProject(compose string) docker.Project {
	return docker.Project{Name: projShop, NamespacePrefix: "dev", ComposeYAML: compose, EntryPoint: "web"}
}

func namedProject(name string) docker.Project {
	return docker.Project{Name: name, NamespacePrefix: "dev", ComposeYAML: baseCompose}
}

func proxyUp(uptime time.Duration) docker.Container {
	return docker.Container{Name: proxyContainer, State: "running", UpTime: uptime}
}

func appRunning(project string) docker.Container {
	return docker.Container{
		Name:           project + "-c",
		State:          "running",
		ComposeProject: "apinfra_" + project,
		ComposeService: "web",
	}
}

func appService(project, service, state string) docker.Container {
	return docker.Container{
		Name:           project + "-" + service,
		State:          state,
		ComposeProject: "apinfra_" + project,
		ComposeService: service,
	}
}

func hasCall(calls []string, want string) bool { return slices.Contains(calls, want) }

func readYAML(t *testing.T, content string) map[string]any {
	t.Helper()

	var doc map[string]any

	err := yaml.Unmarshal([]byte(content), &doc)
	if err != nil {
		t.Fatalf("parse yaml: %v\n%s", err, content)
	}

	return doc
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)

	return m
}

func serviceOf(doc map[string]any, name string) map[string]any {
	return asMap(asMap(doc["services"])[name])
}

func TestApply_FirstRunPullsThenStartsProxyAndApps(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)

	res, err := env.applyAll(t, shopProject(baseCompose))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{"pull-proxy", "up-proxy", "pull-app:shop", "up-app:shop"}
	if !slices.Equal(env.cli.calls, want) {
		t.Fatalf("calls = %v, want %v", env.cli.calls, want)
	}

	if !res.Restarted || !res.ProxyRunning || res.ProxyReady {
		t.Fatalf("result = %+v, want restarted, proxy running but not ready yet", res)
	}

	if got := res.ProxyHosts[projShop]; got != "web.apinfra_shop" {
		t.Fatalf("ProxyHosts = %v", res.ProxyHosts)
	}

	for _, path := range []string{
		filepath.Join(env.infra, "docker-compose.yml"),
		env.appFile("docker-compose.yml"),
		env.appFile(".env"),
	} {
		_, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("expected %s: %v", path, statErr)
		}
	}
}

func TestApply_SecondRunIsZeroDiskAndZeroRestart(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)
	project := shopProject(baseCompose)

	_, err := env.applyAll(t, project)
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}

	paths := []string{
		filepath.Join(env.infra, "docker-compose.yml"),
		env.appFile("docker-compose.yml"),
		env.appFile(".env"),
	}

	type snapshot struct {
		mod  time.Time
		data string
	}

	before := map[string]snapshot{}

	for _, path := range paths {
		st, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}

		before[path] = snapshot{mod: st.ModTime(), data: env.read(t, path)}
	}

	env.cli.calls = nil
	env.lister.containers = []docker.Container{proxyUp(time.Minute), appRunning(projShop)}

	res, err := env.applyAll(t, project)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	if len(env.cli.calls) != 0 {
		t.Fatalf("healthy state must not touch docker: %v", env.cli.calls)
	}

	if res.Restarted || !res.ProxyRunning || !res.ProxyReady {
		t.Fatalf("result = %+v", res)
	}

	for _, path := range paths {
		st, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}

		if !st.ModTime().Equal(before[path].mod) || env.read(t, path) != before[path].data {
			t.Fatalf("%s was rewritten on an unchanged cycle", path)
		}
	}
}

func TestApply_RestartDecisions(t *testing.T) {
	t.Parallel()

	changedCompose := baseCompose + "    environment:\n      A: \"1\"\n"

	tests := []struct {
		name       string
		first      []docker.Project // nil → shopProject(baseCompose)
		second     []docker.Project
		containers []docker.Container
		listErr    error
		wantCalls  []string
		wantProxy  bool // ProxyRunning
		wantReady  bool
	}{
		{
			name:       "compose_changed_restarts_only_that_app",
			second:     []docker.Project{shopProject(changedCompose)},
			containers: []docker.Container{proxyUp(time.Minute), appRunning(projShop)},
			wantCalls:  []string{"pull-app:shop", "up-app:shop"},
			wantProxy:  true,
			wantReady:  true,
		},
		{
			name: "inbound_port_change_restarts_proxy",
			second: []docker.Project{func() docker.Project {
				p := shopProject(baseCompose)
				p.InboundPort = "9999"

				return p
			}()},
			containers: []docker.Container{proxyUp(time.Minute), appRunning(projShop)},
			wantCalls:  []string{"pull-proxy", "up-proxy"},
			wantProxy:  true,
			wantReady:  false,
		},
		{
			name:   "stopped_container_heals_stack",
			second: []docker.Project{shopProject(baseCompose)},
			containers: []docker.Container{
				proxyUp(time.Minute),
				appRunning(projShop),
				{Name: "shop-db", State: "exited", ComposeProject: shopComposeID, ComposeService: "db"},
			},
			wantCalls: []string{"pull-app:shop", "up-app:shop"},
			wantProxy: true,
			wantReady: true,
		},
		{
			name:       "missing_app_containers_start_stack",
			second:     []docker.Project{shopProject(baseCompose)},
			containers: []docker.Container{proxyUp(time.Minute)},
			wantCalls:  []string{"pull-app:shop", "up-app:shop"},
			wantProxy:  true,
			wantReady:  true,
		},
		{
			name:   "deleted_service_container_heals_stack",
			first:  []docker.Project{shopProject(multiCompose)},
			second: []docker.Project{shopProject(multiCompose)},
			containers: []docker.Container{
				proxyUp(time.Minute),
				appService(projShop, "web", "running"),
				// db service container was docker rm'd — absent from the list
			},
			wantCalls: []string{"pull-app:shop", "up-app:shop"},
			wantProxy: true,
			wantReady: true,
		},
		{
			name:   "multi_service_all_running_skips_up",
			first:  []docker.Project{shopProject(multiCompose)},
			second: []docker.Project{shopProject(multiCompose)},
			containers: []docker.Container{
				proxyUp(time.Minute),
				appService(projShop, "web", "running"),
				appService(projShop, "db", "running"),
			},
			wantCalls: nil,
			wantProxy: true,
			wantReady: true,
		},
		{
			name:       "proxy_not_running_starts_proxy",
			second:     []docker.Project{shopProject(baseCompose)},
			containers: []docker.Container{appRunning(projShop)},
			wantCalls:  []string{"pull-proxy", "up-proxy"},
			wantProxy:  true,
			wantReady:  false,
		},
		{
			name:       "young_proxy_not_ready_yet",
			second:     []docker.Project{shopProject(baseCompose)},
			containers: []docker.Container{proxyUp(5 * time.Second), appRunning(projShop)},
			wantCalls:  nil,
			wantProxy:  true,
			wantReady:  false,
		},
		{
			name:      "unknown_state_changes_nothing_when_files_unchanged",
			second:    []docker.Project{shopProject(baseCompose)},
			listErr:   errTestList,
			wantCalls: nil,
			wantProxy: false,
			wantReady: false,
		},
		{
			name:      "unknown_state_still_applies_changed_stacks",
			second:    []docker.Project{shopProject(changedCompose)},
			listErr:   errTestList,
			wantCalls: []string{"pull-app:shop", "up-app:shop"},
			wantProxy: false,
			wantReady: false,
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newDockerEnv(t)

			first := tcase.first
			if first == nil {
				first = []docker.Project{shopProject(baseCompose)}
			}

			_, err := env.applyAll(t, first...)
			if err != nil {
				t.Fatalf("first Apply: %v", err)
			}

			env.cli.calls = nil
			env.lister.containers, env.lister.err = tcase.containers, tcase.listErr

			res, err := env.applyAll(t, tcase.second...)
			if err != nil {
				t.Fatalf("second Apply: %v", err)
			}

			if !slices.Equal(env.cli.calls, tcase.wantCalls) {
				t.Fatalf("calls = %v, want %v", env.cli.calls, tcase.wantCalls)
			}

			if res.ProxyRunning != tcase.wantProxy || res.ProxyReady != tcase.wantReady {
				t.Fatalf("result = %+v, want running=%v ready=%v", res, tcase.wantProxy, tcase.wantReady)
			}

			if res.Restarted != (len(tcase.wantCalls) > 0) {
				t.Fatalf("Restarted = %v with calls %v", res.Restarted, tcase.wantCalls)
			}
		})
	}
}

func TestApply_WithoutApplyContainersOnlyWritesFiles(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)

	res, err := env.apply(t, []docker.Project{shopProject(baseCompose)}, nil, nil, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(env.cli.calls) != 0 || res.Restarted {
		t.Fatalf("no docker operations expected: calls=%v res=%+v", env.cli.calls, res)
	}

	_, statErr := os.Stat(env.appFile("docker-compose.yml"))
	if statErr != nil {
		t.Fatalf("compose must still be written: %v", statErr)
	}

	if res.ProxyHosts[projShop] != "web.apinfra_shop" {
		t.Fatalf("ProxyHosts = %v", res.ProxyHosts)
	}
}

func TestApply_DownsUnusedApps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		downErr      error
		wantDirKept  bool
		wantRestart  bool
		wantApplyErr bool
	}{
		{name: "down_ok_removes_dir", wantRestart: true},
		{name: "down_failure_keeps_dir_for_retry", downErr: errTestDown, wantDirKept: true, wantRestart: false},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newDockerEnv(t)

			_, err := env.applyAll(t, shopProject(baseCompose), namedProject("old"))
			if err != nil {
				t.Fatalf("first Apply: %v", err)
			}

			env.cli.calls = nil
			env.cli.errs["down:old"] = tcase.downErr
			env.lister.containers = []docker.Container{
				proxyUp(time.Minute),
				appRunning(projShop),
				appRunning("old"),
				appRunning("ghost"), // no apps/ghost dir: found only through docker labels
			}

			res, err := env.applyAll(t, shopProject(baseCompose))
			if err != nil {
				t.Fatalf("second Apply: %v", err)
			}

			if !hasCall(env.cli.calls, "down:old") || !hasCall(env.cli.calls, "down:ghost") {
				t.Fatalf("calls = %v, want down for old and ghost", env.cli.calls)
			}

			if !env.cli.downFile["old"] || env.cli.downFile["ghost"] {
				t.Fatalf("useComposeFile = %v, want old=true ghost=false", env.cli.downFile)
			}

			_, statErr := os.Stat(filepath.Join(env.infra, "apps", "old"))
			if dirKept := statErr == nil; dirKept != tcase.wantDirKept {
				t.Fatalf("old dir kept = %v, want %v", dirKept, tcase.wantDirKept)
			}

			_, enabledErr := os.Stat(filepath.Join(env.infra, "apps", projShop))
			if enabledErr != nil {
				t.Fatal("enabled app dir must be preserved")
			}

			// ghost's down succeeded, so the cycle counts as restarted either way.
			if !res.Restarted {
				t.Fatalf("Restarted = false, want true (ghost was downed)")
			}
		})
	}
}

func TestApply_PullFailureDoesNotBlockOtherApps(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)
	env.cli.errs["pull-app:alpha"] = errTestPull

	res, err := env.applyAll(t, namedProject("alpha"), namedProject("beta"))
	if !errors.Is(err, errTestPull) {
		t.Fatalf("err = %v, want pull failure surfaced", err)
	}

	if hasCall(env.cli.calls, "up-app:alpha") {
		t.Fatalf("up must be skipped after failed pull: %v", env.cli.calls)
	}

	if !hasCall(env.cli.calls, "up-app:beta") || !res.Restarted {
		t.Fatalf("beta must still start: calls=%v res=%+v", env.cli.calls, res)
	}
}

func TestApply_PendingRetriesAfterPullFailure(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)
	projectV1 := shopProject(baseCompose)
	projectV2 := shopProject(baseCompose + "    environment:\n      MARKER: \"v2\"\n")

	_, err := env.applyAll(t, projectV1)
	if err != nil {
		t.Fatalf("baseline Apply: %v", err)
	}

	if env.hasApplyPending(t) {
		t.Fatal("successful up must clear .apply_pending")
	}

	env.lister.containers = []docker.Container{proxyUp(time.Minute), appRunning(projShop)}
	env.cli.calls = nil
	env.cli.errs["pull-app:shop"] = errTestPull

	_, err = env.applyAll(t, projectV2)
	if !errors.Is(err, errTestPull) {
		t.Fatalf("err = %v, want pull failure", err)
	}

	if hasCall(env.cli.calls, "up-app:shop") {
		t.Fatalf("up must not run after failed pull: %v", env.cli.calls)
	}

	if !env.hasApplyPending(t) {
		t.Fatal("failed apply must leave .apply_pending")
	}

	env.cli.errs = map[string]error{}
	env.cli.calls = nil

	_, err = env.applyAll(t, projectV2)
	if err != nil {
		t.Fatalf("retry Apply: %v", err)
	}

	wantRetry := []string{"pull-app:shop", "up-app:shop"}
	if !slices.Equal(env.cli.calls, wantRetry) {
		t.Fatalf("pending retry calls = %v, want %v", env.cli.calls, wantRetry)
	}

	if env.hasApplyPending(t) {
		t.Fatal("successful retry must clear .apply_pending")
	}

	env.cli.calls = nil

	_, err = env.applyAll(t, projectV2)
	if err != nil {
		t.Fatalf("stable Apply: %v", err)
	}

	if len(env.cli.calls) != 0 {
		t.Fatalf("healthy cycle must not restart: %v", env.cli.calls)
	}
}

func TestApply_PendingRetriesAfterLoginFailure(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)
	projectV1 := shopProject(baseCompose)
	projectV2 := shopProject(baseCompose + "    environment:\n      MARKER: \"v2\"\n")
	creds := map[string]string{keyRegHost: "https://ghcr.io", keyRegUser: "bot", keyRegToken: "s3cret"}

	_, err := env.apply(t, []docker.Project{projectV1}, nil, nil, true)
	if err != nil {
		t.Fatalf("baseline Apply: %v", err)
	}

	env.lister.containers = []docker.Container{proxyUp(time.Minute), appRunning(projShop)}
	env.cli.calls = nil
	env.cli.errs["login:https://ghcr.io"] = errTestLogin

	_, err = env.apply(t, []docker.Project{projectV2}, nil, creds, true)
	if !errors.Is(err, errTestLogin) || !errors.Is(err, docker.ErrLoginFailed) {
		t.Fatalf("err = %v, want ErrLoginFailed wrapping login failure", err)
	}

	if hasCall(env.cli.calls, "pull-app:shop") || hasCall(env.cli.calls, "up-app:shop") {
		t.Fatalf("pull/up must not run after login failure: %v", env.cli.calls)
	}

	if !env.hasApplyPending(t) {
		t.Fatal("failed login after stack change must leave .apply_pending")
	}

	env.cli.errs = map[string]error{}
	env.cli.calls = nil

	_, err = env.apply(t, []docker.Project{projectV2}, nil, nil, true)
	if err != nil {
		t.Fatalf("retry Apply: %v", err)
	}

	if !hasCall(env.cli.calls, "pull-app:shop") || !hasCall(env.cli.calls, "up-app:shop") {
		t.Fatalf("pending must force pull/up: %v", env.cli.calls)
	}

	if env.hasApplyPending(t) {
		t.Fatal("successful retry must clear .apply_pending")
	}
}

func (e *dockerEnv) hasApplyPending(t *testing.T) bool {
	t.Helper()

	_, err := os.Stat(filepath.Join(e.infra, "apps", projShop, ".apply_pending"))
	if err == nil {
		return true
	}

	if os.IsNotExist(err) {
		return false
	}

	t.Fatalf("stat .apply_pending: %v", err)

	return false
}

func TestApply_RegistryLogin(t *testing.T) {
	t.Parallel()

	full := map[string]string{keyRegHost: "https://ghcr.io", keyRegUser: "bot", keyRegToken: "s3cret"}

	tests := []struct {
		name        string
		settings    map[string]string
		loginErr    error
		wantLogin   bool
		wantErr     bool
		wantEvent   bool
		wantStarted bool
	}{
		{name: "full_credentials_log_in", settings: full, wantLogin: true, wantStarted: true},
		{name: "no_credentials_skip", settings: nil, wantStarted: true},
		{
			name:        "incomplete_credentials_skip",
			settings:    map[string]string{keyRegHost: "https://ghcr.io", keyRegUser: "bot"},
			wantStarted: true,
		},
		{
			name:     "whitespace_in_host_rejected",
			settings: map[string]string{keyRegHost: "ghcr.io evil", keyRegUser: "bot", keyRegToken: "x"},
			wantErr:  true,
		},
		{
			name:     "whitespace_in_user_rejected",
			settings: map[string]string{keyRegHost: "ghcr.io", keyRegUser: "b ot", keyRegToken: "x"},
			wantErr:  true,
		},
		{name: "login_failure_event_and_error", settings: full, loginErr: errTestLogin, wantLogin: true, wantErr: true, wantEvent: true},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newDockerEnv(t)
			env.cli.errs["login:https://ghcr.io"] = tcase.loginErr

			_, err := env.apply(t, []docker.Project{shopProject(baseCompose)}, nil, tcase.settings, true)
			if (err != nil) != tcase.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tcase.wantErr)
			}

			loggedIn := hasCall(env.cli.calls, "login:https://ghcr.io")
			if loggedIn != tcase.wantLogin {
				t.Fatalf("login attempted = %v, want %v (calls %v)", loggedIn, tcase.wantLogin, env.cli.calls)
			}

			started := hasCall(env.cli.calls, "up-app:shop")
			if started != tcase.wantStarted {
				t.Fatalf("app started = %v, want %v (calls %v)", started, tcase.wantStarted, env.cli.calls)
			}

			if tcase.wantEvent {
				if !errors.Is(err, docker.ErrLoginFailed) {
					t.Fatalf("err = %v, want ErrLoginFailed", err)
				}

				if len(env.events.got) != 1 || env.events.got[0].kind != "docker_login_failed" ||
					env.events.got[0].fields["registry"] != "https://ghcr.io" {
					t.Fatalf("events = %+v", env.events.got)
				}
			} else if len(env.events.got) != 0 {
				t.Fatalf("unexpected events: %+v", env.events.got)
			}
		})
	}
}

func TestApply_NoLoginWhenNothingToStart(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)
	project := shopProject(baseCompose)
	creds := map[string]string{keyRegHost: "https://ghcr.io", keyRegUser: "bot", keyRegToken: "s3cret"}

	_, err := env.apply(t, []docker.Project{project}, nil, creds, true)
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}

	env.cli.calls = nil
	env.lister.containers = []docker.Container{proxyUp(time.Minute), appRunning(projShop)}

	_, err = env.apply(t, []docker.Project{project}, nil, creds, true)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	if len(env.cli.calls) != 0 {
		t.Fatalf("healthy cycle must not login or pull: %v", env.cli.calls)
	}
}

func TestApply_RejectsEmptyCompose(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)

	_, err := env.applyAll(t, shopProject("   \n"))
	if err == nil || !strings.Contains(err.Error(), "missing docker-compose.yml") {
		t.Fatalf("err = %v, want refusal of partial apply", err)
	}

	if len(env.cli.calls) != 0 {
		t.Fatalf("no docker calls expected: %v", env.cli.calls)
	}
}

func TestPruneImages(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)

	err := env.svc.PruneImages(context.Background())
	if err != nil || !hasCall(env.cli.calls, "prune") {
		t.Fatalf("PruneImages = %v calls=%v", err, env.cli.calls)
	}

	env.cli.errs["prune"] = fmt.Errorf("wrapped: %w", errTestDown)

	err = env.svc.PruneImages(context.Background())
	if !errors.Is(err, errTestDown) {
		t.Fatalf("err = %v, want underlying error", err)
	}
}

func TestValidateProjects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		project   docker.Project
		hostEnv   map[string]string
		wantErr   string // substring; empty = valid
		wantNoErr bool
	}{
		{name: "valid", project: shopProject(baseCompose), wantNoErr: true},
		{name: "empty_compose", project: shopProject(""), wantErr: "docker-compose.yml is empty"},
		{name: "invalid_yaml", project: shopProject("services: [oops"), wantErr: "compose config invalid"},
		{
			name:    "unknown_top_level_key",
			project: shopProject("services:\n  web:\n    image: nginx\nbogus: true\n"),
			wantErr: "compose config invalid",
		},
		{
			name: "entry_point_not_in_compose",
			project: func() docker.Project {
				p := shopProject(baseCompose)
				p.EntryPoint = "api"

				return p
			}(),
			wantErr: "proxy.target.service-name not found",
		},
		{
			name: "entry_point_by_container_name",
			project: func() docker.Project {
				p := shopProject("services:\n  web:\n    image: nginx\n    container_name: shop-web\n")
				p.EntryPoint = "shop-web"

				return p
			}(),
			wantNoErr: true,
		},
		{
			name: "interpolation_from_sidecar",
			project: func() docker.Project {
				p := shopProject("services:\n  web:\n    image: nginx:${TAG}\n")
				p.SidecarEnvRaw = "tag=1.30\n"

				return p
			}(),
			wantNoErr: true,
		},
		{
			name:      "interpolation_from_host_prefixed",
			project:   shopProject("services:\n  web:\n    image: nginx:${TAG}\n"),
			hostEnv:   map[string]string{"SHOP_TAG": "1.30"},
			wantNoErr: true,
		},
		{
			name: "host_overrides_invalid_sidecar_value",
			project: func() docker.Project {
				p := shopProject("services:\n  web:\n    image: nginx\n    deploy:\n      replicas: ${REPLICAS}\n")
				p.SidecarEnvRaw = "REPLICAS=abc\n"

				return p
			}(),
			hostEnv:   map[string]string{"SHOP_REPLICAS": "2"},
			wantNoErr: true,
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newDockerEnv(t)

			err := env.svc.ValidateProjects(context.Background(), []docker.Project{tcase.project}, tcase.hostEnv)
			if tcase.wantNoErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tcase.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tcase.wantErr)
			}
		})
	}
}

func TestValidateProjects_AggregatesAllFailures(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)

	err := env.svc.ValidateProjects(context.Background(), []docker.Project{
		shopProject(""),
		namedProject("ok"),
		{Name: "bad", ComposeYAML: "services: [oops"},
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}

	assertContainsAll(t, err.Error(), "project=shop", "project=bad")

	if strings.Contains(err.Error(), "project=ok") {
		t.Fatalf("valid project must not be reported: %v", err)
	}
}

func TestValidateProjects_RejectsHostBindMounts(t *testing.T) {
	t.Parallel()

	const trustedDigest = "amir20/dozzle@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	tests := []struct {
		name       string
		compose    string
		allowlist  []string
		wantErr    string
		wantKind   string
		wantReason string
	}{
		{
			name:       "host_root_bind",
			compose:    "services:\n  web:\n    image: nginx\n    volumes:\n      - /:/host\n",
			wantErr:    "volume policy violation",
			wantKind:   "security_violation",
			wantReason: "disallowed_bind_mount",
		},
		{
			name:       "untrusted_docker_sock",
			compose:    "services:\n  web:\n    image: amir20/dozzle:latest\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n",
			wantErr:    "volume policy violation",
			wantKind:   "security_violation",
			wantReason: "untrusted_docker_socket_mount",
		},
		{
			name:       "tmpfs_forbidden",
			compose:    "services:\n  web:\n    image: nginx\n    volumes:\n      - type: tmpfs\n        target: /tmp\n",
			wantErr:    "volume policy violation",
			wantKind:   "security_violation",
			wantReason: "disallowed_volume_type",
		},
		{
			name:      "trusted_docker_sock",
			compose:   "services:\n  web:\n    image: " + trustedDigest + "\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n",
			allowlist: []string{trustedDigest},
		},
		{
			name:    "named_volume_ok",
			compose: "services:\n  web:\n    image: nginx\n    volumes:\n      - data:/data\nvolumes:\n  data:\n",
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			trustedPath := ""
			if len(tcase.allowlist) > 0 {
				trustedPath = writeTrustedImages(t, tcase.allowlist...)
			}

			env := newDockerEnvWithTrusted(t, trustedPath)
			project := shopProject(tcase.compose)
			project.EntryPoint = ""

			err := env.svc.ValidateProjects(context.Background(), []docker.Project{project}, nil)
			if tcase.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tcase.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tcase.wantErr)
			}

			if len(env.events.got) != 1 || env.events.got[0].kind != tcase.wantKind {
				t.Fatalf("events = %+v, want kind %q", env.events.got, tcase.wantKind)
			}

			if env.events.got[0].fields["reason"] != tcase.wantReason {
				t.Fatalf("reason = %q, want %q", env.events.got[0].fields["reason"], tcase.wantReason)
			}
		})
	}
}

func TestApply_TrustedImageDeployedEvent(t *testing.T) {
	t.Parallel()

	const trustedDigest = "amir20/dozzle@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	trustedPath := writeTrustedImages(t, trustedDigest)
	env := newDockerEnvWithTrusted(t, trustedPath)

	project := docker.Project{
		Name:        "dozzle",
		ComposeYAML: "services:\n  dozzle:\n    image: " + trustedDigest + "\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n",
	}

	_, err := env.applyAll(t, project)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	if !hasCall(env.cli.calls, "up-app:dozzle") {
		t.Fatalf("expected up-app:dozzle in %v", env.cli.calls)
	}

	var found bool

	for _, event := range env.events.got {
		if event.kind != "trusted_image_deployed" {
			continue
		}

		found = true

		if event.fields["project"] != "dozzle" ||
			event.fields["service"] != "dozzle" ||
			event.fields["image"] != trustedDigest ||
			event.fields["digest"] != "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
			t.Fatalf("unexpected event fields: %+v", event.fields)
		}
	}

	if !found {
		t.Fatalf("missing trusted_image_deployed in %+v", env.events.got)
	}
}

func TestApply_RejectsBindMountBeforeUp(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)
	project := shopProject("services:\n  web:\n    image: nginx\n    volumes:\n      - /root:/root\n")
	project.EntryPoint = ""

	_, err := env.apply(t, []docker.Project{project}, nil, nil, true)
	if err == nil || !strings.Contains(err.Error(), "volume policy violation") {
		t.Fatalf("err = %v, want volume policy violation", err)
	}

	if hasCall(env.cli.calls, "up-app:"+projShop) || hasCall(env.cli.calls, "pull-app:"+projShop) {
		t.Fatalf("compose up must not run on security violation: %v", env.cli.calls)
	}

	if len(env.events.got) == 0 || env.events.got[0].kind != "security_violation" {
		t.Fatalf("events = %+v, want security_violation", env.events.got)
	}
}

func TestApply_RejectsTmpfsBeforeUp(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)
	project := shopProject("services:\n  web:\n    image: nginx\n    volumes:\n      - type: tmpfs\n        target: /tmp\n")
	project.EntryPoint = ""

	_, err := env.apply(t, []docker.Project{project}, nil, nil, true)
	if err == nil || !strings.Contains(err.Error(), "volume policy violation") {
		t.Fatalf("err = %v, want volume policy violation", err)
	}

	if hasCall(env.cli.calls, "up-app:"+projShop) || hasCall(env.cli.calls, "pull-app:"+projShop) {
		t.Fatalf("compose up must not run on tmpfs violation: %v", env.cli.calls)
	}

	if len(env.events.got) != 1 ||
		env.events.got[0].kind != "security_violation" ||
		env.events.got[0].fields["reason"] != "disallowed_volume_type" {
		t.Fatalf("events = %+v, want security_violation/disallowed_volume_type", env.events.got)
	}
}

func TestValidateProjects_RejectsTmpfs(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)
	project := shopProject("services:\n  web:\n    image: nginx\n    volumes:\n      - type: tmpfs\n        target: /tmp\n")
	project.EntryPoint = ""

	err := env.svc.ValidateProjects(context.Background(), []docker.Project{project}, nil)
	if err == nil || !strings.Contains(err.Error(), "volume policy violation") {
		t.Fatalf("err = %v, want volume policy violation", err)
	}

	if len(env.events.got) != 1 ||
		env.events.got[0].kind != "security_violation" ||
		env.events.got[0].fields["reason"] != "disallowed_volume_type" {
		t.Fatalf("events = %+v, want security_violation/disallowed_volume_type", env.events.got)
	}
}

func assertContainsAll(t *testing.T, haystack string, needles ...string) {
	t.Helper()

	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			t.Fatalf("expected %q in:\n%s", needle, haystack)
		}
	}
}
