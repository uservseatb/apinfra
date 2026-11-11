package sys

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	infraDirPerm = 0o755
	appsDirPerm  = 0o700
)

// EnsureInfraLayout creates the infra directory tree nginx/certbot need for bind mounts.
// Nginx/ACME parents stay 0755 so containers can traverse to mounted webroot/certs.
// apps/ is owner-only: generated compose stacks hold secrets and are not bind-mounted
// into the proxy.
func EnsureInfraLayout(infraDirPath string) error {
	traversable := []string{
		filepath.Dir(filepath.Clean(infraDirPath)),
		infraDirPath,
		filepath.Join(infraDirPath, "nginx"),
		filepath.Join(infraDirPath, "nginx", "conf"),
		filepath.Join(infraDirPath, "nginx", "leconf"),
		filepath.Join(infraDirPath, "certbot"),
		filepath.Join(infraDirPath, "certbot", "conf"),
		filepath.Join(infraDirPath, "certbot", "www"),
		filepath.Join(infraDirPath, "mockcerts"),
	}
	for _, dir := range traversable {
		err := os.MkdirAll(dir, infraDirPerm) // #nosec G301 -- world-traversable bind-mount parents for ACME/nginx
		if err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}

		err = os.Chmod(dir, infraDirPerm) // #nosec G302 -- enforce 0755 even if dir already existed as 0750
		if err != nil {
			return fmt.Errorf("chmod 0755 %s: %w", dir, err)
		}
	}

	appsDir := filepath.Join(infraDirPath, "apps")

	err := os.MkdirAll(appsDir, appsDirPerm)
	if err != nil {
		return fmt.Errorf("create %s: %w", appsDir, err)
	}

	err = os.Chmod(appsDir, appsDirPerm)
	if err != nil {
		return fmt.Errorf("chmod 0700 %s: %w", appsDir, err)
	}

	return nil
}
