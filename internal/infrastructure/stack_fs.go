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

	appsDirName     = "apps"
	composeFileName = "docker-compose.yml"
	envFileName     = ".env"
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

	envChanged, err := writeFile(envPath, envContent, infraEnvFileMode)
	if err != nil {
		return false, fmt.Errorf("project %s write .env: %w", projectName, err)
	}

	composePath := filepath.Join(appDir, composeFileName)

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
