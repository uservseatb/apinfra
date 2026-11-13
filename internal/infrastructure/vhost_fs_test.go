package infrastructure_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/domain/nginx"
	"github.com/uservseatb/apinfra/internal/infrastructure"
)

const (
	testDomain   = "example.com"
	modeMock     = "mock"
	modeProd     = "prod"
	fullchainPEM = "fullchain.pem"
	privkeyPEM   = "privkey.pem"
	confOld      = "server { old; }\n"
	confNew      = "server { new; }\n"
	bakExt       = ".bak"
)

var (
	errValidateRejected = errors.New("nginx -t: syntax error")

	errTestUnused = errors.New("validate must not be called")
)

func newVhostFS(t *testing.T) (*infrastructure.VhostFS, string) {
	t.Helper()

	infra := filepath.Join(t.TempDir(), "infra")

	err := os.MkdirAll(infra, 0o750)
	if err != nil {
		t.Fatalf("mkdir infra: %v", err)
	}

	return infrastructure.NewVhostFS(infra), infra
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()

	err := os.MkdirAll(filepath.Dir(path), 0o750)
	if err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}

	err = os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

func exists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

func confPath(infra, name string) string {
	return filepath.Join(infra, "nginx", "conf", name)
}

func TestVhostFS_ReloadPending_MarkOnWriteAndClear(t *testing.T) {
	t.Parallel()

	vhost, infra := newVhostFS(t)
	pendingPath := filepath.Join(infra, "nginx", ".reload_pending")

	has, err := vhost.HasReloadPending()
	if err != nil || has {
		t.Fatalf("HasReloadPending before write = %v, %v", has, err)
	}

	changed, err := vhost.WriteConfig("app.conf", confNew, nil)
	if err != nil || !changed {
		t.Fatalf("WriteConfig: changed=%v err=%v", changed, err)
	}

	has, err = vhost.HasReloadPending()
	if err != nil || !has {
		t.Fatalf("content change must create pending: has=%v err=%v", has, err)
	}

	info, err := os.Stat(pendingPath)
	if err != nil {
		t.Fatalf("stat pending: %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("pending mode = %v, want 0600", info.Mode().Perm())
	}

	changed, err = vhost.WriteConfig("app.conf", confNew, nil)
	if err != nil || changed {
		t.Fatalf("identical write: changed=%v err=%v", changed, err)
	}

	has, err = vhost.HasReloadPending()
	if err != nil || !has {
		t.Fatalf("identical write must keep pending: has=%v err=%v", has, err)
	}

	err = vhost.ClearReloadPending()
	if err != nil {
		t.Fatalf("ClearReloadPending: %v", err)
	}

	has, err = vhost.HasReloadPending()
	if err != nil || has {
		t.Fatalf("after clear HasReloadPending = %v, %v", has, err)
	}

	err = vhost.ClearReloadPending()
	if err != nil {
		t.Fatalf("clear absent pending must be a no-op: %v", err)
	}
}

func TestVhostFS_EnsureHostFiles_WritesAssetsIdempotentlyAndHealsDirs(t *testing.T) {
	t.Parallel()

	vhost, infra := newVhostFS(t)

	err := vhost.EnsureHostFiles()
	if err != nil {
		t.Fatalf("EnsureHostFiles: %v", err)
	}

	has, err := vhost.HasReloadPending()
	if err != nil || !has {
		t.Fatalf("first EnsureHostFiles must mark reload pending: has=%v err=%v", has, err)
	}

	err = vhost.ClearReloadPending()
	if err != nil {
		t.Fatalf("ClearReloadPending: %v", err)
	}

	hostFiles := []string{
		filepath.Join(infra, "nginx", "nginx.conf"),
		filepath.Join(infra, "nginx", "leconf", "options-ssl-nginx.conf"),
		filepath.Join(infra, "nginx", "leconf", "ssl-dhparams.pem"),
	}

	for _, path := range hostFiles {
		if strings.TrimSpace(readTestFile(t, path)) == "" {
			t.Fatalf("%s is empty", path)
		}
	}

	changed, err := vhost.UpdateMainNginxConfig()
	if err != nil || changed {
		t.Fatalf("second UpdateMainNginxConfig: changed=%v err=%v, want false/nil", changed, err)
	}

	// docker creates a directory when a bind-mounted file is missing on the host.
	err = os.Remove(hostFiles[0])
	if err != nil {
		t.Fatalf("remove nginx.conf: %v", err)
	}

	err = os.MkdirAll(filepath.Join(hostFiles[0], "junk"), 0o750)
	if err != nil {
		t.Fatalf("seed bind-mount dir: %v", err)
	}

	err = vhost.EnsureHostFiles()
	if err != nil {
		t.Fatalf("EnsureHostFiles after dir artifact: %v", err)
	}

	info, err := os.Stat(hostFiles[0])
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("nginx.conf not healed to regular file: info=%v err=%v", info, err)
	}
}

func TestVhostFS_WriteConfig_ValidateAndRollback(t *testing.T) {
	t.Parallel()

	const fileName = "app.example.com.conf"

	tests := []struct {
		name        string
		existing    *string
		validate    func() error
		wantChanged bool
		wantErr     error
		wantContent *string
	}{
		{
			name:        "new_file_valid",
			existing:    nil,
			validate:    func() error { return nil },
			wantChanged: true,
			wantErr:     nil,
			wantContent: new(confNew),
		},
		{
			name:        "same_content_skips_validate",
			existing:    new(confNew),
			validate:    func() error { return errTestUnused },
			wantChanged: false,
			wantErr:     nil,
			wantContent: new(confNew),
		},
		{
			name:        "existing_file_rolled_back_on_invalid",
			existing:    new(confOld),
			validate:    func() error { return errValidateRejected },
			wantChanged: false,
			wantErr:     errValidateRejected,
			wantContent: new(confOld),
		},
		{
			name:        "new_file_removed_on_invalid",
			existing:    nil,
			validate:    func() error { return errValidateRejected },
			wantChanged: false,
			wantErr:     errValidateRejected,
			wantContent: nil,
		},
		{
			name:        "proxy_not_ready_keeps_new_content",
			existing:    new(confOld),
			validate:    func() error { return nginx.ErrProxyNotReady },
			wantChanged: true,
			wantErr:     nil,
			wantContent: new(confNew),
		},
		{
			name:        "nil_validate_just_writes",
			existing:    new(confOld),
			validate:    nil,
			wantChanged: true,
			wantErr:     nil,
			wantContent: new(confNew),
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			vhost, infra := newVhostFS(t)

			err := vhost.PrepareConfDir()
			if err != nil {
				t.Fatalf("PrepareConfDir: %v", err)
			}

			path := confPath(infra, fileName)
			if tcase.existing != nil {
				writeTestFile(t, path, *tcase.existing)
			}

			changed, err := vhost.WriteConfig(fileName, confNew, tcase.validate)
			if changed != tcase.wantChanged {
				t.Fatalf("changed = %v, want %v (err=%v)", changed, tcase.wantChanged, err)
			}

			if tcase.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tcase.wantErr != nil && !errors.Is(err, tcase.wantErr) {
				t.Fatalf("error = %v, want %v in chain", err, tcase.wantErr)
			}

			switch {
			case tcase.wantContent == nil && exists(path):
				t.Fatalf("%s must not exist", path)
			case tcase.wantContent != nil && readTestFile(t, path) != *tcase.wantContent:
				t.Fatalf("content = %q, want %q", readTestFile(t, path), *tcase.wantContent)
			}

			if exists(path + bakExt) {
				t.Fatalf("backup %s.bak left behind", path)
			}
		})
	}
}

func TestVhostFS_PrepareConfDir_RemovesOnlyLegacyFiles(t *testing.T) {
	t.Parallel()

	vhost, infra := newVhostFS(t)

	writeTestFile(t, confPath(infra, "app.conf"), "legacy")
	writeTestFile(t, confPath(infra, "ssl.conf"), "legacy")
	writeTestFile(t, confPath(infra, "keep.example.com.conf"), confOld)

	err := vhost.PrepareConfDir()
	if err != nil {
		t.Fatalf("PrepareConfDir: %v", err)
	}

	if exists(confPath(infra, "app.conf")) || exists(confPath(infra, "ssl.conf")) {
		t.Fatal("legacy app.conf/ssl.conf must be removed")
	}

	if !exists(confPath(infra, "keep.example.com.conf")) {
		t.Fatal("per-domain conf must stay")
	}
}

func TestVhostFS_RemoveStaleConfigs(t *testing.T) {
	t.Parallel()

	seed := map[string]string{
		"keep.conf":  confOld,
		"stale.conf": confOld,
		"other.conf": confNew,
		"notes.txt":  "not a conf",
	}

	tests := []struct {
		name        string
		keep        map[string]struct{}
		validate    func() error
		wantRemoved bool
		wantErr     error
		wantStale   bool
	}{
		{
			name:        "removes_unlisted_conf_files_only",
			keep:        map[string]struct{}{"keep.conf": {}},
			validate:    func() error { return nil },
			wantRemoved: true,
			wantErr:     nil,
			wantStale:   false,
		},
		{
			name:        "restores_everything_when_validate_fails",
			keep:        map[string]struct{}{"keep.conf": {}},
			validate:    func() error { return errValidateRejected },
			wantRemoved: false,
			wantErr:     errValidateRejected,
			wantStale:   true,
		},
		{
			name:        "proxy_not_ready_keeps_removal",
			keep:        map[string]struct{}{"keep.conf": {}},
			validate:    func() error { return nginx.ErrProxyNotReady },
			wantRemoved: true,
			wantErr:     nil,
			wantStale:   false,
		},
		{
			name:        "nothing_stale_skips_validate",
			keep:        map[string]struct{}{"keep.conf": {}, "stale.conf": {}, "other.conf": {}},
			validate:    func() error { return errTestUnused },
			wantRemoved: false,
			wantErr:     nil,
			wantStale:   true,
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			vhost, infra := newVhostFS(t)

			for name, content := range seed {
				writeTestFile(t, confPath(infra, name), content)
			}

			removed, err := vhost.RemoveStaleConfigs(tcase.keep, tcase.validate)
			if removed != tcase.wantRemoved {
				t.Fatalf("removed = %v, want %v (err=%v)", removed, tcase.wantRemoved, err)
			}

			if tcase.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tcase.wantErr != nil && !errors.Is(err, tcase.wantErr) {
				t.Fatalf("error = %v, want %v in chain", err, tcase.wantErr)
			}

			for _, name := range []string{"stale.conf", "other.conf"} {
				if got := exists(confPath(infra, name)); got != tcase.wantStale {
					t.Fatalf("%s exists = %v, want %v", name, got, tcase.wantStale)
				}

				if tcase.wantStale && readTestFile(t, confPath(infra, name)) != seed[name] {
					t.Fatalf("%s content changed after rollback", name)
				}
			}

			for _, name := range []string{"keep.conf", "notes.txt"} {
				if readTestFile(t, confPath(infra, name)) != seed[name] {
					t.Fatalf("%s must be untouched", name)
				}
			}

			matches, globErr := filepath.Glob(confPath(infra, "*"+bakExt))
			if globErr != nil || len(matches) != 0 {
				t.Fatalf("backups left behind: %v (err=%v)", matches, globErr)
			}
		})
	}
}

func TestVhostFS_SnapshotAndRestoreConfigs(t *testing.T) {
	t.Parallel()

	vhost, infra := newVhostFS(t)

	writeTestFile(t, confPath(infra, "a.conf"), confOld)
	writeTestFile(t, confPath(infra, "c.conf"), confOld)

	files := map[string]string{"a": "a.conf", "b": "b.conf", "c": "c.conf"}
	snapshots := vhost.SnapshotConfigs(files)

	if !snapshots["a"].Existed || string(snapshots["a"].Data) != confOld || snapshots["b"].Existed {
		t.Fatalf("unexpected snapshots: %+v", snapshots)
	}

	writeTestFile(t, confPath(infra, "a.conf"), confNew)
	writeTestFile(t, confPath(infra, "b.conf"), confNew)
	writeTestFile(t, confPath(infra, "c.conf"), confNew)

	changed := vhost.RestoreConfigSnapshots(snapshots, []string{"a", "b", "unknown"})
	if !changed {
		t.Fatal("restore must report changes")
	}

	if got := readTestFile(t, confPath(infra, "a.conf")); got != confOld {
		t.Fatalf("a.conf = %q, want original", got)
	}

	if exists(confPath(infra, "b.conf")) {
		t.Fatal("b.conf did not exist in snapshot and must be removed")
	}

	if got := readTestFile(t, confPath(infra, "c.conf")); got != confNew {
		t.Fatalf("c.conf not in restore list, must stay %q, got %q", confNew, got)
	}

	if vhost.RestoreConfigSnapshots(snapshots, []string{"a", "b"}) {
		t.Fatal("second restore must be a no-op")
	}
}

func TestVhostFS_CertMaterial_StashRestoreAndRemove(t *testing.T) {
	t.Parallel()

	vhost, infra := newVhostFS(t)

	prodDir := filepath.Join(infra, "certbot", "conf", "live", testDomain)
	mockDir := filepath.Join(infra, "mockcerts", testDomain)

	if vhost.CertFilesPresent(testDomain, modeProd) {
		t.Fatal("no certs yet")
	}

	writeTestFile(t, filepath.Join(prodDir, fullchainPEM), "old-chain")
	writeTestFile(t, filepath.Join(prodDir, privkeyPEM), "")

	if vhost.CertFilesPresent(testDomain, modeProd) {
		t.Fatal("empty privkey must count as absent")
	}

	writeTestFile(t, filepath.Join(prodDir, privkeyPEM), "old-key")

	if !vhost.CertFilesPresent(testDomain, modeProd) || vhost.CertFilesPresent(testDomain, modeMock) {
		t.Fatal("prod present, mock absent expected")
	}

	// step: failed_issue_restores_original
	{
		finish := vhost.StashCertDir(testDomain, modeProd)

		if vhost.CertFilesPresent(testDomain, modeProd) {
			t.Fatal("certs must be moved aside while reissuing")
		}

		writeTestFile(t, filepath.Join(prodDir, fullchainPEM), "partial")
		finish(false)

		if got := readTestFile(t, filepath.Join(prodDir, fullchainPEM)); got != "old-chain" {
			t.Fatalf("fullchain = %q, want original restored", got)
		}

		if exists(prodDir + ".reissue") {
			t.Fatal("stash must be consumed on restore")
		}
	}

	// step: successful_issue_drops_stash
	{
		finish := vhost.StashCertDir(testDomain, modeProd)

		writeTestFile(t, filepath.Join(prodDir, fullchainPEM), "new-chain")
		writeTestFile(t, filepath.Join(prodDir, privkeyPEM), "new-key")
		finish(true)

		if got := readTestFile(t, filepath.Join(prodDir, fullchainPEM)); got != "new-chain" {
			t.Fatalf("fullchain = %q, want new", got)
		}

		if exists(prodDir + ".reissue") {
			t.Fatal("stash must be dropped after successful issue")
		}
	}

	// step: missing_dir_returns_noop
	{
		finish := vhost.StashCertDir("absent.example.com", modeMock)
		finish(false)
		finish(true)
	}

	// step: remove_deletes_mock_and_prod
	{
		writeTestFile(t, filepath.Join(mockDir, fullchainPEM), "m")

		vhost.RemoveCertMaterial("")

		if !exists(prodDir) || !exists(mockDir) {
			t.Fatal("empty domain must not delete anything")
		}

		vhost.RemoveCertMaterial(testDomain)

		if exists(prodDir) || exists(mockDir) {
			t.Fatal("cert dirs must be removed")
		}
	}
}

func TestVhostFS_InstalledCerts_RoundTrip(t *testing.T) {
	t.Parallel()

	vhost, infra := newVhostFS(t)

	got, err := vhost.LoadInstalledCerts()
	if err != nil || got != nil {
		t.Fatalf("missing file: got=%v err=%v, want nil/nil", got, err)
	}

	issued := time.Date(2025, 1, 2, 3, 4, 5, 678_000_000, time.UTC)
	certs := []nginx.CertDescriptor{
		{DomainName: "b.example.com", IssueDateTime: issued, Mode: modeProd},
		{DomainName: "a.example.com", IssueDateTime: issued.Add(time.Hour), Mode: ""},
	}

	changed, err := vhost.SaveInstalledCerts(certs)
	if err != nil || !changed {
		t.Fatalf("first save: changed=%v err=%v", changed, err)
	}

	changed, err = vhost.SaveInstalledCerts(certs)
	if err != nil || changed {
		t.Fatalf("second save: changed=%v err=%v, want false/nil", changed, err)
	}

	loaded, err := vhost.LoadInstalledCerts()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	slices.SortFunc(loaded, func(left, right nginx.CertDescriptor) int {
		return strings.Compare(left.DomainName, right.DomainName)
	})

	if len(loaded) != len(certs) {
		t.Fatalf("loaded %d certs, want %d: %+v", len(loaded), len(certs), loaded)
	}

	for idx, want := range []nginx.CertDescriptor{certs[1], certs[0]} {
		have := loaded[idx]
		if have.DomainName != want.DomainName || have.Mode != want.Mode || !have.IssueDateTime.Equal(want.IssueDateTime) {
			t.Fatalf("cert[%d] = %+v, want %+v", idx, have, want)
		}
	}

	if !exists(filepath.Join(filepath.Dir(infra), ".certs.json")) {
		t.Fatal(".certs.json must live next to the infra dir")
	}

	info, err := os.Stat(filepath.Join(filepath.Dir(infra), ".certs.json"))
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf(".certs.json mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestVhostFS_LoadInstalledCerts_MigratesLegacyBrokenDomains(t *testing.T) {
	t.Parallel()

	vhost, infra := newVhostFS(t)

	writeTestFile(t, filepath.Join(filepath.Dir(infra), ".installedCert"),
		"good.example.com=2025-01-02T03:04:05.000|prod\nbad1.example.com=yesterday\n")

	_, err := vhost.LoadInstalledCerts()
	if err == nil {
		t.Fatal("want parse error")
	}

	if !strings.Contains(err.Error(), "bad1.example.com") {
		t.Fatalf("error %q must mention bad1.example.com", err)
	}
}
