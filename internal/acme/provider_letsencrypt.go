package acme

import (
	"fmt"

	"github.com/go-acme/lego/v4/registration"
)

// letsEncryptProvider uses Let's Encrypt production or staging directories.
type letsEncryptProvider struct {
	prod    string
	staging string
}

func (p *letsEncryptProvider) directoryURL(mode string) string {
	if mode == ModeTest {
		return p.staging
	}

	return p.prod
}

func (p *letsEncryptProvider) register(reg accountRegistrar) (*registration.Resource, error) {
	res, err := reg.Register(registration.RegisterOptions{
		TermsOfServiceAgreed: true,
	})
	if err != nil {
		return nil, fmt.Errorf("letsencrypt register: %w", err)
	}

	return res, nil
}
