package sys_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/uservseatb/apinfra/internal/sys"
)

func TestAcquire_ExclusiveUntilReleased(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "apinfra.lock")

	first, err := sys.Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	_, err = sys.Acquire(path)
	if !errors.Is(err, sys.ErrAlreadyLocked) {
		t.Fatalf("second Acquire: want ErrAlreadyLocked, got %v", err)
	}

	err = first.Release()
	if err != nil {
		t.Fatalf("Release: %v", err)
	}

	second, err := sys.Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}

	err = second.Release()
	if err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

func TestAcquire_RejectsUnusablePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "missing_parent_dir", path: filepath.Join(t.TempDir(), "missing", "apinfra.lock")},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			lock, err := sys.Acquire(tcase.path)
			if err == nil {
				t.Fatal("want error")
			}

			if lock != nil {
				t.Fatalf("want nil lock on error, got %v", lock)
			}
		})
	}
}

func TestLock_ReleaseIsSafeToRepeat(t *testing.T) {
	t.Parallel()

	var nilLock *sys.Lock

	err := nilLock.Release()
	if err != nil {
		t.Fatalf("nil Release: %v", err)
	}

	lock, err := sys.Acquire(filepath.Join(t.TempDir(), "apinfra.lock"))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	for range 2 {
		err = lock.Release()
		if err != nil {
			t.Fatalf("Release: %v", err)
		}
	}
}
