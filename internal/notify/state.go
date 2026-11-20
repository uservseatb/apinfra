package notify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	stateFileName = ".events.notify.state.json"
	stateFileMode = 0o600
	stateDirMode  = 0o755
)

type persistedState struct {
	Offset int64 `json:"offset"`
	Count  int64 `json:"count"`
}

type runtimeState struct {
	Offset int64
	Count  int64
}

func defaultStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}

	return filepath.Join(home, stateFileName), nil
}

func loadState(path string) (runtimeState, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- host notify state path
	if err != nil {
		if os.IsNotExist(err) {
			return runtimeState{Offset: 0, Count: 0}, nil
		}

		return runtimeState{}, fmt.Errorf("read notify state %s: %w", path, err)
	}

	var raw persistedState

	err = json.Unmarshal(data, &raw)
	if err != nil {
		return runtimeState{}, fmt.Errorf("parse notify state %s: %w", path, err)
	}

	return runtimeState{
		Offset: max(raw.Offset, 0),
		Count:  max(raw.Count, 0),
	}, nil
}

func saveState(path string, state runtimeState) error {
	payload, err := json.Marshal(persistedState(state))
	if err != nil {
		return fmt.Errorf("marshal notify state: %w", err)
	}

	payload = append(payload, '\n')

	return atomicWriteFile(path, payload, stateFileMode)
}

func atomicWriteFile(path string, payload []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	err := os.MkdirAll(dir, stateDirMode) // #nosec G301
	if err != nil {
		return fmt.Errorf("mkdir for %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}

	tmpName := tmp.Name()

	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	_, err = tmp.Write(payload)
	if err != nil {
		cleanup()

		return fmt.Errorf("write temp %s: %w", tmpName, err)
	}

	err = tmp.Chmod(perm)
	if err != nil {
		cleanup()

		return fmt.Errorf("chmod temp %s: %w", tmpName, err)
	}

	err = tmp.Close()
	if err != nil {
		_ = os.Remove(tmpName)

		return fmt.Errorf("close temp %s: %w", tmpName, err)
	}

	err = os.Rename(tmpName, path)
	if err != nil {
		_ = os.Remove(tmpName)

		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}

	return nil
}
