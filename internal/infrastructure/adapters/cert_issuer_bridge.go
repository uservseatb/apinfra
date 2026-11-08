package adapters

import (
	"context"
	"fmt"
	"time"

	"github.com/uservseatb/apinfra/internal/domain/certmanagement"
	"github.com/uservseatb/apinfra/internal/domain/nginx"
)

// CertIssuerBridge adapts *certmanagement.CertificateService to nginx.CertificateIssuer.
type CertIssuerBridge struct {
	svc *certmanagement.CertificateService
}

// NewCertIssuerBridge wraps a certificate domain service for nginx.
func NewCertIssuerBridge(svc *certmanagement.CertificateService) *CertIssuerBridge {
	return &CertIssuerBridge{svc: svc}
}

// ResolveMode returns the effective issue mode for domain (downgrade-aware).
func (b *CertIssuerBridge) ResolveMode(domain, requested string) (string, error) {
	mode, err := b.svc.ResolveMode(domain, requested)
	if err != nil {
		return "", fmt.Errorf("resolve cert mode %s: %w", domain, err)
	}

	return mode, nil
}

// EnsureCertificate issues or reuses a certificate for domain.
func (b *CertIssuerBridge) EnsureCertificate(
	ctx context.Context,
	domain, email, mode string,
	timeout time.Duration,
) (nginx.IssuedCertificate, error) {
	paths, err := b.svc.EnsureCertificate(ctx, domain, email, mode, timeout)
	if err != nil {
		return nginx.IssuedCertificate{}, fmt.Errorf("ensure certificate %s: %w", domain, err)
	}

	return nginx.IssuedCertificate{
		Fullchain: paths.Fullchain,
		Privkey:   paths.Privkey,
		Mode:      paths.Mode,
	}, nil
}

var _ nginx.CertificateIssuer = (*CertIssuerBridge)(nil)
