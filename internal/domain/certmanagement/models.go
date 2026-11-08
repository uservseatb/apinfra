package certmanagement

import (
	"errors"
	"slices"
	"time"
)

// Certificate issue modes.
const (
	ModeProd = "prod"
	ModeTest = "test"
	ModeMock = "mock"
	ModeNone = "none"
)

// CertTimeLayout is the wall-clock layout used for blocked_until / downgrade until.
const CertTimeLayout = "2006-01-02T15:04:05.000"

const (
	defaultTriesCount             = 3
	defaultBlockHours             = 24
	defaultDowngradeCertModeHours = 24
)

var (
	// ErrCertBlocked is returned when issuance is blocked by .retries cooldown.
	ErrCertBlocked = errors.New("certificate issue blocked")
	// ErrInvalidCertMode is returned for unknown certificate modes.
	ErrInvalidCertMode = errors.New("invalid certificate mode")
	// ErrEmptyDomain is returned when domain or mode is missing.
	ErrEmptyDomain = errors.New("empty domain")
)

// RetryEntry is the per-domain retry / block state.
type RetryEntry struct {
	Attempts     int
	Blocked      bool
	BlockedUntil time.Time
}

// DowngradeEntry is a temporary cert-mode override after ACME rate limiting.
type DowngradeEntry struct {
	Mode  string
	Until time.Time // zero = no expiry (legacy entries)
}

// CertRetryPolicy controls non–rate-limit failure blocking and .downgradeCertMode TTL.
type CertRetryPolicy struct {
	TriesCount           int
	BlockDuration        time.Duration
	DowngradeCertModeTTL time.Duration
}

// CertPaths locates issued PEM files on the host.
type CertPaths struct {
	Fullchain string
	Privkey   string
	Mode      string
}

// EffectiveRetryPolicy fills zero policy fields with defaults.
func EffectiveRetryPolicy(policy CertRetryPolicy) CertRetryPolicy {
	if policy.TriesCount <= 0 {
		policy.TriesCount = defaultTriesCount
	}

	if policy.BlockDuration <= 0 {
		policy.BlockDuration = time.Duration(defaultBlockHours) * time.Hour
	}

	if policy.DowngradeCertModeTTL <= 0 {
		policy.DowngradeCertModeTTL = time.Duration(defaultDowngradeCertModeHours) * time.Hour
	}

	return policy
}

// NextLowerMode returns the next lower certificate mode (prod→test→mock→none).
func NextLowerMode(mode string) (string, bool) {
	switch mode {
	case ModeProd:
		return ModeTest, true
	case ModeTest:
		return ModeMock, true
	case ModeMock:
		return ModeNone, true
	default:
		return "", false
	}
}

// modeRank orders certificate modes for downgrade comparison (higher = stronger).
// Unknown modes rank below none so they never override a requested mode.
func modeRank(mode string) int {
	return slices.Index([]string{ModeNone, ModeMock, ModeTest, ModeProd}, mode) + 1
}

// EffectiveMode returns an active downgrade override for domain when it is
// strictly lower than requested (prod > test > mock > none); otherwise requested.
func EffectiveMode(requested, domain string, downgrade map[string]DowngradeEntry, now time.Time) string {
	if domain != "" && downgrade != nil {
		if entry, ok := downgrade[domain]; ok && entry.Mode != "" && !DowngradeExpired(entry, now) {
			if modeRank(entry.Mode) < modeRank(requested) {
				return entry.Mode
			}
		}
	}

	return requested
}

// DowngradeExpired reports whether a downgrade entry has passed its Until time.
func DowngradeExpired(entry DowngradeEntry, now time.Time) bool {
	until := entry.Until
	if until.IsZero() {
		return false
	}

	return until.Compare(now.UTC()) <= 0
}
