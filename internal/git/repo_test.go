package git_test

import (
	"context"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504 targets Go < 1.6.3 (httpoxy); test-only git-http-backend wrapper
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/uservseatb/apinfra/internal/git"
)

// gitRun executes the real git binary in dir; the suite skips when git is absent.
func gitRun(t *testing.T, dir string, args ...string) {
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

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()

	path := filepath.Join(dir, filepath.FromSlash(name))

	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	err = os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
}

// newOrigin creates a local repository served through file:// with two commits on
// main (the second one rewrites settings.yml) and a "release" branch.
func newOrigin(t *testing.T) string {
	t.Helper()

	_, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not available")
	}

	dir := t.TempDir()

	gitRun(t, dir, "init", "-b", "main")
	writeRepoFile(t, dir, "dev/settings.yml", "version: old\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "first")

	writeRepoFile(t, dir, "dev/settings.yml", "version: \"1\"\n")
	writeRepoFile(t, dir, "dev/shop/docker-compose.yml", "services: {}\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "second")

	gitRun(t, dir, "checkout", "-b", "release")
	writeRepoFile(t, dir, "dev/settings.yml", "version: release\n")
	gitRun(t, dir, "commit", "-am", "release")
	gitRun(t, dir, "checkout", "main")

	return "file://" + dir
}

func TestLoad_ReadsLatestCommitOfBranch(t *testing.T) {
	t.Parallel()

	url := newOrigin(t)

	tests := []struct {
		name   string
		branch string
		want   string
	}{
		{name: "empty_branch_defaults_to_main", branch: "", want: "version: \"1\"\n"},
		{name: "explicit_main", branch: "main", want: "version: \"1\"\n"},
		{name: "other_branch", branch: "release", want: "version: release\n"},
		{name: "branch_is_trimmed", branch: " main ", want: "version: \"1\"\n"},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			repo, err := git.Load(context.Background(), git.LoadOptions{URL: url, Branch: tcase.branch})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			data, err := repo.ReadFile("dev/settings.yml")
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}

			if string(data) != tcase.want {
				t.Fatalf("settings.yml = %q, want %q", data, tcase.want)
			}
		})
	}
}

func TestRepo_ReadFileAndExists(t *testing.T) {
	t.Parallel()

	repo, err := git.Load(context.Background(), git.LoadOptions{URL: newOrigin(t), Branch: "main"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tests := []struct {
		name       string
		path       string
		wantExists bool
		wantRead   bool
	}{
		{name: "file", path: "dev/settings.yml", wantExists: true, wantRead: true},
		{name: "nested_file", path: "dev/shop/docker-compose.yml", wantExists: true, wantRead: true},
		{name: "directory", path: "dev", wantExists: true},
		{name: "directory_with_trailing_slash", path: "dev/", wantExists: true},
		{name: "missing", path: "dev/nope.yml"},
		{name: "root_dot", path: ".", wantExists: true},
		{name: "backslash_separators", path: `dev\settings.yml`, wantExists: true, wantRead: true},
		{name: "leading_slash_is_relative", path: "/dev/settings.yml", wantExists: true, wantRead: true},
		{name: "traversal_stays_inside_repo", path: "../../etc/passwd"},
		{name: "traversal_then_valid", path: "dev/../dev/settings.yml", wantExists: true, wantRead: true},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			if got := repo.Exists(tcase.path); got != tcase.wantExists {
				t.Fatalf("Exists(%q) = %v, want %v", tcase.path, got, tcase.wantExists)
			}

			_, err := repo.ReadFile(tcase.path)
			if (err == nil) != tcase.wantRead {
				t.Fatalf("ReadFile(%q) err = %v, want success=%v", tcase.path, err, tcase.wantRead)
			}
		})
	}
}

func TestLoad_Errors(t *testing.T) {
	t.Parallel()

	url := newOrigin(t)

	tests := []struct {
		name    string
		cancel  bool
		opt     git.LoadOptions
		wantErr string
	}{
		{name: "empty_url", opt: git.LoadOptions{URL: "  "}, wantErr: "empty repo URL"},
		{name: "missing_branch", opt: git.LoadOptions{URL: url, Branch: "nope"}, wantErr: "branch not found"},
		{name: "canceled_context", cancel: true, opt: git.LoadOptions{URL: url}, wantErr: "context canceled"},
		{
			name:    "unreachable_repo",
			opt:     git.LoadOptions{URL: "file://" + filepath.Join(t.TempDir(), "absent")},
			wantErr: "clone",
		},
		{
			name: "token_is_ignored_for_file_urls",
			opt:  git.LoadOptions{URL: url, Token: "secret"},
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			if tcase.cancel {
				cancel()
			}

			repo, err := git.Load(ctx, tcase.opt)

			if tcase.wantErr == "" {
				if err != nil || repo == nil {
					t.Fatalf("Load: repo=%v err=%v", repo, err)
				}

				return
			}

			if err == nil || !strings.Contains(err.Error(), tcase.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tcase.wantErr)
			}
		})
	}
}

const (
	testToken       = "ghp_test_token" //nolint:gosec // fake fixture, not a credential
	tokenUser       = "x-access-token"
	authFailedMsg   = "git authentication failed"
	loginFailedKind = "git_login_failed"
)

type recordedEvent struct {
	kind   string
	fields map[string]string
}

type fakeRecorder struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (r *fakeRecorder) Write(kind string, fields map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, recordedEvent{kind: kind, fields: fields})

	return nil
}

func (r *fakeRecorder) snapshot() []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]recordedEvent(nil), r.events...)
}

// newSmartHTTPOrigin serves a real git repository over smart HTTP through
// git-http-backend; every request must carry the expected token as basic-auth password.
func newSmartHTTPOrigin(t *testing.T, token string) string {
	t.Helper()

	originURL := newOrigin(t)
	originDir := strings.TrimPrefix(originURL, "file://")

	out, err := exec.CommandContext(t.Context(), "git", "--exec-path").Output()
	if err != nil {
		t.Skipf("git --exec-path: %v", err)
	}

	backend := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")

	_, err = os.Stat(backend)
	if err != nil {
		t.Skip("git-http-backend not available")
	}

	cgiHandler := &cgi.Handler{
		Path: backend,
		Root: "/",
		Dir:  "",
		Env: []string{
			"GIT_PROJECT_ROOT=" + filepath.Dir(originDir),
			"GIT_HTTP_EXPORT_ALL=1",
		},
		InheritEnv: []string{"PATH", "HOME"},
		Logger:     nil,
		Args:       nil,
		Stderr:     nil,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		user, pass, ok := req.BasicAuth()
		if !ok || user != tokenUser || pass != token {
			resp.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			resp.WriteHeader(http.StatusUnauthorized)

			return
		}

		cgiHandler.ServeHTTP(resp, req)
	}))
	t.Cleanup(srv.Close)

	return srv.URL + "/" + filepath.Base(originDir)
}

func TestLoad_OverHTTP_TokenAuthentication(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		token     string
		branch    string
		wantErr   string
		wantEvent bool
	}{
		{name: "valid_token_clones", token: testToken, branch: "main"},
		{name: "token_is_trimmed", token: "  " + testToken + "\n", branch: "release"},
		{name: "wrong_token_records_event", token: "nope", branch: "main", wantErr: authFailedMsg, wantEvent: true},
		{name: "missing_token_records_event", token: "", branch: "main", wantErr: authFailedMsg, wantEvent: true},
		{name: "missing_branch_with_valid_token", token: testToken, branch: "nope", wantErr: "branch not found"},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			repoURL := newSmartHTTPOrigin(t, testToken)
			rec := &fakeRecorder{}

			repo, err := git.Load(t.Context(), git.LoadOptions{
				URL: repoURL, Token: tcase.token, Branch: tcase.branch, Events: rec,
			})

			if tcase.wantErr == "" {
				if err != nil {
					t.Fatalf("Load: %v", err)
				}

				if !repo.Exists("dev/settings.yml") {
					t.Fatal("cloned worktree misses dev/settings.yml")
				}
			} else if err == nil || !strings.Contains(err.Error(), tcase.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tcase.wantErr)
			}

			got := rec.snapshot()

			switch {
			case tcase.wantEvent && len(got) != 1:
				t.Fatalf("want exactly one %s event, got %v", loginFailedKind, got)
			case tcase.wantEvent && (got[0].kind != loginFailedKind || got[0].fields["repo"] != repoURL):
				t.Fatalf("event = %+v, want kind=%s repo=%s", got[0], loginFailedKind, repoURL)
			case !tcase.wantEvent && len(got) != 0:
				t.Fatalf("unexpected events: %v", got)
			}
		})
	}
}

func TestLoad_OverHTTP_ServerStatusClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		wantAuth  bool
		wantEvent bool
	}{
		{name: "401_is_auth_failure", status: http.StatusUnauthorized, wantAuth: true, wantEvent: true},
		{name: "403_is_auth_failure", status: http.StatusForbidden, wantAuth: true, wantEvent: true},
		{name: "404_is_plain_clone_error", status: http.StatusNotFound, wantAuth: false, wantEvent: false},
		{name: "500_is_plain_clone_error", status: http.StatusInternalServerError, wantAuth: false, wantEvent: false},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tcase.status)
			}))
			t.Cleanup(srv.Close)

			rec := &fakeRecorder{}

			_, err := git.Load(t.Context(), git.LoadOptions{URL: srv.URL + "/o/r", Token: testToken, Events: rec})
			if err == nil {
				t.Fatal("want error")
			}

			if gotAuth := strings.Contains(err.Error(), authFailedMsg); gotAuth != tcase.wantAuth {
				t.Fatalf("auth error = %v, want %v: %v", gotAuth, tcase.wantAuth, err)
			}

			if gotEvent := len(rec.snapshot()) == 1; gotEvent != tcase.wantEvent {
				t.Fatalf("event recorded = %v, want %v", gotEvent, tcase.wantEvent)
			}
		})
	}
}
