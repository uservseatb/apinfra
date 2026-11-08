package acme

import (
	"fmt"

	"github.com/go-acme/lego/v4/registration"
)

// zeroSSLProvider uses ZeroSSL's ACME directory and EAB registration when credentials exist.
// See https://zerossl.com/documentation/acme/
type zeroSSLProvider struct {
	kid       string
	hmac      string
	directory string
}

func (p *zeroSSLProvider) directoryURL(mode string) string {
	_ = mode // ZeroSSL has no public staging directory.

	return p.directory
}

func (p *zeroSSLProvider) register(reg accountRegistrar) (*registration.Resource, error) {
	if p.kid == "" || p.hmac == "" {
		res, err := reg.Register(registration.RegisterOptions{
			TermsOfServiceAgreed: true,
		})
		if err != nil {
			return nil, fmt.Errorf("zerossl register: %w", err)
		}

		return res, nil
	}

	res, err := reg.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{
		TermsOfServiceAgreed: true,
		Kid:                  p.kid,
		HmacEncoded:          p.hmac,
	})
	if err != nil {
		return nil, fmt.Errorf("zerossl register EAB: %w", err)
	}

	return res, nil
}
