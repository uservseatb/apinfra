package events_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/uservseatb/apinfra/internal/events"
)

func readEvents(t *testing.T, path string) []map[string]string {
	t.Helper()

	file, err := os.Open(path) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatalf("open events: %v", err)
	}

	t.Cleanup(func() { _ = file.Close() })

	var out []map[string]string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry map[string]string

		err = json.Unmarshal(scanner.Bytes(), &entry)
		if err != nil {
			t.Fatalf("invalid JSONL line %q: %v", scanner.Text(), err)
		}

		out = append(out, entry)
	}

	err = scanner.Err()
	if err != nil {
		t.Fatalf("scan events: %v", err)
	}

	return out
}

func TestPublisher_AddEvent_AppendsJSONLWithReservedKeysProtected(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".events")
	pub := events.NewPublisher(path)

	err := pub.AddEvent(events.GitLoginFailed, map[string]string{
		events.FieldRepo: "r",
		"type":           "forged",
		"ts":             "forged",
	})
	if err != nil {
		t.Fatalf("AddEvent #1: %v", err)
	}

	err = pub.AddEvent(events.CertIssueFailed, nil)
	if err != nil {
		t.Fatalf("AddEvent #2: %v", err)
	}

	got := readEvents(t, path)
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d", len(got))
	}

	if got[0]["type"] != events.GitLoginFailed || got[0][events.FieldRepo] != "r" {
		t.Fatalf("first event = %v", got[0])
	}

	if got[0]["ts"] == "forged" || got[0]["ts"] == "" {
		t.Fatalf("ts must be generated, got %q", got[0]["ts"])
	}

	if got[1]["type"] != events.CertIssueFailed {
		t.Fatalf("second event = %v", got[1])
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestPublisher_AddEvent_ConcurrentWritersKeepLinesIntact(t *testing.T) {
	t.Parallel()

	const writers = 20

	path := filepath.Join(t.TempDir(), ".events")
	pub := events.NewPublisher(path)

	var pending sync.WaitGroup

	errs := make(chan error, writers)

	for idx := range writers {
		pending.Go(func() {
			errs <- pub.AddEvent("tick", map[string]string{"n": strconv.Itoa(idx)})
		})
	}

	pending.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("AddEvent: %v", err)
		}
	}

	if got := readEvents(t, path); len(got) != writers {
		t.Fatalf("want %d intact lines, got %d", writers, len(got))
	}
}

func TestPublisher_AddEvent_NilPublisherAndBadPath(t *testing.T) {
	t.Parallel()

	var nilPub *events.Publisher

	err := nilPub.AddEvent("noop", nil)
	if err != nil {
		t.Fatalf("nil publisher must be a no-op, got %v", err)
	}

	bad := events.NewPublisher(filepath.Join(t.TempDir(), "missing", ".events"))

	err = bad.AddEvent("x", nil)
	if err == nil {
		t.Fatal("want error for unwritable path")
	}
}
