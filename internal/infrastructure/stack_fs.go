package infrastructure

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/uservseatb/apinfra/internal/domain/docker"
	"github.com/uservseatb/apinfra/internal/sys"
)

const (
	fileModeReadable   = 0o644
	fileModePrivate    = 0o600
	fileModeOwnerRead  = 0o400
	dirModeTraversable = 0o755

	infraEnvFileMode = fileModeOwnerRead

	appsDirName      = "apps"
	composeFileName  = "docker-compose.yml"
	envFileName      = ".env"
	applyPendingName = ".apply_pending"
	applyPendingMode = fileModePrivate
)

var (
	errUnsafeProjectName = errors.New("unsafe project name")
	appNameFromLabelRE   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)
)

// StackFS implements docker.StackFileSystem for the host infra tree.
type StackFS struct {
	infraDir string
}

// NewStackFS builds a StackFS rooted at infraDir.
func NewStackFS(infraDir string) *StackFS {
	return &StackFS{infraDir: infraDir}
}

// WriteProxyCompose writes infra/docker-compose.yml.
func (f *StackFS) WriteProxyCompose(content string) (bool, error) {
	composePath := filepath.Join(f.infraDir, composeFileName)

	changed, err := writeFile(composePath, content, fileModeReadable)
	if err != nil {
		return false, fmt.Errorf("write proxy docker-compose.yml: %w", err)
	}

	return changed, nil
}

// WriteAppStack writes apps/<name>/docker-compose.yml and .env.
// When either file content will change, .apply_pending is written first.
// changed is true when either file content changed.
func (f *StackFS) WriteAppStack(projectName, composeContent, envContent string) (bool, error) {
	appDir, err := appDirPath(f.infraDir, projectName)
	if err != nil {
		return false, fmt.Errorf("project %s: %w", projectName, err)
	}

	err = os.MkdirAll(appDir, dirModeTraversable) // #nosec G301 -- infra layout world-traversable
	if err != nil {
		return false, fmt.Errorf("project %s mkdir %s: %w", projectName, appDir, err)
	}

	envPath := filepath.Join(appDir, envFileName)
	composePath := filepath.Join(appDir, composeFileName)

	wouldChange, err := appStackWouldChange(envPath, composePath, envContent, composeContent)
	if err != nil {
		return false, fmt.Errorf("project %s: %w", projectName, err)
	}

	if wouldChange {
		err = f.MarkApplyPending(projectName)
		if err != nil {
			return false, err
		}
	}

	envChanged, err := writeFile(envPath, envContent, infraEnvFileMode)
	if err != nil {
		return false, fmt.Errorf("project %s write .env: %w", projectName, err)
	}

	composeChanged, err := writeFile(composePath, composeContent, fileModeReadable)
	if err != nil {
		return false, fmt.Errorf("project %s write docker-compose.yml: %w", projectName, err)
	}

	log.Printf(
		"[docker] wrote app stack project=%s composeChanged=%v envChanged=%v",
		projectName, composeChanged, envChanged,
	)

	return composeChanged || envChanged, nil
}

// MarkApplyPending records that apps/<name> still needs a successful compose up.
func (f *StackFS) MarkApplyPending(projectName string) error {
	appDir, err := appDirPath(f.infraDir, projectName)
	if err != nil {
		return fmt.Errorf("project %s: %w", projectName, err)
	}

	err = os.MkdirAll(appDir, dirModeTraversable) // #nosec G301 -- infra layout world-traversable
	if err != nil {
		return fmt.Errorf("project %s mkdir %s: %w", projectName, appDir, err)
	}

	pendingPath := filepath.Join(appDir, applyPendingName)

	_, err = writeFile(pendingPath, "", applyPendingMode)
	if err != nil {
		return fmt.Errorf("project %s mark apply pending: %w", projectName, err)
	}

	return nil
}

// HasApplyPending reports whether apps/<name>/.apply_pending exists.
func (f *StackFS) HasApplyPending(projectName string) (bool, error) {
	pendingPath, err := applyPendingPath(f.infraDir, projectName)
	if err != nil {
		return false, err
	}

	_, err = os.Stat(pendingPath)
	if err == nil {
		return true, nil
	}

	if os.IsNotExist(err) {
		return false, nil
	}

	return false, fmt.Errorf("stat %s: %w", pendingPath, err)
}

// ClearApplyPending removes apps/<name>/.apply_pending after a successful up.
func (f *StackFS) ClearApplyPending(projectName string) error {
	pendingPath, err := applyPendingPath(f.infraDir, projectName)
	if err != nil {
		return err
	}

	err = os.Remove(pendingPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("project %s clear apply pending: %w", projectName, err)
	}

	return nil
}

// ListAppDirs returns directory names under apps/.
func (f *StackFS) ListAppDirs() ([]string, error) {
	appsRoot := filepath.Join(f.infraDir, appsDirName)

	entries, err := os.ReadDir(appsRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, fmt.Errorf("read apps dir: %w", err)
	}

	var out []string

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		out = append(out, entry.Name())
	}

	return out, nil
}

// AppComposeExists reports whether apps/<name>/docker-compose.yml exists.
func (f *StackFS) AppComposeExists(projectName string) (bool, error) {
	composePath, err := appComposePath(f.infraDir, projectName)
	if err != nil {
		return false, err
	}

	_, err = os.Stat(composePath)
	if err == nil {
		return true, nil
	}

	if os.IsNotExist(err) {
		return false, nil
	}

	return false, fmt.Errorf("stat %s: %w", composePath, err)
}

// RemoveAppDir removes apps/<name>.
func (f *StackFS) RemoveAppDir(projectName string) error {
	appDir, err := appDirPath(f.infraDir, projectName)
	if err != nil {
		return err
	}

	err = os.RemoveAll(appDir)
	if err != nil {
		return fmt.Errorf("remove app dir project=%s: %w", projectName, err)
	}

	return nil
}

func appComposePath(infraDirPath, projectName string) (string, error) {
	appDir, err := appDirPath(infraDirPath, projectName)
	if err != nil {
		return "", err
	}

	return filepath.Join(appDir, composeFileName), nil
}

func applyPendingPath(infraDirPath, projectName string) (string, error) {
	appDir, err := appDirPath(infraDirPath, projectName)
	if err != nil {
		return "", fmt.Errorf("project %s: %w", projectName, err)
	}

	return filepath.Join(appDir, applyPendingName), nil
}

func appStackWouldChange(envPath, composePath, envContent, composeContent string) (bool, error) {
	envOrig, envExists, err := readExistingFile(envPath)
	if err != nil {
		return false, err
	}

	composeOrig, composeExists, err := readExistingFile(composePath)
	if err != nil {
		return false, err
	}

	envChanged := !envExists || string(envOrig) != envContent
	composeChanged := !composeExists || string(composeOrig) != composeContent

	return envChanged || composeChanged, nil
}

func appDirPath(infraDirPath, projectName string) (string, error) {
	if !appNameFromLabelRE.MatchString(projectName) {
		return "", fmt.Errorf("%w: %q", errUnsafeProjectName, projectName)
	}

	appsRoot := filepath.Clean(filepath.Join(infraDirPath, appsDirName))
	dir := filepath.Clean(filepath.Join(appsRoot, projectName))

	rel, err := filepath.Rel(appsRoot, dir)
	if err != nil || !isLocalSubpath(rel) {
		return "", fmt.Errorf("%w: %q escapes apps dir", errUnsafeProjectName, projectName)
	}

	return dir, nil
}

func isLocalSubpath(rel string) bool {
	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}

func writeFile(filePath, content string, perm os.FileMode) (bool, error) {
	changed, err := sys.FileWriter{}.WriteFile(filePath, content, perm)
	if err != nil {
		return false, fmt.Errorf("write file: %w", err)
	}

	return changed, nil
}

func readExistingFile(filePath string) ([]byte, bool, error) {
	orig, readErr := os.ReadFile(filePath) // #nosec G304 -- managed path under infraDir
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, false, fmt.Errorf("read %s: %w", filePath, readErr)
	}

	return orig, readErr == nil, nil
}

var _ docker.StackFileSystem = (*StackFS)(nil)
