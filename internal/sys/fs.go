package sys

import (
	"fmt"
	"os"
	"path/filepath"
)

const infraDirPerm = 0o755

// EnsureInfraLayout creates the infra directory tree nginx/certbot need for bind mounts.
// Directories are 0755 so nginx can traverse home→infra→certbot/www for ACME HTTP-01.
func EnsureInfraLayout(infraDirPath string) error {
	dirs := []string{
		filepath.Dir(filepath.Clean(infraDirPath)),
		infraDirPath,
		filepath.Join(infraDirPath, "apps"),
		filepath.Join(infraDirPath, "nginx"),
		filepath.Join(infraDirPath, "nginx", "conf"),
		filepath.Join(infraDirPath, "nginx", "leconf"),
		filepath.Join(infraDirPath, "certbot"),
		filepath.Join(infraDirPath, "certbot", "conf"),
		filepath.Join(infraDirPath, "certbot", "www"),
		filepath.Join(infraDirPath, "mockcerts"),
	}
	for _, dir := range dirs {
		err := os.MkdirAll(dir, infraDirPerm) // #nosec G301 -- world-traversable bind-mount parents for ACME/nginx
		if err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}

		err = os.Chmod(dir, infraDirPerm) // #nosec G302 -- enforce 0755 even if dir already existed as 0750
		if err != nil {
			return fmt.Errorf("chmod 0755 %s: %w", dir, err)
		}
	}

	return nil
}
