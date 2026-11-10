package docker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uservseatb/apinfra/internal/domain/docker"
	"github.com/uservseatb/apinfra/internal/infrastructure"
)

func TestValidateProjects_VolumePolicyTable(t *testing.T) {
	t.Parallel()

	const (
		trustedDigest = "timberio/vector@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		otherDigest   = "timberio/vector@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	)

	tests := []struct {
		name       string
		compose    string
		allowlist  []string
		wantErr    bool
		wantReason string
	}{
		{
			name:    "named_volume_ok",
			compose: "services:\n  db:\n    image: postgres:16\n    volumes:\n      - db_data:/var/lib/postgresql/data\nvolumes:\n  db_data:\n",
		},
		{
			name:       "root_bind_rejected",
			compose:    "services:\n  app:\n    image: alpine\n    volumes:\n      - /:/host\n",
			wantErr:    true,
			wantReason: "disallowed_bind_mount",
		},
		{
			name:       "relative_bind_rejected",
			compose:    "services:\n  app:\n    image: alpine\n    volumes:\n      - ./data:/data\n",
			wantErr:    true,
			wantReason: "disallowed_bind_mount",
		},
		{
			name:       "parent_relative_bind_rejected",
			compose:    "services:\n  app:\n    image: alpine\n    volumes:\n      - ../etc:/etc\n",
			wantErr:    true,
			wantReason: "disallowed_bind_mount",
		},
		{
			name:       "sock_without_digest_rejected",
			compose:    "services:\n  vector:\n    image: timberio/vector:0.34.0\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n",
			allowlist:  []string{trustedDigest},
			wantErr:    true,
			wantReason: "untrusted_docker_socket_mount",
		},
		{
			name:       "sock_digest_not_in_allowlist",
			compose:    "services:\n  vector:\n    image: " + otherDigest + "\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n",
			allowlist:  []string{trustedDigest},
			wantErr:    true,
			wantReason: "untrusted_docker_socket_mount",
		},
		{
			name:      "sock_trusted_digest_ok",
			compose:   "services:\n  vector:\n    image: " + trustedDigest + "\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n",
			allowlist: []string{trustedDigest},
		},
		{
			name:       "sock_missing_allowlist_file",
			compose:    "services:\n  vector:\n    image: " + trustedDigest + "\n    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n",
			wantErr:    true,
			wantReason: "untrusted_docker_socket_mount",
		},
		{
			name:    "no_sock_missing_allowlist_ok",
			compose: "services:\n  web:\n    image: nginx:1.30-alpine\n",
		},
		{
			name:       "tmpfs_rejected",
			compose:    "services:\n  app:\n    image: alpine\n    volumes:\n      - type: tmpfs\n        target: /tmp\n",
			wantErr:    true,
			wantReason: "disallowed_volume_type",
		},
		{
			name:       "image_volume_rejected",
			compose:    "services:\n  app:\n    image: alpine\n    volumes:\n      - type: image\n        source: alpine\n        target: /data\n",
			wantErr:    true,
			wantReason: "disallowed_volume_type",
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			trustedPath := filepath.Join(t.TempDir(), ".trusted_images")
			if len(tcase.allowlist) > 0 {
				content := strings.Join(tcase.allowlist, "\n") + "\n"

				writeErr := os.WriteFile(trustedPath, []byte(content), 0o600)
				if writeErr != nil {
					t.Fatalf("write allowlist: %v", writeErr)
				}
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
				Name:        "app",
				ComposeYAML: tcase.compose,
			}

			err := svc.ValidateProjects(context.Background(), []docker.Project{project}, nil)
			if tcase.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}

				if len(evs.got) != 1 || evs.got[0].kind != "security_violation" {
					t.Fatalf("events = %+v, want one security_violation", evs.got)
				}

				if evs.got[0].fields["reason"] != tcase.wantReason {
					t.Fatalf("reason = %q, want %q", evs.got[0].fields["reason"], tcase.wantReason)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}

			if len(evs.got) != 0 {
				t.Fatalf("unexpected events: %+v", evs.got)
			}
		})
	}
}
