package sys_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/sys"
)

const (
	testExecTimeout = 10 * time.Second
	shellBin        = "sh"
	printStderrCmd  = `printf '%s' "$1" >&2`
)

func newTestExecutor(t *testing.T) *sys.CommandExecutor {
	t.Helper()

	return &sys.CommandExecutor{WorkDir: t.TempDir(), UseExecLogs: false}
}

func testContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), testExecTimeout)
	t.Cleanup(cancel)

	return ctx
}

func TestCommandExecutor_Exec_RedactsURLCredentialsInError(t *testing.T) {
	t.Parallel()

	const secret = "s3cr3t"

	tests := []struct {
		name     string
		args     []string
		wantInfo string
	}{
		{
			name:     "token_only",
			args:     []string{"false", "https://" + secret + "@github.com/o/r.git"},
			wantInfo: "https://***@github.com/o/r.git",
		},
		{
			name:     "user_and_password",
			args:     []string{"false", "https://bob:" + secret + "@github.com/o/r.git"},
			wantInfo: "https://***@github.com/o/r.git",
		},
		{
			name:     "at_sign_inside_password_uses_last_at",
			args:     []string{"false", "https://bob:" + secret + "@x@github.com/o/r.git"},
			wantInfo: "https://***@github.com/o/r.git",
		},
		{
			name: "several_urls_in_one_command",
			args: []string{
				"false", "https://a:" + secret + "@one.test/x",
				"ssh://b:" + secret + "@two.test/y",
			},
			wantInfo: "https://***@one.test/x ssh://***@two.test/y",
		},
		{
			name:     "url_without_userinfo_untouched",
			args:     []string{"false", "https://github.com/o/r.git"},
			wantInfo: "https://github.com/o/r.git",
		},
		{
			name:     "at_sign_in_path_is_not_credentials",
			args:     []string{"false", "https://github.com/o/r@v1"},
			wantInfo: "https://github.com/o/r@v1",
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := newTestExecutor(t).Exec(testContext(t), tcase.args...)
			if err == nil {
				t.Fatal("want error from `false`")
			}

			if strings.Contains(err.Error(), secret) {
				t.Fatalf("secret leaked into error: %v", err)
			}

			if !strings.Contains(err.Error(), tcase.wantInfo) {
				t.Fatalf("error %q does not contain %q", err.Error(), tcase.wantInfo)
			}
		})
	}
}

func TestCommandExecutor_Exec_StripsLogrusWarningsFromStderr(t *testing.T) {
	t.Parallel()

	const warning = `time="2024-01-01T00:00:00Z" level=warning msg="the attribute version is obsolete"`

	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{name: "empty", stderr: "", want: ""},
		{name: "plain_error_preserved", stderr: "boom", want: "boom"},
		{name: "only_warning_dropped", stderr: warning, want: ""},
		{name: "warning_without_timestamp_dropped", stderr: `level=warning msg="x"`, want: ""},
		{name: "warning_case_insensitive", stderr: `LEVEL=WARNING msg="x"`, want: ""},
		{name: "warning_then_real_error", stderr: warning + "\nError: invalid compose project", want: "Error: invalid compose project"},
		{name: "real_error_then_warning", stderr: "Error: invalid compose project\n" + warning, want: "Error: invalid compose project"},
		{name: "unstructured_warning_line_dropped", stderr: "noise level=warning tail\nkeep", want: "keep"},
		{name: "blank_lines_removed", stderr: "a\n\n\nb", want: "a\nb"},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			stdout, stderr, err := newTestExecutor(t).Exec(testContext(t), shellBin, "-c", printStderrCmd, shellBin, tcase.stderr)
			if err != nil {
				t.Fatalf("Exec: %v", err)
			}

			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}

			if stderr != tcase.want {
				t.Fatalf("stderr = %q, want %q", stderr, tcase.want)
			}
		})
	}
}

func TestCommandExecutor_Exec_FailureKeepsOutputAndExitError(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := newTestExecutor(t).Exec(testContext(t), shellBin, "-c", "echo out; echo err >&2; exit 3")
	if err == nil {
		t.Fatal("want error on non-zero exit")
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("want *exec.ExitError with code 3 in chain, got %v", err)
	}

	if stdout != "out" || stderr != "err" {
		t.Fatalf("stdout=%q stderr=%q, want out/err", stdout, stderr)
	}
}

func TestCommandExecutor_Exec_RunsInWorkDir(t *testing.T) {
	t.Parallel()

	executor := newTestExecutor(t)

	stdout, _, err := executor.Exec(testContext(t), "pwd")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	gotDir, err := filepath.EvalSymlinks(stdout)
	if err != nil {
		t.Fatalf("EvalSymlinks(stdout): %v", err)
	}

	wantDir, err := filepath.EvalSymlinks(executor.WorkDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(workdir): %v", err)
	}

	if gotDir != wantDir {
		t.Fatalf("pwd = %q, want %q", gotDir, wantDir)
	}
}

func TestCommandExecutor_ExecWithStdin_PassesInput(t *testing.T) {
	t.Parallel()

	stdout, _, err := newTestExecutor(t).ExecWithStdin(testContext(t), "token-value\n", "cat")
	if err != nil {
		t.Fatalf("ExecWithStdin: %v", err)
	}

	if stdout != "token-value" {
		t.Fatalf("stdout = %q, want token-value", stdout)
	}
}

func TestCommandExecutor_Exec_TimeoutKillsProcess(t *testing.T) {
	t.Parallel()

	const (
		timeout = 150 * time.Millisecond
		maxWait = 2 * time.Second
	)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)

	start := time.Now()

	_, _, err := newTestExecutor(t).Exec(ctx, "sleep", "30")
	if err == nil {
		t.Fatal("want timeout error")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded in chain, got %v", err)
	}

	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("want timeout marker in error, got %v", err)
	}

	if elapsed := time.Since(start); elapsed > maxWait {
		t.Fatalf("process was not killed promptly: %s", elapsed)
	}
}

func TestCommandExecutor_Exec_RejectsInvalidInput(t *testing.T) {
	t.Parallel()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name      string
		ctx       context.Context //nolint:containedctx // table input for rejection cases
		args      []string
		wantIsErr error
	}{
		{name: "no_args", ctx: context.Background(), args: nil, wantIsErr: nil},
		{name: "blank_command", ctx: context.Background(), args: []string{"  "}, wantIsErr: nil},
		{name: "already_canceled_context", ctx: canceled, args: []string{"true"}, wantIsErr: context.Canceled},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := newTestExecutor(t).Exec(tcase.ctx, tcase.args...)
			if err == nil {
				t.Fatal("want error")
			}

			if tcase.wantIsErr != nil && !errors.Is(err, tcase.wantIsErr) {
				t.Fatalf("want %v in chain, got %v", tcase.wantIsErr, err)
			}
		})
	}
}
