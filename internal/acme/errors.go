package acme

import "errors"

var (
	// ErrRateLimited is returned when the CA rate-limits issuance (HTTP 429 / rateLimited).
	ErrRateLimited = errors.New("ACME rate limited")
	// ErrTimeout is returned when ACME issuance exceeds its wall-clock budget.
	ErrTimeout = errors.New("certificate issue timed out")
	// ErrEmailRequired is returned when an ACME account email is needed but empty.
	ErrEmailRequired = errors.New("ACME email required")
	// ErrEmptyDomain is returned when domain or mode is missing.
	ErrEmptyDomain = errors.New("empty domain")
)

var (
	errEmptyCertificate = errors.New("empty certificate resource")
	errNilContext       = errors.New("nil context")
	errInvalidMode      = errors.New("invalid certificate mode")
)
