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

	fieldTS   = "ts"
	fieldType = "type"
)

// Publisher appends JSONL events to a host file (typically ~/.events).
type Publisher struct {
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

// NewPublisher creates a publisher that appends to path.
func NewPublisher(path string) *Publisher {
	//nolint:exhaustruct_v5 // mu zero value is ready to use
	return &Publisher{path: path}
}

// AddEvent appends one JSONL event with type and optional string fields.
// Signature matches local EventRecorder interfaces in git/docker/certmanagement.
func (p *Publisher) AddEvent(eventType string, fields map[string]string) error {
	if p == nil {
		return nil
	}

	entry := map[string]string{
		fieldTS:   time.Now().UTC().Format(time.RFC3339),
		fieldType: eventType,
	}

	for k, v := range fields {
		if k == fieldTS || k == fieldType {
			continue
		}

		entry[k] = v
	}

	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	line = append(line, '\n')

	p.mu.Lock()
	defer p.mu.Unlock()

	// #nosec G304 -- host events path
	file, err := os.OpenFile(p.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, eventsFileMode)
	if err != nil {
		return fmt.Errorf("open events %s: %w", p.path, err)
	}

	defer func() { _ = file.Close() }()

	err = file.Chmod(eventsFileMode)
	if err != nil {
		return fmt.Errorf("chmod events %s: %w", p.path, err)
	}

	_, err = file.Write(line)
	if err != nil {
		return fmt.Errorf("write events %s: %w", p.path, err)
	}

	return nil
}
