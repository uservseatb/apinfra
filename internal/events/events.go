package events

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	eventsFileName = ".events"
	eventsFileMode = 0o600
)

// FileStore appends JSONL events to a host file (typically ~/.events).
type FileStore struct {
	path string
	mu   sync.Mutex
}

// DefaultPath returns <home>/.events.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}

	return filepath.Join(home, eventsFileName), nil
}

// NewFileStore creates a store that appends to path.
func NewFileStore(path string) *FileStore {
	//nolint:exhaustruct_v5 // mu zero value is ready to use
	return &FileStore{path: path}
}

// Write appends one JSONL event with kind and optional string fields.
// Signature matches local EventRecorder interfaces in git/docker/nginx.
func (s *FileStore) Write(kind string, fields map[string]string) error {
	if s == nil {
		return nil
	}

	entry := map[string]string{
		"ts":   time.Now().UTC().Format(time.RFC3339),
		"kind": kind,
	}

	for k, v := range fields {
		if k == "ts" || k == "kind" {
			continue
		}

		entry[k] = v
	}

	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	// #nosec G304 -- host events path
	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, eventsFileMode)
	if err != nil {
		return fmt.Errorf("open events %s: %w", s.path, err)
	}

	defer func() { _ = file.Close() }()

	err = file.Chmod(eventsFileMode)
	if err != nil {
		return fmt.Errorf("chmod events %s: %w", s.path, err)
	}

	_, err = file.Write(line)
	if err != nil {
		return fmt.Errorf("write events %s: %w", s.path, err)
	}

	return nil
}
