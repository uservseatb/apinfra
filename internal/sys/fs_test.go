package sys_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/uservseatb/apinfra/internal/sys"
)

func TestEnsureInfraLayout_CreatesTreeAndEnforcesTraversableModes(t *testing.T) {
	t.Parallel()

	infra := filepath.Join(t.TempDir(), "home", "infra")
	apps := filepath.Join(infra, "apps")
	certbot := filepath.Join(infra, "certbot")
	nginxDir := filepath.Join(infra, "nginx")

	for _, dir := range []string{apps, certbot, nginxDir} {
		err := os.MkdirAll(dir, 0o750)
		if err != nil {
			t.Fatalf("seed %s: %v", dir, err)
		}
	}

	var err error

	for range 2 {
		err = sys.EnsureInfraLayout(infra)
		if err != nil {
			t.Fatalf("EnsureInfraLayout: %v", err)
		}
	}

	wantDirs := []string{
		filepath.Dir(infra),
		infra,
		apps,
		certbot,
		nginxDir,
		filepath.Join(infra, "nginx", "conf"),
		filepath.Join(infra, "nginx", "leconf"),
		filepath.Join(infra, "certbot", "conf"),
		filepath.Join(infra, "certbot", "www"),
		filepath.Join(infra, "mockcerts"),
	}

	for _, dir := range wantDirs {
		info, statErr := os.Stat(dir)
		if statErr != nil {
			t.Fatalf("Stat(%s): %v", dir, statErr)
		}

		if !info.IsDir() {
			t.Fatalf("%s is not a directory", dir)
		}

		if info.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode = %v, want 0755", dir, info.Mode().Perm())
		}
	}
}
