package sys

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// docker/compose logrus warnings, e.g.:
// time="..." level=warning msg="... version is obsolete ...".
var logrusWarningRE = regexp.MustCompile(`(?i)(?:time="[^"]*"\s+)?level=warning\s+msg="[^"]*"\s*`)

var (
	errEmptyCommand = errors.New("empty command")
	errTimeout      = errors.New("timeout")
	errNilContext   = errors.New("nil context")
)

// waitDelayAfterCancel bounds how long we wait for pipes after killing a timed-out child.
const waitDelayAfterCancel = 3 * time.Second

// CommandExecutor runs host commands in a given working directory.
// Callers pass an explicit argv (no shell, no space-splitting).
type CommandExecutor struct {
	WorkDir     string
	UseExecLogs bool
}

// Exec runs argv under ctx and returns stdout, stderr, and error.
// Callers must pass a bounded context (typically context.WithTimeout).
func (e *CommandExecutor) Exec(ctx context.Context, args ...string) (string, string, error) {
	return e.execContext(ctx, "", args...)
}

// ExecWithStdin runs argv under ctx and feeds stdin (e.g. docker login --password-stdin).
func (e *CommandExecutor) ExecWithStdin(ctx context.Context, stdin string, args ...string) (string, string, error) {
	return e.execContext(ctx, stdin, args...)
}

func (e *CommandExecutor) execContext(ctx context.Context, stdin string, args ...string) (string, string, error) {
	if ctx == nil {
		return "", "", errNilContext
	}

	err := ctx.Err()
	if err != nil {
		return "", "", fmt.Errorf("exec context: %w", err)
	}

	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", "", errEmptyCommand
	}

	safeCmd := redactSecrets(strings.Join(args, " "))
	if e.UseExecLogs {
		log.Printf("[exec] workdir=%s cmd=%s", e.WorkDir, safeCmd)
	}

	// #nosec G204 -- argv is built by apinfra processors as discrete arguments
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = e.WorkDir
	cmd.WaitDelay = waitDelayAfterCancel

	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	var outBuf, errBuf bytes.Buffer

	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	stdout := strings.TrimSpace(outBuf.String())
	stderr := stripLogrusWarnings(errBuf.String())

	e.logExecOutput(stdout, stderr)

	if runErr != nil {
		e.logExecFailure(safeCmd, runErr, stderr)

		return stdout, stderr, formatExecError(safeCmd, runErr, ctx.Err())
	}

	return stdout, stderr, nil
}

// formatExecError keeps runErr in the chain; on deadline also joins errTimeout and
// context.DeadlineExceeded so callers can errors.Is either.
func formatExecError(safeCmd string, runErr, ctxErr error) error {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", safeCmd, errors.Join(errTimeout, context.DeadlineExceeded, runErr))
	}

	return fmt.Errorf("%s: %w", safeCmd, runErr)
}

func (e *CommandExecutor) logExecOutput(stdout, stderr string) {
	if !e.UseExecLogs {
		return
	}

	safeOut, safeErr := oneLine(redactSecrets(stdout)), oneLine(redactSecrets(stderr))
	if safeOut != "" {
		log.Printf("[exec stdout] %s", safeOut)
	}

	if safeErr != "" {
		log.Printf("[exec stderr] %s", safeErr)
	}
}

func (e *CommandExecutor) logExecFailure(safeCmd string, runErr error, stderr string) {
	safeErr := oneLine(redactSecrets(stderr))
	if safeErr != "" {
		log.Printf("[exec] FAIL workdir=%s cmd=%s err=%v stderr=%s", e.WorkDir, safeCmd, runErr, safeErr)
	} else {
		log.Printf("[exec] FAIL workdir=%s cmd=%s err=%v", e.WorkDir, safeCmd, runErr)
	}
}

// stripLogrusWarnings removes docker/compose logrus warning entries from CLI output,
// keeping real errors (e.g. invalid compose project).
func stripLogrusWarnings(input string) string {
	input = logrusWarningRE.ReplaceAllString(input, "")
	lines := strings.Split(input, "\n")

	var keep []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.Contains(strings.ToLower(line), "level=warning") {
			continue
		}

		keep = append(keep, line)
	}

	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// oneLine collapses whitespace/newlines so journald does not split a message across lines.
func oneLine(input string) string {
	fields := strings.Fields(input)

	return strings.Join(fields, " ")
}

// redactSecrets hides credentials embedded in URLs anywhere in s
// (e.g. https://token@host/... or https://user:pass@host/...).
func redactSecrets(input string) string {
	const marker = "://"

	var builder strings.Builder

	remaining := input

	for {
		idx := strings.Index(remaining, marker)
		if idx < 0 {
			builder.WriteString(remaining)

			break
		}

		builder.WriteString(remaining[:idx+len(marker)])
		rest := remaining[idx+len(marker):]
		endAuth := len(rest)

		for _, sep := range []byte{'/', ' ', '\n', '\t', '\'', '"'} {
			if j := strings.IndexByte(rest, sep); j >= 0 && j < endAuth {
				endAuth = j
			}
		}

		// userinfo@host — take the last '@' inside the authority.
		lastAt := -1

		for j := range endAuth {
			if rest[j] == '@' {
				lastAt = j
			}
		}

		if lastAt >= 0 {
			builder.WriteString("***@")

			remaining = rest[lastAt+1:]

			continue
		}

		remaining = rest
	}

	return builder.String()
}
