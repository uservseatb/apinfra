package adapters

import (
	"context"
	"fmt"
	"time"

	"github.com/uservseatb/apinfra/internal/acme"
	"github.com/uservseatb/apinfra/internal/domain/certmanagement"
)

// AcmeBridge adapts *acme.Client to certmanagement.AcmeClient.
type AcmeBridge struct {
	client   *acme.Client
	infraDir string
}

// NewAcmeBridge wraps an ACME client for the certificate domain.
func NewAcmeBridge(client *acme.Client, infraDir string) *AcmeBridge {
	return &AcmeBridge{client: client, infraDir: infraDir}
}

// Issue issues a certificate via the primary CA.
func (b *AcmeBridge) Issue(
	ctx context.Context,
	domain, email, mode string,
	timeout time.Duration,
) (certmanagement.CertPaths, error) {
	paths, err := b.client.Issue(ctx, domain, email, mode, timeout)
	if err != nil {
		return certmanagement.CertPaths{}, fmt.Errorf("acme issue %s: %w", domain, err)
	}

	return toCertPaths(paths), nil
}

// IssueSecondary issues a certificate via the fallback CA.
func (b *AcmeBridge) IssueSecondary(
	ctx context.Context,
	domain, email, mode string,
	timeout time.Duration,
) (certmanagement.CertPaths, error) {
	paths, err := b.client.IssueSecondary(ctx, domain, email, mode, timeout)
	if err != nil {
		return certmanagement.CertPaths{}, fmt.Errorf("acme issue secondary %s: %w", domain, err)
	}

	return toCertPaths(paths), nil
}

// HasSecondary reports whether a fallback CA is configured.
func (b *AcmeBridge) HasSecondary() bool {
	return b.client.HasSecondary()
}

// PEMsMatchMode reports whether PEMs exist and match mode.
func (b *AcmeBridge) PEMsMatchMode(domain, mode string) bool {
	return acme.PEMsMatchMode(b.infraDir, domain, mode)
}

// PathsFor returns host PEM paths for domain+mode.
func (b *AcmeBridge) PathsFor(domain, mode string) certmanagement.CertPaths {
	return toCertPaths(acme.NewPaths(b.infraDir, domain, mode))
}

// IsRateLimited reports whether err is an ACME rate limit.
func (b *AcmeBridge) IsRateLimited(err error) bool {
	return acme.IsRateLimited(err)
}

// RateLimitCooldown reports whether err is rate-limited and the cooldown duration.
func (b *AcmeBridge) RateLimitCooldown(err error, configuredTTL time.Duration) (time.Duration, bool) {
	return acme.RateLimitCooldown(err, configuredTTL)
}

func toCertPaths(paths acme.Paths) certmanagement.CertPaths {
	return certmanagement.CertPaths{
		Fullchain: paths.Fullchain,
		Privkey:   paths.Privkey,
		Mode:      paths.Mode,
	}
}
