package acme

import (
	"strings"

	"github.com/go-acme/lego/v4/registration"
)

const (
	providerLetsEncrypt = "letsencrypt"
	providerZeroSSL     = "zerossl"

	defaultLetsEncryptProd    = "https://acme-v02.api.letsencrypt.org/directory"
	defaultLetsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
	defaultZeroSSL            = "https://acme.zerossl.com/v2/DV90"
)

// Directories holds ACME directory URLs; empty fields fall back to public defaults.
type Directories struct {
	LetsEncryptProd    string
	LetsEncryptStaging string
	ZeroSSL            string
}

func effectiveDirectories(dirs Directories) Directories {
	if dirs.LetsEncryptProd == "" {
		dirs.LetsEncryptProd = defaultLetsEncryptProd
	}

	if dirs.LetsEncryptStaging == "" {
		dirs.LetsEncryptStaging = defaultLetsEncryptStaging
	}

	if dirs.ZeroSSL == "" {
		dirs.ZeroSSL = defaultZeroSSL
	}

	return dirs
}

// provider selects the ACME directory URL and account registration strategy.
type provider interface {
	directoryURL(mode string) string
	register(reg accountRegistrar) (*registration.Resource, error)
}

// accountRegistrar is the lego registration surface used by provider adapters.
type accountRegistrar interface {
	Register(opts registration.RegisterOptions) (*registration.Resource, error)
	RegisterWithExternalAccountBinding(opts registration.RegisterEABOptions) (*registration.Resource, error)
}

// providerPair is the preferred CA plus an optional fallback used before mode downgrade.
type providerPair struct {
	primary    provider
	fallback   provider // nil when only one CA is configured
	primaryID  string
	fallbackID string
}

// newProvider returns the adapter for certs.provider (letsencrypt|zerossl).
// Unknown or empty names default to Let's Encrypt.
//
//nolint:ireturn // factory must return the provider interface
func newProvider(providerName, kid, hmac string, dirs Directories) provider {
	dirs = effectiveDirectories(dirs)

	switch strings.ToLower(strings.TrimSpace(providerName)) {
	case providerZeroSSL:
		return &zeroSSLProvider{kid: kid, hmac: hmac, directory: dirs.ZeroSSL}
	default:
		return &letsEncryptProvider{prod: dirs.LetsEncryptProd, staging: dirs.LetsEncryptStaging}
	}
}

// newProviderPair builds primary + optional fallback.
// Fallback is enabled when ZeroSSL EAB credentials are present (both CAs usable).
func newProviderPair(providerName, kid, hmac string, dirs Directories) providerPair {
	primaryName := strings.ToLower(strings.TrimSpace(providerName))
	if primaryName == "" {
		primaryName = providerLetsEncrypt
	}

	pair := providerPair{
		primary:    newProvider(primaryName, kid, hmac, dirs),
		fallback:   nil,
		primaryID:  primaryName,
		fallbackID: "",
	}

	if kid == "" || hmac == "" {
		return pair
	}

	fallbackName := providerZeroSSL
	if primaryName == providerZeroSSL {
		fallbackName = providerLetsEncrypt
	}

	pair.fallback = newProvider(fallbackName, kid, hmac, dirs)
	pair.fallbackID = fallbackName

	return pair
}
