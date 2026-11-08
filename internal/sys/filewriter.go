package sys

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

const (
	// FileModeReadable is the default mode for non-secret files (0644).
	FileModeReadable = 0o644
	// FileModePrivate is the mode for keys and host state files (0600).
	FileModePrivate = 0o600
	fileDirMode     = 0o755
)

// FileWriter writes agent-managed files idempotently (content + chmod).
// App passes FileWriter into packages that keep their own fileWriter interfaces.
type FileWriter struct{}

// Write writes content with mode 0644. Returns whether the file changed.
func (w FileWriter) Write(filePath, content string) (bool, error) {
	return w.WriteFile(filePath, content, FileModeReadable)
}

// WriteFile writes content with the given permission. Returns whether the file changed.
func (FileWriter) WriteFile(filePath, content string, perm os.FileMode) (bool, error) {
	err := removeDirIfPresent(filePath)
	if err != nil {
		return false, err
	}

	orig, existed, err := readExistingFile(filePath)
	if err != nil {
		return false, err
	}

	changed := !existed || string(orig) != content
	if changed {
		err = writeFileContent(filePath, content, perm, existed)
		if err != nil {
			return false, err
		}
	}

	err = os.Chmod(filePath, perm)
	if err != nil {
		return false, fmt.Errorf("chmod %s: %w", filePath, err)
	}

	return changed, nil
}

func removeDirIfPresent(filePath string) error {
	st, err := os.Stat(filePath)
	if err != nil || !st.IsDir() {
		return nil //nolint:nilerr // missing path or regular file needs no cleanup
	}

	log.Printf("removing directory at %s (expected a file, often created by docker bind-mount)", filePath)

	err = os.RemoveAll(filePath)
	if err != nil {
		return fmt.Errorf("remove dir %s: %w", filePath, err)
	}

	return nil
}

func readExistingFile(filePath string) ([]byte, bool, error) {
	orig, readErr := os.ReadFile(filePath) // #nosec G304
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, false, fmt.Errorf("read %s: %w", filePath, readErr)
	}

	return orig, readErr == nil, nil
}

func writeFileContent(filePath, content string, perm os.FileMode, existed bool) error {
	err := os.MkdirAll(filepath.Dir(filePath), fileDirMode)
	if err != nil { // #nosec G301
		return fmt.Errorf("mkdir for %s: %w", filePath, err)
	}

	if existed {
		err = os.Chmod(filePath, FileModePrivate)
		if err != nil {
			return fmt.Errorf("chmod writable %s: %w", filePath, err)
		}
	}

	err = os.WriteFile(filePath, []byte(content), perm) // #nosec G703 -- agent-managed paths
	if err != nil {
		return fmt.Errorf("write %s: %w", filePath, err)
	}

	log.Printf("updated %s", filePath)

	return nil
}
