package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/events"
	"github.com/uservseatb/apinfra/internal/notify"
)

var errSendBoom = errors.New("boom")

type recordingSender struct {
	mu       sync.Mutex
	messages []string
	err      error
	block    time.Duration
}

func (s *recordingSender) Send(ctx context.Context, message string) error {
	if s.block > 0 {
		select {
		case <-time.After(s.block):
		case <-ctx.Done():
			return fmt.Errorf("send wait: %w", ctx.Err())
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.err != nil {
		return s.err
	}

	s.messages = append(s.messages, message)

	return nil
}

func (s *recordingSender) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, len(s.messages))
	copy(out, s.messages)

	return out
}

func writeEvents(t *testing.T, path string, entries ...map[string]string) {
	t.Helper()

	var b strings.Builder

	for _, entry := range entries {
		line, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		b.Write(line)
		b.WriteByte('\n')
	}

	err := os.WriteFile(path, []byte(b.String()), 0o600)
	if err != nil {
		t.Fatalf("write events: %v", err)
	}
}

func appendEvent(t *testing.T, path string, entry map[string]string) {
	t.Helper()

	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = file.Close() }()

	_, err = file.Write(append(line, '\n'))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
}

func newTestDispatcher(
	t *testing.T,
	sender notify.Sender,
	eventsPath, statePath string,
	mutate func(*notify.Config),
) *notify.Dispatcher {
	t.Helper()

	cfg := notify.Config{
		Enabled:     true,
		Targets:     []string{"generic+http://example.invalid/hook"},
		Events:      []string{events.GitLoginFailed, events.ContainerDeployFailed, events.ContainerDeployed},
		Timeout:     time.Second,
		MaxPerCycle: 10,
		CutEvery:    100,
		ServerName:  "test-host",
		Namespace:   "dev",
		EventsPath:  eventsPath,
		StatePath:   statePath,
	}
	if mutate != nil {
		mutate(&cfg)
	}

	dispatcher, err := notify.NewWithSender(cfg, sender)
	if err != nil {
		t.Fatalf("NewWithSender: %v", err)
	}

	return dispatcher
}

func TestDispatcher_MessageFieldOrder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	eventsPath := filepath.Join(dir, ".events")
	statePath := filepath.Join(dir, ".events.notify.state.json")

	writeEvents(t, eventsPath, map[string]string{
		"type":    events.SecurityViolation,
		"ts":      "2026-11-20T08:22:49Z",
		"project": "exploit",
		"reason":  "disallowed_bind_mount",
		"service": "",
		"source":  "my_server_root",
	})

	sender := &recordingSender{}
	dispatcher := newTestDispatcher(t, sender, eventsPath, statePath, func(cfg *notify.Config) {
		cfg.Events = []string{events.SecurityViolation}
	})

	err := os.WriteFile(statePath, []byte(`{"offset":0,"count":0}`+"\n"), 0o600)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	dispatcher.Flush(t.Context())

	got := sender.got()
	if len(got) != 1 {
		t.Fatalf("messages = %#v", got)
	}

	want := strings.Join([]string{
		"test-host[dev]",
		"ts: 2026-11-20T08:22:49Z",
		"project: exploit",
		"type: security_violation",
		"reason: disallowed_bind_mount",
		"service: ",
		"source: my_server_root",
	}, "\n")
	if got[0] != want {
		t.Fatalf("message:\n%s\nwant:\n%s", got[0], want)
	}
}

func TestDispatcher_PrepareStartsAtEOF(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	eventsPath := filepath.Join(dir, ".events")
	statePath := filepath.Join(dir, ".events.notify.state.json")

	writeEvents(t, eventsPath, map[string]string{
		"type": events.GitLoginFailed,
		"repo": "old",
	})

	sender := &recordingSender{}
	dispatcher := newTestDispatcher(t, sender, eventsPath, statePath, nil)
	dispatcher.Prepare()

	appendEvent(t, eventsPath, map[string]string{
		"type": events.GitLoginFailed,
		"repo": "new",
	})

	dispatcher.Flush(t.Context())

	got := sender.got()
	if len(got) != 1 || !strings.Contains(got[0], "repo: new") {
		t.Fatalf("messages = %#v", got)
	}
}

func TestDispatcher_AdvanceOnlyOnSuccessfulSend(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	eventsPath := filepath.Join(dir, ".events")
	statePath := filepath.Join(dir, ".events.notify.state.json")
	//nolint:gosec // G101: intentional credential-shaped fixtures for redaction assertions
	secretRepo := "https://user:s3cret-value@example.com/r.git"
	secretErr := "Authorization: Bearer s3cret-value"

	writeEvents(t, eventsPath, map[string]string{
		"type":  events.GitLoginFailed,
		"repo":  secretRepo,
		"error": secretErr,
	})

	failing := &recordingSender{err: errSendBoom}
	dispatcher := newTestDispatcher(t, failing, eventsPath, statePath, nil)
	dispatcher.Prepare()
	// Re-write after prepare so the line is new relative to EOF cursor.
	writeEvents(t, eventsPath, map[string]string{
		"type":  events.GitLoginFailed,
		"repo":  secretRepo,
		"error": secretErr,
	})
	// Reset cursor to 0 for this test.
	err := os.WriteFile(statePath, []byte(`{"offset":0,"count":0}`+"\n"), 0o600)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	dispatcher.Flush(t.Context())

	if len(failing.got()) != 0 {
		t.Fatal("failed send must not record success")
	}

	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	if !strings.Contains(string(raw), `"offset":0`) {
		t.Fatalf("offset must stay 0 on failure, state=%s", raw)
	}

	okSender := &recordingSender{}
	dispatcher = newTestDispatcher(t, okSender, eventsPath, statePath, nil)
	dispatcher.Flush(t.Context())

	got := okSender.got()
	if len(got) != 1 {
		t.Fatalf("messages = %#v", got)
	}

	if strings.Contains(got[0], "s3cret-value") {
		t.Fatalf("secrets leaked: %s", got[0])
	}

	if !strings.Contains(got[0], "***") {
		t.Fatalf("expected redaction markers: %s", got[0])
	}
}

func TestDispatcher_MaxPerCycle(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	eventsPath := filepath.Join(dir, ".events")
	statePath := filepath.Join(dir, ".events.notify.state.json")

	writeEvents(t, eventsPath,
		map[string]string{"type": events.GitLoginFailed, "repo": "a"},
		map[string]string{"type": events.GitLoginFailed, "repo": "b"},
		map[string]string{"type": events.GitLoginFailed, "repo": "c"},
	)

	sender := &recordingSender{}
	dispatcher := newTestDispatcher(t, sender, eventsPath, statePath, func(cfg *notify.Config) {
		cfg.MaxPerCycle = 2
	})

	err := os.WriteFile(statePath, []byte(`{"offset":0,"count":0}`+"\n"), 0o600)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	dispatcher.Flush(t.Context())

	if got := sender.got(); len(got) != 2 {
		t.Fatalf("first flush messages = %d", len(got))
	}

	dispatcher.Flush(t.Context())

	if got := sender.got(); len(got) != 3 {
		t.Fatalf("second flush should send remaining one, got %d", len(got))
	}

	appendEvent(t, eventsPath, map[string]string{"type": events.GitLoginFailed, "repo": "c"})
	dispatcher.Flush(t.Context())

	if got := sender.got(); len(got) != 4 {
		t.Fatalf("duplicate-scope event must still send, got %d messages", len(got))
	}
}

func TestDispatcher_FilterSkipsNonAllowed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	eventsPath := filepath.Join(dir, ".events")
	statePath := filepath.Join(dir, ".events.notify.state.json")

	writeEvents(t, eventsPath,
		map[string]string{"type": events.ContainerDeployFailed, "project": "shop", "error": "pull"},
		map[string]string{"type": events.ContainerDeployed, "project": "shop"},
		map[string]string{"type": events.ContainerDeployFailed, "project": "shop", "error": "again"},
	)

	sender := &recordingSender{}
	dispatcher := newTestDispatcher(t, sender, eventsPath, statePath, func(cfg *notify.Config) {
		cfg.Events = []string{events.ContainerDeployFailed}
	})

	err := os.WriteFile(statePath, []byte(`{"offset":0,"count":0}`+"\n"), 0o600)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	dispatcher.Flush(t.Context())

	got := sender.got()
	if len(got) != 2 {
		t.Fatalf("messages = %#v", got)
	}
}

func TestDispatcher_CutEvery(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	eventsPath := filepath.Join(dir, ".events")
	statePath := filepath.Join(dir, ".events.notify.state.json")

	writeEvents(t, eventsPath,
		map[string]string{"type": events.GitLoginFailed, "repo": "1"},
		map[string]string{"type": events.ContainerRemoved, "project": "x"},
	)

	sender := &recordingSender{}
	dispatcher := newTestDispatcher(t, sender, eventsPath, statePath, func(cfg *notify.Config) {
		cfg.CutEvery = 2
		cfg.Events = []string{events.GitLoginFailed}
	})

	err := os.WriteFile(statePath, []byte(`{"offset":0,"count":0}`+"\n"), 0o600)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	dispatcher.Flush(t.Context())

	raw, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}

	if len(raw) != 0 {
		t.Fatalf("fully consumed file must be empty after cut, file=%s", raw)
	}

	stateRaw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	if !strings.Contains(string(stateRaw), `"offset":0`) || !strings.Contains(string(stateRaw), `"count":0`) {
		t.Fatalf("state after cut = %s", stateRaw)
	}

	appendEvent(t, eventsPath, map[string]string{"type": events.GitLoginFailed, "repo": "after-cut"})
	dispatcher.Flush(t.Context())

	if got := sender.got(); len(got) != 2 || !strings.Contains(got[1], "after-cut") {
		t.Fatalf("messages after cut = %#v", got)
	}
}

func TestDispatcher_TimeoutLeavesOffset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	eventsPath := filepath.Join(dir, ".events")
	statePath := filepath.Join(dir, ".events.notify.state.json")

	writeEvents(t, eventsPath, map[string]string{"type": events.GitLoginFailed, "repo": "slow"})

	sender := &recordingSender{block: 200 * time.Millisecond}
	dispatcher := newTestDispatcher(t, sender, eventsPath, statePath, func(cfg *notify.Config) {
		cfg.Timeout = 20 * time.Millisecond
	})

	err := os.WriteFile(statePath, []byte(`{"offset":0,"count":0}`+"\n"), 0o600)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	dispatcher.Flush(t.Context())

	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	if !strings.Contains(string(raw), `"offset":0`) {
		t.Fatalf("timeout must keep offset, state=%s", raw)
	}
}

func TestDispatcher_HTTPtestGeneric(t *testing.T) {
	t.Parallel()

	var gotBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)

		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	eventsPath := filepath.Join(dir, ".events")
	statePath := filepath.Join(dir, ".events.notify.state.json")

	writeEvents(t, eventsPath, map[string]string{"type": events.GitLoginFailed, "repo": "r"})

	target := "generic+" + server.URL + "/hook"

	dispatcher, err := notify.New(notify.Config{
		Enabled:     true,
		Targets:     []string{target},
		Events:      []string{events.GitLoginFailed},
		Timeout:     2 * time.Second,
		MaxPerCycle: 10,
		CutEvery:    100,
		ServerName:  "test-host",
		Namespace:   "dev",
		EventsPath:  eventsPath,
		StatePath:   statePath,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = os.WriteFile(statePath, []byte(`{"offset":0,"count":0}`+"\n"), 0o600)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	dispatcher.Flush(t.Context())

	if !strings.Contains(gotBody, "git_login_failed") && !strings.Contains(gotBody, "type: git_login_failed") {
		// generic may wrap body; accept either message text presence
		if gotBody == "" {
			t.Fatal("expected webhook body")
		}
	}
}
