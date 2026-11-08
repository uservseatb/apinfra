package acme

import (
	"os"
	"path/filepath"
	"strings"
)

// Certificate issue modes.
const (
	ModeProd = "prod"
	ModeTest = "test"
	ModeMock = "mock"
)

const (
	fullchainPEM   = "fullchain.pem"
	privkeyPEM     = "privkey.pem"
	issuedModeFile = ".issued-mode"

	dirModeTraversable = 0o755
	fileModePrivate    = 0o600
)

// Paths locates issued PEM files on the host.
type Paths struct {
	Fullchain string
	Privkey   string
	Mode      string
}

func NewPaths(infraDirPath, domainName, mode string) Paths {
	dir := certHostDir(infraDirPath, domainName, mode)

	return Paths{
		Fullchain: filepath.Join(dir, fullchainPEM),
		Privkey:   filepath.Join(dir, privkeyPEM),
		Mode:      mode,
	}
}

// certHostDir returns the host directory with fullchain.pem / privkey.pem for domain+mode.
// Mock: infra/mockcerts/<domain>/; ACME: infra/certbot/conf/live/<domain>/.
func certHostDir(infraDirPath, domainName, mode string) string {
	if mode == ModeMock {
		return filepath.Join(infraDirPath, "mockcerts", domainName)
	}

	return filepath.Join(infraDirPath, "certbot", "conf", "live", domainName)
}

// FilesPresent reports whether non-empty PEM files exist for domain+mode.
func FilesPresent(infraDirPath, domainName, mode string) bool {
	dir := certHostDir(infraDirPath, domainName, mode)

	for _, name := range []string{fullchainPEM, privkeyPEM} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.IsDir() || st.Size() == 0 {
			return false
		}
	}

	return true
}

// IssuedMode returns the mode recorded next to the PEMs for domain (prod/test share live/).
// ok is false when the marker is missing or unreadable (legacy cert dirs).
func IssuedMode(infraDirPath, domainName, mode string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(certHostDir(infraDirPath, domainName, mode), issuedModeFile)) // #nosec G304
	if err != nil {
		return "", false
	}

	got := strings.TrimSpace(string(data))
	if got == "" {
		return "", false
	}

	return got, true
}

// PEMsMatchMode reports whether PEMs exist and were issued for the given mode.
// Prod/test share live/; the .issued-mode marker distinguishes them.
func PEMsMatchMode(infraDirPath, domainName, mode string) bool {
	if !FilesPresent(infraDirPath, domainName, mode) {
		return false
	}

	got, ok := IssuedMode(infraDirPath, domainName, mode)

	return ok && got == mode
}
