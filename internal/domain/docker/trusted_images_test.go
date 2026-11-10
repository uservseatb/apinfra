package docker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/uservseatb/apinfra/internal/domain/docker"
	"github.com/uservseatb/apinfra/internal/infrastructure"
)

func TestValidateProjects_TrustedImagesAllowlistParsing(t *testing.T) {
	t.Parallel()

	const digest = "timberio/vector@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	tests := []struct {
		name      string
		allowlist string
		image     string
		wantOK    bool
	}{
		{
			name:      "exact_digest",
			allowlist: digest + "\n",
			image:     digest,
			wantOK:    true,
		},
		{
			name:      "case_insensitive_and_comments",
			allowlist: "# comment\n\nTimberio/Vector@sha256:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF\n",
			image:     digest,
			wantOK:    true,
		},
		{
			name:      "tag_only_rejected",
			allowlist: digest + "\n",
			image:     "timberio/vector:0.34.0",
			wantOK:    false,
		},
		{
			name:      "wrong_digest_rejected",
			allowlist: digest + "\n",
			image:     "timberio/vector@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			wantOK:    false,
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			trustedPath := filepath.Join(t.TempDir(), ".trusted_images")

			err := os.WriteFile(trustedPath, []byte(tcase.allowlist), 0o600)
			if err != nil {
				t.Fatalf("write allowlist: %v", err)
			}

			err = os.Chmod(trustedPath, 0o644)
			if err != nil {
				t.Fatalf("chmod: %v", err)
			}

			evs := &fakeEvents{}
			svc := docker.NewService(
				infrastructure.NewStackFS(filepath.Join(t.TempDir(), "infra")),
				newFakeCLI(),
				&fakeLister{},
				evs,
				docker.Config{TrustedImagesPath: trustedPath},
			)

			project := docker.Project{
				Name: "vector",
				ComposeYAML: "services:\n  vector:\n    image: " + tcase.image + "\n" +
					"    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n",
			}

			err = svc.ValidateProjects(context.Background(), []docker.Project{project}, nil)

			info, statErr := os.Stat(trustedPath)
			if statErr != nil {
				t.Fatalf("stat: %v", statErr)
			}

			if info.Mode().Perm() != 0o600 {
				t.Fatalf("mode = %o, want 0600 after allowlist read", info.Mode().Perm())
			}

			if tcase.wantOK {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}

				return
			}

			if err == nil {
				t.Fatal("expected error")
			}

			if len(evs.got) != 1 || evs.got[0].kind != "security_violation" {
				t.Fatalf("events = %+v, want security_violation", evs.got)
			}
		})
	}
}
