package nginx

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"
)

// certNeedsReissue reports whether a cert must be (re)issued for the current mode/ttl.
func (s *RoutingService) certNeedsReissue(
	cert CertDescriptor,
	getCertMode string,
	sslUpdateSec int64,
	now time.Time,
) bool {
	if getCertMode == CertModeNone {
		return false
	}

	if cert.Mode == "" || cert.Mode != getCertMode {
		return true
	}

	if !s.fs.CertFilesPresent(cert.DomainName, cert.Mode) {
		return true
	}

	expiresAt := cert.IssueDateTime.Add(time.Duration(sslUpdateSec) * time.Second)

	return expiresAt.Compare(now) <= 0
}

func (s *RoutingService) pruneStaleInstalledCerts(
	projects []Project,
	installedByName map[string]CertDescriptor,
) (bool, error) {
	desired := desiredCertIdentities(projects)

	existing, err := s.fs.LoadInstalledCerts()
	if err != nil {
		return false, fmt.Errorf("load installed certs: %w", err)
	}

	kept := make([]CertDescriptor, 0, len(existing))
	removed := make([]string, 0)

	for _, cert := range existing {
		if _, ok := desired[cert.DomainName]; ok {
			kept = append(kept, cert)

			continue
		}

		removed = append(removed, cert.DomainName)
		delete(installedByName, cert.DomainName)
		s.fs.RemoveCertMaterial(cert.DomainName)
	}

	if len(removed) == 0 {
		return false, nil
	}

	slices.Sort(removed)
	log.Printf("[nginx] pruning installed certs not in settings: %s", strings.Join(removed, ", "))

	changed, err := s.fs.SaveInstalledCerts(kept)
	if err != nil {
		return false, fmt.Errorf("write .installedCert after prune: %w", err)
	}

	return changed, nil
}

func desiredCertIdentities(projects []Project) map[string]struct{} {
	desired := make(map[string]struct{}, len(projects))

	for _, cfg := range projects {
		name := certIdentity(cfg)
		if name == "" {
			continue
		}

		desired[name] = struct{}{}
	}

	return desired
}

func (s *RoutingService) issueNeededCerts(
	ctx context.Context,
	projects []Project,
	installedByName map[string]CertDescriptor,
	confSnaps map[string]ConfigSnapshot,
	publicIP string,
	now time.Time,
	certsChanged bool,
) (bool, []string, error) {
	var unresolved []string

	for _, cfg := range projects {
		changed, unresolvedName, err := s.issueOneNeededCert(
			ctx, cfg, installedByName, confSnaps, publicIP, now,
		)
		if err != nil {
			return certsChanged, unresolved, err
		}

		if changed {
			certsChanged = true
		}

		if unresolvedName != "" {
			unresolved = append(unresolved, unresolvedName)
		}
	}

	return certsChanged, unresolved, nil
}

// issueOneNeededCert returns (certsChanged, unresolvedIdentity).
func (s *RoutingService) issueOneNeededCert(
	ctx context.Context,
	cfg Project,
	installedByName map[string]CertDescriptor,
	confSnaps map[string]ConfigSnapshot,
	publicIP string,
	now time.Time,
) (bool, string, error) {
	name := certIdentity(cfg)
	if name == "" || !wantsCertificate(cfg) {
		return false, "", nil
	}

	requested := effectiveCertMode(cfg)
	if requested == CertModeNone {
		return false, "", nil
	}

	mode := s.resolveIssueMode(name, requested)
	if mode == CertModeNone {
		return s.applyDowngradeNoneMode(name, installedByName)
	}

	installed, hasInstalled := installedByName[name]
	if hasInstalled && !s.certNeedsReissue(installed, mode, cfg.CertUpdateSec, now) {
		return false, "", nil
	}

	if !s.dnsPreflightOK(ctx, name, mode, publicIP) {
		return false, name, nil
	}

	logCertIssueStart(name, mode, installedByName)

	installedFresh := hasInstalled && installed.Mode != "" &&
		!s.certNeedsReissue(installed, installed.Mode, cfg.CertUpdateSec, now)

	issued, err := s.issueViaIssuer(ctx, cfg, name, requested, mode, installed, hasInstalled)
	if err != nil {
		log.Printf("[nginx] WARN cert issuance failed name=%s mode=%s: %v", name, mode, err)

		s.fs.RestoreConfigSnapshots(confSnaps, []string{name})

		return false, name, nil
	}

	if installedFresh && installed.Mode == issued.Mode {
		return false, "", nil
	}

	_, err = s.recordIssuedCert(name, issued.Mode)
	if err != nil {
		return false, "", err
	}

	log.Printf("[nginx] cert issued name=%s mode=%s", name, issued.Mode)

	installedByName[name] = CertDescriptor{
		DomainName:    name,
		IssueDateTime: now,
		Mode:          issued.Mode,
	}

	return true, "", nil
}

func (s *RoutingService) resolveIssueMode(name, requested string) string {
	if requested == CertModeNone {
		return requested
	}

	mode, err := s.certs.ResolveMode(name, requested)
	if err != nil {
		log.Printf("[nginx] WARN resolve cert mode name=%s: %v (using requested=%s)", name, err, requested)

		return requested
	}

	return mode
}

// applyDowngradeNoneMode records .installedCert as none when an active rate-limit
// downgrade overrides settings to plain HTTP. Skips ACME; conf rewrite uses confCertMode.
func (s *RoutingService) applyDowngradeNoneMode(
	name string,
	installedByName map[string]CertDescriptor,
) (bool, string, error) {
	if c, exists := installedByName[name]; exists && c.Mode == CertModeNone {
		return false, "", nil
	}

	log.Printf(
		"[nginx] applying downgrade mode=none name=%s (%s -> none)",
		name, previousCertModeLabel(installedByName, name),
	)

	changed, err := s.recordInstalledCertMode(name, CertModeNone, true)
	if err != nil {
		return false, "", fmt.Errorf("downgrade mode=none for %s: %w", name, err)
	}

	if !changed {
		return false, "", nil
	}

	installedByName[name] = CertDescriptor{
		DomainName:    name,
		IssueDateTime: preservedCertIssueTime(installedByName, name),
		Mode:          CertModeNone,
	}

	return true, "", nil
}

// issueViaIssuer stashes PEMs for the effective mode and asks the issuer with
// the settings-requested mode so CertificateService can apply / log downgrades.
func (s *RoutingService) issueViaIssuer(
	ctx context.Context,
	cfg Project,
	name, requested, effective string,
	installed CertDescriptor,
	hasInstalled bool,
) (IssuedCertificate, error) {
	finish := func(bool) {}
	if hasInstalled && installed.Mode == effective {
		finish = s.fs.StashCertDir(name, effective)
	}

	issued, err := s.certs.EnsureCertificate(ctx, name, cfg.CertAcmeEmail, requested, s.timeouts.CertIssue)
	if err != nil {
		finish(false)

		return IssuedCertificate{}, fmt.Errorf("issue cert for %s: %w", name, err)
	}

	finish(true)

	return issued, nil
}

func logCertIssueStart(name, mode string, installedByName map[string]CertDescriptor) {
	if c, ok := installedByName[name]; ok && c.Mode != "" && c.Mode != mode {
		log.Printf("[nginx] reissuing cert name=%s (mode %s -> %s)", name, c.Mode, mode)

		return
	}

	log.Printf("[nginx] issuing cert for name=%s mode=%s", name, mode)
}

func (s *RoutingService) syncNoneCertModes(
	projects []Project,
	installedByName map[string]CertDescriptor,
) (bool, error) {
	changed := false

	for _, cfg := range projects {
		oneChanged, err := s.syncOneNoneCertMode(cfg, installedByName)
		if err != nil {
			return changed, err
		}

		if oneChanged {
			changed = true
		}
	}

	return changed, nil
}

func (s *RoutingService) syncOneNoneCertMode(
	cfg Project,
	installedByName map[string]CertDescriptor,
) (bool, error) {
	name := certIdentity(cfg)
	if name == "" || effectiveCertMode(cfg) != CertModeNone {
		return false, nil
	}

	if c, exists := installedByName[name]; exists && c.Mode == CertModeNone {
		return false, nil
	}

	log.Printf("[nginx] setting cert mode name=%s (%s -> none)", name, previousCertModeLabel(installedByName, name))

	changed, err := s.recordInstalledCertMode(name, CertModeNone, true)
	if err != nil {
		return false, fmt.Errorf("sync mode=none for %s: %w", name, err)
	}

	if !changed {
		return false, nil
	}

	installedByName[name] = CertDescriptor{
		DomainName:    name,
		IssueDateTime: preservedCertIssueTime(installedByName, name),
		Mode:          CertModeNone,
	}

	return true, nil
}

func previousCertModeLabel(installedByName map[string]CertDescriptor, name string) string {
	c, exists := installedByName[name]
	if !exists || c.Mode == "" {
		return "absent"
	}

	return c.Mode
}

func preservedCertIssueTime(installedByName map[string]CertDescriptor, name string) time.Time {
	c, exists := installedByName[name]
	if exists && !c.IssueDateTime.IsZero() {
		return c.IssueDateTime
	}

	return time.Now().UTC()
}

func (s *RoutingService) recordInstalledCertMode(domainName, mode string, preserveTime bool) (bool, error) {
	if domainName == "" || mode == "" {
		return false, nil
	}

	existing, err := s.fs.LoadInstalledCerts()
	if err != nil {
		return false, fmt.Errorf("load installed certs: %w", err)
	}

	now := time.Now().UTC()
	issueTime := now

	updated := make([]CertDescriptor, 0, len(existing)+1)

	for _, cert := range existing {
		if cert.DomainName != domainName {
			updated = append(updated, cert)

			continue
		}

		if cert.Mode == mode {
			return false, nil
		}

		if preserveTime && !cert.IssueDateTime.IsZero() {
			issueTime = cert.IssueDateTime
		}
	}

	updated = append(updated, CertDescriptor{
		DomainName:    domainName,
		IssueDateTime: issueTime,
		Mode:          mode,
	})

	changed, err := s.fs.SaveInstalledCerts(updated)
	if err != nil {
		return false, fmt.Errorf("recordInstalledCertMode domain=%s mode=%s: %w", domainName, mode, err)
	}

	return changed, nil
}

func (s *RoutingService) recordIssuedCert(domain, mode string) (bool, error) {
	existing, err := s.fs.LoadInstalledCerts()
	if err != nil {
		return false, fmt.Errorf("load installed certs: %w", err)
	}

	now := time.Now().UTC()
	updated := make([]CertDescriptor, 0, len(existing)+1)

	for _, cert := range existing {
		if cert.DomainName != domain {
			updated = append(updated, cert)
		}
	}

	updated = append(updated, CertDescriptor{
		DomainName:    domain,
		IssueDateTime: now,
		Mode:          mode,
	})

	changed, err := s.fs.SaveInstalledCerts(updated)
	if err != nil {
		return false, fmt.Errorf("record issued cert domain=%s mode=%s: %w", domain, mode, err)
	}

	return changed, nil
}

func wantsCertificate(cfg Project) bool {
	mode := effectiveCertMode(cfg)

	return mode != CertModeNone && certIdentity(cfg) != ""
}
