package nginx

import (
	"context"
	"time"
)

// IngressFileSystem is the host filesystem port for nginx confs and cert metadata.
//
//nolint:interfacebloat // plan ports: single host FS port for conf + cert lifecycle
type IngressFileSystem interface {
	EnsureHostFiles() error
	UpdateMainNginxConfig() (changed bool, err error)
	PrepareConfDir() error
	WriteConfig(fileName, content string, validate func() error) (changed bool, err error)
	RemoveStaleConfigs(keepFileNames map[string]struct{}, validate func() error) (removed bool, err error)
	SnapshotConfigs(identityToFileName map[string]string) map[string]ConfigSnapshot
	RestoreConfigSnapshots(snapshots map[string]ConfigSnapshot, identities []string) (restored bool)
	MarkReloadPending() error
	HasReloadPending() (bool, error)
	ClearReloadPending() error
	CertFilesPresent(domain, mode string) bool
	StashCertDir(domain, mode string) (finish func(issued bool))
	RemoveCertMaterial(domain string)
	LoadInstalledCerts() ([]CertDescriptor, error)
	SaveInstalledCerts(certs []CertDescriptor) (changed bool, err error)
}

// ProxyController runs nginx -t / reload against the proxy container.
type ProxyController interface {
	TestConfiguration(ctx context.Context, timeout time.Duration) error
	Reload(ctx context.Context, timeout time.Duration) error
}

// DNSResolver looks up A/AAAA records for ACME DNS preflight.
type DNSResolver interface {
	LookupShortIPs(ctx context.Context, domain string, timeout time.Duration) ([]string, error)
}

// CertificateIssuer ensures cert PEMs (ACME / mock) without coupling to certmanagement.
// ResolveMode returns the mode that will actually be issued (an active downgrade may lower it).
type CertificateIssuer interface {
	ResolveMode(domain, requested string) (string, error)
	EnsureCertificate(ctx context.Context, domain, email, mode string, timeout time.Duration) (IssuedCertificate, error)
}
