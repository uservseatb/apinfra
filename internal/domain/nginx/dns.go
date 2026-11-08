package nginx

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
)

func (s *RoutingService) dnsPreflightOK(ctx context.Context, name, mode, publicIP string) bool {
	if !needsDNSPreflight(name, mode) {
		return true
	}

	err := s.preflightDNSCheck(ctx, name, publicIP)
	if err != nil {
		log.Printf("[nginx] skip cert issue name=%s (DNS preflight): %v", name, err)

		return false
	}

	if publicIP == "" {
		log.Printf("[nginx] DNS preflight ok name=%s (lookup)", name)
	} else {
		log.Printf("[nginx] DNS preflight ok name=%s ip=%s", name, publicIP)
	}

	return true
}

// preflightDNSCheck resolves domain via DNSResolver and optionally checks PublicIP.
func (s *RoutingService) preflightDNSCheck(ctx context.Context, domain, expectedIP string) error {
	if domain == "" {
		return ErrEmptyDomain
	}

	if strings.ContainsAny(domain, " \t\n;&|$`\"'\\") {
		return fmt.Errorf("%w: unsafe domain %q", ErrDNSPreflightFailed, domain)
	}

	ips, err := s.dns.LookupShortIPs(ctx, domain, s.timeouts.DomainPrecheck)
	if err != nil {
		return fmt.Errorf("%w: lookup %s: %w", ErrDNSPreflightFailed, domain, err)
	}

	if len(ips) == 0 {
		return fmt.Errorf("%w: lookup %s returned no IPs", ErrDNSPreflightFailed, domain)
	}

	if expectedIP == "" {
		return nil
	}

	if slices.Contains(ips, expectedIP) {
		return nil
	}

	return fmt.Errorf("%w: domain %s resolves to %v, expected %s", ErrDNSPreflightFailed, domain, ips, expectedIP)
}

// needsDNSPreflight is true for hostname ACME modes (prod/test), not mock or port certs.
func needsDNSPreflight(domainName, mode string) bool {
	return (mode == CertModeProd || mode == CertModeTest) &&
		domainName != "" &&
		!strings.HasPrefix(domainName, "port-")
}
