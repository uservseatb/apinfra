// Package acme issues TLS certificates (ACME via lego, or self-signed mock).
// Retry, downgrade, and fallback policy belong to the caller.
package acme

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/uservseatb/apinfra/internal/certstore"
)

var errNoSecondary = errors.New("no secondary ACME provider configured")

// Config configures a Client.
type Config struct {
	InfraDir    string
	Provider    string // letsencrypt|zerossl
	KID, HMAC   string
	Directories Directories
	FS          fileWriter       // required; app passes sys.FileWriter
	Accounts    *certstore.Store // optional; persists ACME accounts in ~/.certs.json
}

// Client is a thin certificate issuer: one CA call per Issue / IssueSecondary.
type Client struct {
	infraDir string
	fs       fileWriter
	accounts *certstore.Store
	pair     providerPair
}

// NewClient builds a Client; empty Directories fields use public CA defaults.
func NewClient(cfg Config) *Client {
	return &Client{
		infraDir: cfg.InfraDir,
		fs:       cfg.FS,
		accounts: cfg.Accounts,
		pair:     newProviderPair(cfg.Provider, cfg.KID, cfg.HMAC, cfg.Directories),
	}
}

// HasSecondary reports whether a fallback CA is configured.
func (c *Client) HasSecondary() bool {
	return c.pair.fallback != nil
}

// Issue writes PEMs for domain in mode using the primary CA (or a mock self-signed cert).
func (c *Client) Issue(
	ctx context.Context,
	domain, email, mode string,
	timeout time.Duration,
) (Paths, error) {
	return c.issue(ctx, domain, email, mode, timeout, c.pair.primary, c.pair.primaryID)
}

// IssueSecondary is Issue against the fallback CA; it fails when none is configured.
func (c *Client) IssueSecondary(
	ctx context.Context,
	domain, email, mode string,
	timeout time.Duration,
) (Paths, error) {
	if !c.HasSecondary() {
		return Paths{}, errNoSecondary
	}

	return c.issue(ctx, domain, email, mode, timeout, c.pair.fallback, c.pair.fallbackID)
}

func (c *Client) issue(
	ctx context.Context,
	domain, email, mode string,
	timeout time.Duration,
	prov provider,
	providerID string,
) (Paths, error) {
	err := validateIssue(domain, mode)
	if err != nil {
		return Paths{}, err
	}

	if mode == ModeMock {
		log.Printf("[acme] issuing mock domain=%s", domain)

		err = writeMockCert(c.fs, c.infraDir, domain)
		if err != nil {
			return Paths{}, fmt.Errorf("write mock cert for %s: %w", domain, err)
		}

		log.Printf("[acme] mock written domain=%s", domain)

		return NewPaths(c.infraDir, domain, mode), nil
	}

	log.Printf("[acme] obtaining domain=%s mode=%s provider=%s", domain, mode, providerID)

	err = obtainCert(ctx, c.fs, c.accounts, c.infraDir, domain, email, mode, providerID, timeout, prov)
	if err != nil {
		return Paths{}, fmt.Errorf("provider %s: %w", providerID, err)
	}

	log.Printf("[acme] obtained domain=%s mode=%s provider=%s", domain, mode, providerID)

	return NewPaths(c.infraDir, domain, mode), nil
}

func validateIssue(domain, mode string) error {
	if domain == "" {
		return ErrEmptyDomain
	}

	switch mode {
	case "":
		return fmt.Errorf("%w: mode missing for %s", ErrEmptyDomain, domain)
	case ModeProd, ModeTest, ModeMock:
		return nil
	default:
		return fmt.Errorf("%w %q for %s", errInvalidMode, mode, domain)
	}
}
