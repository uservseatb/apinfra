package acme_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/uservseatb/apinfra/internal/acme"
)

const (
	testDomain = "app.example.com"
	issuedFile = ".issued-mode"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()

	for name, content := range files {
		path := filepath.Join(dir, name)

		err := os.MkdirAll(filepath.Dir(path), 0o755)
		if err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		err = os.WriteFile(path, []byte(content), 0o600)
		if err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func liveDir(infra string) string {
	return filepath.Join(infra, "certbot", "conf", "live", testDomain)
}

func mockDir(infra string) string {
	return filepath.Join(infra, "mockcerts", testDomain)
}

func TestNewPaths(t *testing.T) {
	t.Parallel()

	const infra = "/infra"

	tests := []struct {
		mode    string
		wantDir string
	}{
		{mode: acme.ModeMock, wantDir: "/infra/mockcerts/app.example.com"},
		{mode: acme.ModeProd, wantDir: "/infra/certbot/conf/live/app.example.com"},
		{mode: acme.ModeTest, wantDir: "/infra/certbot/conf/live/app.example.com"},
	}

	for _, tcase := range tests {
		t.Run(tcase.mode, func(t *testing.T) {
			t.Parallel()

			got := acme.NewPaths(infra, testDomain, tcase.mode)
			want := acme.Paths{
				Fullchain: tcase.wantDir + "/fullchain.pem",
				Privkey:   tcase.wantDir + "/privkey.pem",
				Mode:      tcase.mode,
			}

			if got != want {
				t.Fatalf("paths = %+v, want %+v", got, want)
			}
		})
	}
}

func TestFilesPresent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string // relative to certbot/conf/live/<domain>
		want  bool
	}{
		{name: "nothing", files: nil},
		{name: "both_non_empty", files: map[string]string{"fullchain.pem": "c", "privkey.pem": "k"}, want: true},
		{name: "missing_key", files: map[string]string{"fullchain.pem": "c"}},
		{name: "empty_cert", files: map[string]string{"fullchain.pem": "", "privkey.pem": "k"}},
		{name: "directory_instead_of_file", files: map[string]string{"fullchain.pem/x": "c", "privkey.pem": "k"}},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			infra := t.TempDir()
			writeFiles(t, liveDir(infra), tcase.files)

			if got := acme.FilesPresent(infra, testDomain, acme.ModeProd); got != tcase.want {
				t.Fatalf("FilesPresent = %v, want %v", got, tcase.want)
			}
		})
	}

	t.Run("mock_and_live_dirs_are_independent", func(t *testing.T) {
		t.Parallel()

		infra := t.TempDir()
		writeFiles(t, mockDir(infra), map[string]string{"fullchain.pem": "c", "privkey.pem": "k"})

		if !acme.FilesPresent(infra, testDomain, acme.ModeMock) {
			t.Fatal("mock PEMs must be found under mockcerts/")
		}

		if acme.FilesPresent(infra, testDomain, acme.ModeProd) {
			t.Fatal("mock PEMs must not satisfy prod")
		}
	})
}

func TestPEMsMatchMode(t *testing.T) {
	t.Parallel()

	pems := map[string]string{"fullchain.pem": "c", "privkey.pem": "k"}

	tests := []struct {
		name      string
		files     map[string]string // relative to certbot/conf/live/<domain>
		askedMode string
		want      bool
		wantIssue string
		wantOK    bool
	}{
		{
			name:      "prod_marker_matches_prod",
			files:     map[string]string{"fullchain.pem": "c", "privkey.pem": "k", issuedFile: "prod\n"},
			askedMode: acme.ModeProd, want: true, wantIssue: "prod", wantOK: true,
		},
		{
			name:      "test_marker_matches_test",
			files:     map[string]string{"fullchain.pem": "c", "privkey.pem": "k", issuedFile: "test\n"},
			askedMode: acme.ModeTest, want: true, wantIssue: "test", wantOK: true,
		},
		{
			name:      "prod_marker_does_not_match_test",
			files:     map[string]string{"fullchain.pem": "c", "privkey.pem": "k", issuedFile: "prod\n"},
			askedMode: acme.ModeTest, wantIssue: "prod", wantOK: true,
		},
		{
			name:      "test_marker_does_not_match_prod",
			files:     map[string]string{"fullchain.pem": "c", "privkey.pem": "k", issuedFile: "test\n"},
			askedMode: acme.ModeProd, wantIssue: "test", wantOK: true,
		},
		{name: "legacy_without_marker", files: pems, askedMode: acme.ModeProd},
		{
			name:      "blank_marker",
			files:     map[string]string{"fullchain.pem": "c", "privkey.pem": "k", issuedFile: "  \n"},
			askedMode: acme.ModeProd,
		},
		{
			name:      "marker_without_pems",
			files:     map[string]string{issuedFile: "prod\n"},
			askedMode: acme.ModeProd, wantIssue: "prod", wantOK: true,
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			infra := t.TempDir()
			writeFiles(t, liveDir(infra), tcase.files)

			if got := acme.PEMsMatchMode(infra, testDomain, tcase.askedMode); got != tcase.want {
				t.Fatalf("PEMsMatchMode = %v, want %v", got, tcase.want)
			}

			issued, ok := acme.IssuedMode(infra, testDomain, tcase.askedMode)
			if issued != tcase.wantIssue || ok != tcase.wantOK {
				t.Fatalf("IssuedMode = (%q, %v), want (%q, %v)", issued, ok, tcase.wantIssue, tcase.wantOK)
			}
		})
	}
}
