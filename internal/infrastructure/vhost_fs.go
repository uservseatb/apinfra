package infrastructure

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/uservseatb/apinfra/internal/assets"
	"github.com/uservseatb/apinfra/internal/domain/nginx"
	"github.com/uservseatb/apinfra/internal/sys"
)

const (
	certsConfigFile   = ".installedCert"
	certTimeLayout    = "2006-01-02T15:04:05.000"
	certIssueMock     = "mock"
	certIssueProd     = "prod"
	legacyAppConf     = "app.conf"
	legacySSLConf     = "ssl.conf"
	reloadPendingName = ".reload_pending"
	reloadPendingMode = fileModePrivate
)

var errNginxHostIsDir = errors.New("nginx host path is a directory (expected file)")

// VhostFS implements nginx.IngressFileSystem for the host infra tree.
type VhostFS struct {
	infraDir string
}

// NewVhostFS builds a VhostFS rooted at infraDir.
func NewVhostFS(infraDir string) *VhostFS {
	return &VhostFS{infraDir: infraDir}
}

// EnsureHostFiles writes bind-mounted nginx files and verifies they are regular files.
// Asset changes mark nginx/.reload_pending via UpdateMainNginxConfig.
func (f *VhostFS) EnsureHostFiles() error {
	_, err := f.UpdateMainNginxConfig()
	if err != nil {
		return err
	}

	for _, rel := range nginxHostRelativeFiles() {
		path := filepath.Join(f.infraDir, rel)

		st, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("nginx host file missing %s: %w", path, err)
		}

		if st.IsDir() {
			return fmt.Errorf("%w: %s", errNginxHostIsDir, path)
		}
	}

	return nil
}

// UpdateMainNginxConfig copies embedded nginx/ssl assets into the infra tree.
func (f *VhostFS) UpdateMainNginxConfig() (bool, error) {
	base := filepath.Dir(f.infraDir)

	nginxConfChanged, err := copyResourceFile(base, "configs/nginx.conf", "infra/nginx/nginx.conf")
	if err != nil {
		return false, err
	}

	sslOptsChanged, err := copyResourceFile(
		base, "configs/options-ssl-nginx.conf", "infra/nginx/leconf/options-ssl-nginx.conf",
	)
	if err != nil {
		return false, err
	}

	dhParamsChanged, err := copyResourceFile(base, "ssl-dhparams.pem", "infra/nginx/leconf/ssl-dhparams.pem")
	if err != nil {
		return false, err
	}

	changed := nginxConfChanged || sslOptsChanged || dhParamsChanged
	if !changed {
		return false, nil
	}

	err = f.MarkReloadPending()
	if err != nil {
		return true, err
	}

	return true, nil
}

// PrepareConfDir creates nginx/conf and removes legacy conf files.
func (f *VhostFS) PrepareConfDir() error {
	confDir := f.confDir()

	err := os.MkdirAll(confDir, dirModeTraversable) // #nosec G301
	if err != nil {
		return fmt.Errorf("mkdir conf.d: %w", err)
	}

	rmErr := os.Remove(filepath.Join(confDir, legacyAppConf))
	if rmErr != nil && !os.IsNotExist(rmErr) {
		log.Printf("[nginx] WARN remove legacy %s: %v", legacyAppConf, rmErr)
	}

	rmErr = os.Remove(filepath.Join(confDir, legacySSLConf))
	if rmErr != nil && !os.IsNotExist(rmErr) {
		log.Printf("[nginx] WARN remove legacy %s: %v", legacySSLConf, rmErr)
	}

	return nil
}

// WriteConfig writes a conf.d file with backup + optional validate callback.
// A content change marks nginx/.reload_pending before returning.
func (f *VhostFS) WriteConfig(fileName, content string, validate func() error) (bool, error) {
	dest := filepath.Join(f.confDir(), fileName)

	changed, err := writeWithBackup(dest, content, validate)
	if err != nil || !changed {
		return changed, err
	}

	err = f.MarkReloadPending()
	if err != nil {
		return true, err
	}

	return true, nil
}

// RemoveStaleConfigs removes .conf files not in keepFileNames, with validate/rollback.
func (f *VhostFS) RemoveStaleConfigs(
	keepFileNames map[string]struct{},
	validate func() error,
) (bool, error) {
	confDir := f.confDir()

	stale, err := collectStaleConfigs(confDir, keepFileNames)
	if err != nil {
		return false, err
	}

	if len(stale) == 0 {
		return false, nil
	}

	removed, removeErr := deleteStaleBatch(stale)
	if removeErr != nil {
		restoreErr := restoreStaleBatch(removed)
		if restoreErr != nil {
			return false, fmt.Errorf(
				"remove stale batch failed: %w; restore also failed: %w",
				removeErr, restoreErr,
			)
		}

		return false, fmt.Errorf("remove stale batch failed, restored: %w", removeErr)
	}

	validateErr := validateStaleBatch(removed, validate)
	if validateErr != nil {
		return false, validateErr
	}

	for _, snap := range removed {
		_ = os.Remove(snap.path + ".bak")
		log.Printf("[nginx] removed stale config %s", snap.path)
	}

	err = f.MarkReloadPending()
	if err != nil {
		return true, err
	}

	return true, nil
}

// MarkReloadPending records that live nginx still needs to pick up disk conf.
func (f *VhostFS) MarkReloadPending() error {
	nginxDir := f.nginxDir()

	err := os.MkdirAll(nginxDir, dirModeTraversable) // #nosec G301
	if err != nil {
		return fmt.Errorf("mkdir nginx dir: %w", err)
	}

	pendingPath := filepath.Join(nginxDir, reloadPendingName)

	_, err = writeFile(pendingPath, "", reloadPendingMode)
	if err != nil {
		return fmt.Errorf("mark reload pending: %w", err)
	}

	return nil
}

// HasReloadPending reports whether nginx/.reload_pending exists.
func (f *VhostFS) HasReloadPending() (bool, error) {
	pendingPath := filepath.Join(f.nginxDir(), reloadPendingName)

	_, err := os.Stat(pendingPath)
	if err == nil {
		return true, nil
	}

	if os.IsNotExist(err) {
		return false, nil
	}

	return false, fmt.Errorf("stat %s: %w", pendingPath, err)
}

// ClearReloadPending removes nginx/.reload_pending after a successful reload.
func (f *VhostFS) ClearReloadPending() error {
	pendingPath := filepath.Join(f.nginxDir(), reloadPendingName)

	err := os.Remove(pendingPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear reload pending: %w", err)
	}

	return nil
}

// SnapshotConfigs reads current conf files for the given identity→fileName map.
func (f *VhostFS) SnapshotConfigs(identityToFileName map[string]string) map[string]nginx.ConfigSnapshot {
	out := make(map[string]nginx.ConfigSnapshot, len(identityToFileName))
	confDir := f.confDir()

	for identity, fileName := range identityToFileName {
		path := filepath.Join(confDir, fileName)

		data, err := os.ReadFile(path) // #nosec G304 -- managed nginx conf
		if err != nil {
			if os.IsNotExist(err) {
				out[identity] = nginx.ConfigSnapshot{FileName: fileName, Existed: false, Data: nil}

				continue
			}

			log.Printf("[nginx] WARN snapshot conf %s: %v", path, err)

			continue
		}

		out[identity] = nginx.ConfigSnapshot{FileName: fileName, Existed: true, Data: data}
	}

	return out
}

// RestoreConfigSnapshots restores only the named identities' conf snapshots.
func (f *VhostFS) RestoreConfigSnapshots(
	snapshots map[string]nginx.ConfigSnapshot,
	identities []string,
) bool {
	changed := false

	for _, name := range identities {
		snap, ok := snapshots[name]
		if !ok {
			continue
		}

		if f.restoreConfSnapshot(snap) {
			changed = true
		}
	}

	if changed {
		err := f.MarkReloadPending()
		if err != nil {
			log.Printf("[nginx] WARN mark reload pending after restore: %v", err)
		}
	}

	return changed
}

// CertFilesPresent reports whether PEM files exist for domain+mode.
func (f *VhostFS) CertFilesPresent(domain, mode string) bool {
	dir := certHostDir(f.infraDir, domain, mode)
	for _, name := range []string{"fullchain.pem", "privkey.pem"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.IsDir() || st.Size() == 0 {
			return false
		}
	}

	return true
}

// StashCertDir moves a PEM dir aside so reissue cannot no-op on existing files.
func (f *VhostFS) StashCertDir(domain, mode string) func(issued bool) {
	dir := certHostDir(f.infraDir, domain, mode)
	stash := dir + ".reissue"

	err := os.RemoveAll(stash)
	if err != nil {
		log.Printf("[nginx] WARN remove stale cert stash %s: %v", stash, err)
	}

	err = os.Rename(dir, stash)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[nginx] WARN stash cert dir %s: %v", dir, err)
		}

		return func(bool) {}
	}

	return func(issued bool) {
		if issued {
			dropCertStash(stash)

			return
		}

		clearErr := os.RemoveAll(dir)
		if clearErr != nil {
			log.Printf("[nginx] WARN clear partial cert dir %s: %v", dir, clearErr)
		}

		restoreErr := os.Rename(stash, dir)
		if restoreErr != nil {
			log.Printf("[nginx] WARN restore cert dir %s: %v", dir, restoreErr)
		}
	}
}

// RemoveCertMaterial deletes mock and ACME PEM directories for a cert identity.
func (f *VhostFS) RemoveCertMaterial(domain string) {
	if domain == "" {
		return
	}

	for _, mode := range []string{certIssueMock, certIssueProd} {
		dir := certHostDir(f.infraDir, domain, mode)

		err := os.RemoveAll(dir)
		if err != nil {
			log.Printf("[nginx] WARN remove cert dir %s: %v", dir, err)
		}
	}
}

// LoadInstalledCerts reads .installedCert.
func (f *VhostFS) LoadInstalledCerts() ([]nginx.CertDescriptor, error) {
	cfgPath := certConfigFilePath(f.infraDir)

	props, err := readProperties(cfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, err
	}

	res := make([]nginx.CertDescriptor, 0, len(props))

	var parseErrs []error

	for key, val := range props {
		parsed, mode, parseErr := parseCertValue(val)
		if parseErr != nil {
			parseErrs = append(parseErrs, fmt.Errorf("domain %s: %w", key, parseErr))

			continue
		}

		res = append(res, nginx.CertDescriptor{
			DomainName:    key,
			IssueDateTime: parsed,
			Mode:          mode,
		})
	}

	if len(parseErrs) > 0 {
		return nil, fmt.Errorf("parse %s: %w", cfgPath, errors.Join(parseErrs...))
	}

	return res, nil
}

// SaveInstalledCerts writes .installedCert.
func (f *VhostFS) SaveInstalledCerts(certs []nginx.CertDescriptor) (bool, error) {
	var builder strings.Builder

	for i, cert := range certs {
		if i > 0 {
			builder.WriteByte('\n')
		}

		builder.WriteString(cert.DomainName)
		builder.WriteByte('=')
		builder.WriteString(formatCertValue(cert))
	}

	return writeFile(certConfigFilePath(f.infraDir), builder.String(), fileModeReadable)
}

func (f *VhostFS) nginxDir() string {
	return filepath.Join(f.infraDir, "nginx")
}

func (f *VhostFS) confDir() string {
	return filepath.Join(f.nginxDir(), "conf")
}

func nginxHostRelativeFiles() []string {
	return []string{
		"nginx/nginx.conf",
		"nginx/leconf/options-ssl-nginx.conf",
		"nginx/leconf/ssl-dhparams.pem",
	}
}

func copyResourceFile(baseDir, fromResource, toRelativePath string) (bool, error) {
	content, err := assets.LoadText(fromResource)
	if err != nil {
		return false, fmt.Errorf("load resource %s: %w", fromResource, err)
	}

	dest := filepath.Join(baseDir, toRelativePath)

	return writeFile(dest, content, fileModeReadable)
}

func certHostDir(infraDirPath, domainName, mode string) string {
	if mode == certIssueMock {
		return filepath.Join(infraDirPath, "mockcerts", domainName)
	}

	return filepath.Join(infraDirPath, "certbot", "conf", "live", domainName)
}

func certConfigFilePath(infraDirPath string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(infraDirPath)), certsConfigFile)
}

func formatCertValue(cert nginx.CertDescriptor) string {
	timeStr := cert.IssueDateTime.UTC().Format(certTimeLayout)
	if cert.Mode == "" {
		return timeStr
	}

	return timeStr + "|" + cert.Mode
}

func parseCertValue(v string) (time.Time, string, error) {
	mode := ""

	timePart := v
	if part, rest, ok := strings.Cut(v, "|"); ok {
		timePart = part
		mode = strings.TrimSpace(rest)
	}

	parsed, err := time.Parse(certTimeLayout, strings.TrimSpace(timePart))
	if err != nil {
		return time.Time{}, "", fmt.Errorf("parse cert time: %w", err)
	}

	return parsed, mode, nil
}

func readProperties(path string) (map[string]string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- .installedCert under host layout
	if err != nil {
		return nil, fmt.Errorf("open properties %s: %w", path, err)
	}

	props, err := sys.ParsePropertiesString(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse properties %s: %w", path, err)
	}

	return props, nil
}

func dropCertStash(stash string) {
	err := os.RemoveAll(stash)
	if err != nil {
		log.Printf("[nginx] WARN remove cert stash %s: %v", stash, err)
	}
}

func (f *VhostFS) restoreConfSnapshot(snap nginx.ConfigSnapshot) bool {
	path := filepath.Join(f.confDir(), snap.FileName)

	if !snap.Existed {
		err := os.Remove(path)
		if err != nil {
			if os.IsNotExist(err) {
				return false
			}

			log.Printf("[nginx] WARN remove new conf %s: %v", path, err)

			return false
		}

		_ = os.Remove(path + ".bak")

		return true
	}

	changed, err := writeFile(path, string(snap.Data), fileModeReadable)
	if err != nil {
		log.Printf("[nginx] WARN restore conf %s: %v", path, err)

		return false
	}

	return changed
}

type staleConfSnapshot struct {
	path string
	orig []byte
}

func collectStaleConfigs(confDir string, keep map[string]struct{}) ([]staleConfSnapshot, error) {
	entries, err := os.ReadDir(confDir)
	if err != nil {
		return nil, fmt.Errorf("read conf dir: %w", err)
	}

	var stale []staleConfSnapshot

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".conf") {
			continue
		}

		if _, ok := keep[entry.Name()]; ok {
			continue
		}

		path := filepath.Join(confDir, entry.Name())

		orig, readErr := os.ReadFile(path) // #nosec G304
		if readErr != nil {
			return nil, fmt.Errorf("read stale %s: %w", path, readErr)
		}

		stale = append(stale, staleConfSnapshot{path: path, orig: orig})
	}

	return stale, nil
}

func deleteStaleBatch(stale []staleConfSnapshot) ([]staleConfSnapshot, error) {
	removed := make([]staleConfSnapshot, 0, len(stale))

	for _, snap := range stale {
		bakPath := snap.path + ".bak"

		_, err := writeFile(bakPath, string(snap.orig), fileModeReadable)
		if err != nil {
			return removed, fmt.Errorf("backup stale %s: %w", snap.path, err)
		}

		err = os.Remove(snap.path)
		if err != nil {
			return removed, fmt.Errorf("remove stale %s: %w", snap.path, err)
		}

		removed = append(removed, snap)
	}

	return removed, nil
}

func validateStaleBatch(removed []staleConfSnapshot, validate func() error) error {
	if validate == nil {
		return nil
	}

	err := validate()
	if err != nil {
		if errors.Is(err, nginx.ErrProxyNotReady) {
			return nil
		}

		restoreErr := restoreStaleBatch(removed)
		if restoreErr != nil {
			return fmt.Errorf(
				"nginx -t failed after removing stale configs; restore failed: %w; validate: %w",
				restoreErr, err,
			)
		}

		names := make([]string, 0, len(removed))
		for _, snap := range removed {
			names = append(names, snap.path)
			_ = os.Remove(snap.path + ".bak")
		}

		return fmt.Errorf(
			"nginx -t failed after removing stale configs %v, restored: %w",
			names, err,
		)
	}

	return nil
}

func restoreStaleBatch(removed []staleConfSnapshot) error {
	var firstErr error

	for _, snap := range removed {
		_, restoreErr := writeFile(snap.path, string(snap.orig), fileModeReadable)
		if restoreErr != nil && firstErr == nil {
			firstErr = fmt.Errorf("restore %s: %w", snap.path, restoreErr)
		}

		_ = os.Remove(snap.path + ".bak")
	}

	return firstErr
}

func writeWithBackup(filePath, content string, validate func() error) (bool, error) {
	orig, existed, err := readExistingFile(filePath)
	if err != nil {
		return false, err
	}

	if existed && string(orig) == content {
		log.Printf("nothing to change in %s", filePath)

		return false, nil
	}

	bakPath := filePath + ".bak"

	err = prepareBackup(filePath, bakPath, orig, existed)
	if err != nil {
		return false, err
	}

	_, err = writeFile(filePath, content, fileModeReadable)
	if err != nil {
		return false, err
	}

	err = validateOrRestore(filePath, bakPath, existed, validate)
	if err != nil {
		return false, err
	}

	err = os.Remove(bakPath)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("WARN remove bak %s: %v", bakPath, err)
	}

	log.Printf("updated %s", filePath)

	return true, nil
}

func prepareBackup(filePath, bakPath string, orig []byte, existed bool) error {
	if existed {
		_, err := writeFile(bakPath, string(orig), fileModeReadable)
		if err != nil {
			return fmt.Errorf("backup %s: %w", filePath, err)
		}

		return nil
	}

	err := os.Remove(bakPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale bak %s: %w", bakPath, err)
	}

	return nil
}

func validateOrRestore(filePath, bakPath string, existed bool, validate func() error) error {
	if validate == nil {
		return nil
	}

	err := validate()
	if err != nil {
		if errors.Is(err, nginx.ErrProxyNotReady) {
			return nil
		}

		restoreErr := restoreBackup(filePath, bakPath, existed)
		if restoreErr != nil {
			return fmt.Errorf("validate failed (%w) and restore failed: %w", err, restoreErr)
		}

		return fmt.Errorf("validate failed, rolled back %s: %w", filePath, err)
	}

	return nil
}

func restoreBackup(filePath, bakPath string, existed bool) error {
	if !existed {
		err := os.Remove(filePath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s after failed validate: %w", filePath, err)
		}

		return nil
	}

	data, err := os.ReadFile(bakPath) // #nosec G304
	if err != nil {
		return fmt.Errorf("read bak %s: %w", bakPath, err)
	}

	_, err = writeFile(filePath, string(data), fileModeReadable)
	if err != nil {
		return fmt.Errorf("restore %s from bak: %w", filePath, err)
	}

	err = os.Remove(bakPath)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("WARN remove bak %s: %v", bakPath, err)
	}

	return nil
}

var _ nginx.IngressFileSystem = (*VhostFS)(nil)
