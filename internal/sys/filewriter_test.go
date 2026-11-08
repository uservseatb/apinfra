package sys_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/sys"
)

func writeChanged(t *testing.T, path, content string, perm os.FileMode) bool {
	t.Helper()

	changed, err := sys.FileWriter{}.WriteFile(path, content, perm)
	if err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}

	return changed
}

func TestFileWriter_WriteFile_IsIdempotent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "dir", "app.conf")

	if !writeChanged(t, path, "v1", sys.FileModeReadable) {
		t.Fatal("first write must report changed")
	}

	got, err := os.ReadFile(path)
	if err != nil || string(got) != "v1" {
		t.Fatalf("content = %q, err = %v, want v1", got, err)
	}

	old := time.Now().Add(-time.Hour).Truncate(time.Second)

	err = os.Chtimes(path, old, old)
	if err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if writeChanged(t, path, "v1", sys.FileModeReadable) {
		t.Fatal("same content must not report changed")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if !info.ModTime().Equal(old) {
		t.Fatalf("mtime changed on idempotent write: %s, want %s", info.ModTime(), old)
	}

	if !writeChanged(t, path, "v2", sys.FileModeReadable) {
		t.Fatal("new content must report changed")
	}
}

func TestFileWriter_WriteFile_EnforcesPermission(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		initial  os.FileMode
		wantPerm os.FileMode
		content  string
	}{
		{name: "new_private_file", initial: 0, wantPerm: sys.FileModePrivate, content: "secret"},
		{name: "same_content_fixes_loose_mode", initial: 0o666, wantPerm: sys.FileModePrivate, content: "same"},
		{name: "read_only_file_is_rewritten", initial: 0o400, wantPerm: sys.FileModeReadable, content: "new"},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "f")

			if tcase.initial != 0 {
				err := os.WriteFile(path, []byte("same"), tcase.initial)
				if err != nil {
					t.Fatalf("seed: %v", err)
				}

				err = os.Chmod(path, tcase.initial)
				if err != nil {
					t.Fatalf("seed chmod: %v", err)
				}
			}

			writeChanged(t, path, tcase.content, tcase.wantPerm)

			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}

			if info.Mode().Perm() != tcase.wantPerm {
				t.Fatalf("mode = %v, want %v", info.Mode().Perm(), tcase.wantPerm)
			}

			got, err := os.ReadFile(path)
			if err != nil || string(got) != tcase.content {
				t.Fatalf("content = %q, err = %v, want %q", got, err, tcase.content)
			}
		})
	}
}

func TestFileWriter_Write_ReplacesDirectoryWithFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "mounted.conf")

	err := os.MkdirAll(filepath.Join(path, "leftover"), 0o750)
	if err != nil {
		t.Fatalf("seed dir: %v", err)
	}

	changed, err := sys.FileWriter{}.Write(path, "data")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	if !changed {
		t.Fatal("replacing a directory must report changed")
	}

	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("want regular file, info=%v err=%v", info, err)
	}
}
