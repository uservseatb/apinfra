package certmanagement

import (
	"context"
	"time"
)

// StateRepository persists retry and downgrade certificate state.
type StateRepository interface {
	LoadRetries() (map[string]RetryEntry, error)
	SaveRetries(retries map[string]RetryEntry) error
	LoadDowngrades(now time.Time) (map[string]DowngradeEntry, error)
	SaveDowngrades(downgrades map[string]DowngradeEntry) error
}

// AcmeClient issues certificates and inspects existing PEM material.
type AcmeClient interface {
	Issue(ctx context.Context, domain, email, mode string, timeout time.Duration) (CertPaths, error)
	IssueSecondary(ctx context.Context, domain, email, mode string, timeout time.Duration) (CertPaths, error)
	HasSecondary() bool
	PEMsMatchMode(domain, mode string) bool
	PathsFor(domain, mode string) CertPaths
	IsRateLimited(err error) bool
	RateLimitCooldown(err error, configuredTTL time.Duration) (time.Duration, bool)
}

// EventRecorder records operational certificate events.
type EventRecorder interface {
	Write(kind string, fields map[string]string) error
}
