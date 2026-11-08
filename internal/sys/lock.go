package sys

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

const lockFilePerm = 0o600

var (
	// ErrAlreadyLocked is returned when another apinfra process holds the lock.
	ErrAlreadyLocked = errors.New("apinfra lock already held")
	errEmptyLockPath = errors.New("empty lock path")
)

// Lock holds an exclusive flock on a lock file.
type Lock struct {
	file *os.File
	path string
}

// Acquire tries to take an exclusive non-blocking flock on path.
// Caller must call Release when done.
func Acquire(path string) (*Lock, error) {
	if path == "" {
		return nil, errEmptyLockPath
	}

	// #nosec G304 -- lock path is provided by the caller (e.g. main)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, lockFilePerm)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}

	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		_ = file.Close()

		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrAlreadyLocked
		}

		return nil, fmt.Errorf("flock %s: %w", path, err)
	}

	return &Lock{file: file, path: path}, nil
}

// Release unlocks and closes the lock file.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}

	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	err := l.file.Close()
	l.file = nil

	if err != nil {
		return fmt.Errorf("close lock file %s: %w", l.path, err)
	}

	return nil
}
