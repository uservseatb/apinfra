package sys_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/uservseatb/apinfra/internal/sys"
)

func TestEnsureInfraLayout_CreatesTreeAndEnforcesModes(t *testing.T) {
	t.Parallel()

	infra := filepath.Join(t.TempDir(), "home", "infra")
	apps := filepath.Join(infra, "apps")
	certbot := filepath.Join(infra, "certbot")
	nginxDir := filepath.Join(infra, "nginx")

	for _, dir := range []string{apps, certbot, nginxDir} {
		err := os.MkdirAll(dir, 0o755)
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

	traversable := []string{
		filepath.Dir(infra),
		infra,
		certbot,
		nginxDir,
		filepath.Join(infra, "nginx", "conf"),
		filepath.Join(infra, "nginx", "leconf"),
		filepath.Join(infra, "certbot", "conf"),
		filepath.Join(infra, "certbot", "www"),
		filepath.Join(infra, "mockcerts"),
	}

	for _, dir := range traversable {
		assertDirMode(t, dir, 0o755)
	}

	assertDirMode(t, apps, 0o700)
}

func assertDirMode(t *testing.T, dir string, want os.FileMode) {
	t.Helper()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%s): %v", dir, err)
	}

	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}

	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %v, want %v", dir, info.Mode().Perm(), want)
	}
}
