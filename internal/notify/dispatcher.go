package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/uservseatb/apinfra/internal/events"
	"github.com/uservseatb/apinfra/internal/sys"
)

const (
	eventsFileMode = 0o600

	defaultMaxPerCycle = 10
	defaultCutEvery    = 100
	defaultTimeout     = 10 * time.Second
)

var errEventMissingType = errors.New("missing type")

// Config configures post-cycle notification flush from ~/.events.
type Config struct {
	Enabled     bool
	Targets     []string
	Events      []string
	Timeout     time.Duration
	MaxPerCycle int
	CutEvery    int
	ServerName  string
	Namespace   string
	EventsPath  string
	StatePath   string
}

// Dispatcher tails ~/.events and sends matching alerts via Shoutrrr.
type Dispatcher struct {
	cfg    Config
	sender Sender
	allow  map[string]struct{}
}

// New creates a dispatcher. When cfg.Enabled is false, Flush is a no-op.
func New(cfg Config) (*Dispatcher, error) {
	var sender Sender
	if cfg.Enabled {
		sender = newShoutrrrSender(cfg.Targets)
	}

	return newDispatcher(cfg, sender)
}

// NewWithSender creates a dispatcher with a custom Sender (tests).
func NewWithSender(cfg Config, sender Sender) (*Dispatcher, error) {
	return newDispatcher(cfg, sender)
}

func newDispatcher(cfg Config, sender Sender) (*Dispatcher, error) {
	if cfg.MaxPerCycle <= 0 {
		cfg.MaxPerCycle = defaultMaxPerCycle
	}

	if cfg.CutEvery <= 0 {
		cfg.CutEvery = defaultCutEvery
	}

	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}

	if cfg.EventsPath == "" {
		path, err := events.DefaultPath()
		if err != nil {
			return nil, fmt.Errorf("events path: %w", err)
		}

		cfg.EventsPath = path
	}

	if cfg.StatePath == "" {
		path, err := defaultStatePath()
		if err != nil {
			return nil, fmt.Errorf("notify state path: %w", err)
		}

		cfg.StatePath = path
	}

	allow := make(map[string]struct{}, len(cfg.Events))
	for _, kind := range cfg.Events {
		allow[kind] = struct{}{}
	}

	return &Dispatcher{
		cfg:    cfg,
		sender: sender,
		allow:  allow,
	}, nil
}

// Prepare anchors the cursor at EOF when notify state is missing so historical
// events are not replayed. Call before the update cycle; Flush after.
func (d *Dispatcher) Prepare() {
	if d == nil || !d.cfg.Enabled {
		return
	}

	if stateFileExists(d.cfg.StatePath) {
		return
	}

	size, err := eventsFileSize(d.cfg.EventsPath)
	if err != nil {
		log.Printf("[notify] WARN prepare: %v", err)

		return
	}

	err = saveState(d.cfg.StatePath, runtimeState{
		Offset: size,
		Count:  0,
	})
	if err != nil {
		log.Printf("[notify] WARN prepare: %v", err)
	}
}

// Flush reads new ~/.events lines after the update cycle and sends notifications.
// Errors are logged as warnings and never returned to the caller.
func (d *Dispatcher) Flush(ctx context.Context) {
	if d == nil || !d.cfg.Enabled || d.sender == nil {
		return
	}

	err := d.flush(ctx)
	if err != nil {
		log.Printf("[notify] WARN flush: %v", err)
	}
}

func (d *Dispatcher) flush(ctx context.Context) error {
	state, err := loadState(d.cfg.StatePath)
	if err != nil {
		return err
	}

	fileSize, err := eventsFileSize(d.cfg.EventsPath)
	if err != nil {
		return err
	}

	if state.Offset > fileSize {
		state.Offset = fileSize

		return saveState(d.cfg.StatePath, state)
	}

	lines, err := readLinesFrom(d.cfg.EventsPath, state.Offset)
	if err != nil {
		return err
	}

	sent := 0

	for _, line := range lines {
		if sent >= d.cfg.MaxPerCycle {
			break
		}

		advanced, sendFailed, stepErr := d.processLine(ctx, &state, line)
		if stepErr != nil {
			return stepErr
		}

		if sendFailed {
			return nil
		}

		if advanced {
			sent++
		}
	}

	d.maybeCut(&state)

	return nil
}

func (d *Dispatcher) processLine(
	ctx context.Context,
	state *runtimeState,
	line eventLine,
) (bool, bool, error) {
	entry, parseErr := parseEventLine(line.text)
	if parseErr != nil {
		log.Printf("[notify] WARN skip invalid event line: %v", parseErr)

		return false, false, d.advance(state, line.nextOffset)
	}

	kind := entry[eventsFieldType]
	if !d.eventAllowed(kind) {
		return false, false, d.advance(state, line.nextOffset)
	}

	message := formatMessage(d.cfg.ServerName, d.cfg.Namespace, entry)

	sendCtx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	sendErr := d.sender.Send(sendCtx, message)

	cancel()

	if sendErr != nil {
		log.Printf("[notify] WARN send %s: %v", kind, sys.RedactURLUserinfo(sendErr.Error()))

		return false, true, nil
	}

	err := d.advance(state, line.nextOffset)
	if err != nil {
		return false, false, err
	}

	return true, false, nil
}

func (d *Dispatcher) advance(state *runtimeState, nextOffset int64) error {
	state.Offset = nextOffset
	state.Count++

	return saveState(d.cfg.StatePath, *state)
}

type eventLine struct {
	text       string
	nextOffset int64
}

func readLinesFrom(path string, offset int64) ([]eventLine, error) {
	file, err := os.Open(path) // #nosec G304 -- host events path
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("open events %s: %w", path, err)
	}

	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat events %s: %w", path, err)
	}

	if offset > info.Size() {
		return nil, nil
	}

	_, err = file.Seek(offset, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("seek events %s: %w", path, err)
	}

	reader := bufio.NewReader(file)
	pos := offset

	var out []eventLine

	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			next := pos + int64(len(line))
			text := strings.TrimSuffix(line, "\n")
			text = strings.TrimSuffix(text, "\r")

			if text != "" {
				out = append(out, eventLine{text: text, nextOffset: next})
			}

			pos = next
		}

		if readErr == io.EOF {
			break
		}

		if readErr != nil {
			return nil, fmt.Errorf("read events %s: %w", path, readErr)
		}
	}

	return out, nil
}

func parseEventLine(line string) (map[string]string, error) {
	var entry map[string]string

	err := json.Unmarshal([]byte(line), &entry)
	if err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}

	if entry[eventsFieldType] == "" {
		return nil, errEventMissingType
	}

	return entry, nil
}

func (d *Dispatcher) eventAllowed(kind string) bool {
	if len(d.allow) == 0 {
		return false
	}

	_, ok := d.allow[kind]

	return ok
}

func (d *Dispatcher) maybeCut(state *runtimeState) {
	if state.Count < int64(d.cfg.CutEvery) || state.Offset <= 0 {
		return
	}

	err := cutEventsPrefix(d.cfg.EventsPath, state.Offset)
	if err != nil {
		log.Printf("[notify] WARN cut events: %v", err)

		return
	}

	state.Offset = 0
	state.Count = 0

	err = saveState(d.cfg.StatePath, *state)
	if err != nil {
		log.Printf("[notify] WARN save state after cut: %v", err)
	} else {
		log.Printf("[notify] cut consumed prefix of %s", d.cfg.EventsPath)
	}
}

func cutEventsPrefix(path string, offset int64) error {
	file, err := os.Open(path) // #nosec G304
	if err != nil {
		return fmt.Errorf("open events for cut: %w", err)
	}

	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat events for cut: %w", err)
	}

	if offset > info.Size() {
		offset = info.Size()
	}

	_, err = file.Seek(offset, io.SeekStart)
	if err != nil {
		return fmt.Errorf("seek events for cut: %w", err)
	}

	tail, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read events tail: %w", err)
	}

	return atomicWriteFile(path, tail, eventsFileMode)
}

func eventsFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}

		return 0, fmt.Errorf("stat events %s: %w", path, err)
	}

	return info.Size(), nil
}

func stateFileExists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}
