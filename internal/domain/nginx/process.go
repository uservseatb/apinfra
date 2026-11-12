package nginx

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// prepareCertIssuance decides whether ACME may run this cycle. HTTP-01 needs fresh
// vhosts live in nginx, so pending conf changes are reloaded before issuance.
// reloaded=true means that reload already applied confChanged/main/force flags.
func (s *RoutingService) prepareCertIssuance(
	ctx context.Context,
	projects []Project,
	installedByName map[string]CertDescriptor,
	proxyRunning, proxyReady, reloadPending bool,
	now time.Time,
) (bool, bool, error) {
	needs := s.needsCertIssue(projects, installedByName, now)

	if proxyRunning && reloadPending && needs {
		log.Printf("[nginx] reloading %s before cert issuance (new vhosts must be live for HTTP-01)", proxyContainerName)

		err := s.nginxReload(ctx)
		if err != nil {
			if errors.Is(err, ErrProxyNotReady) {
				return false, false, nil
			}

			return false, false, err
		}

		return true, true, nil
	}

	return proxyReady, false, nil
}

func (s *RoutingService) needsCertIssue(
	projects []Project,
	installedByName map[string]CertDescriptor,
	now time.Time,
) bool {
	return len(s.unresolvedCertIdentities(projects, installedByName, now)) > 0
}

func (s *RoutingService) unresolvedCertIdentities(
	projects []Project,
	installedByName map[string]CertDescriptor,
	now time.Time,
) []string {
	var out []string

	for _, cfg := range projects {
		name := certIdentity(cfg)
		if name == "" || !wantsCertificate(cfg) {
			continue
		}

		mode := s.resolveIssueMode(name, effectiveCertMode(cfg))
		if mode == CertModeNone {
			continue
		}

		c, ok := installedByName[name]
		if !ok || s.certNeedsReissue(c, mode, cfg.CertUpdateSec, now) {
			out = append(out, name)
		}
	}

	return out
}

func (s *RoutingService) finishNginxWithoutCerts(
	ctx context.Context,
	projects []Project,
	validate func() error,
	confSnaps map[string]ConfigSnapshot,
	installedByName map[string]CertDescriptor,
	proxyRunning, isMainUpdated, anyConfChanged, certsChanged, forceReload bool,
) error {
	log.Printf("[nginx] skip cert issuance (apinfra-proxy not ready yet)")

	now := time.Now().UTC()

	unresolved := s.unresolvedCertIdentities(projects, installedByName, now)

	if len(unresolved) > 0 {
		restored := s.fs.RestoreConfigSnapshots(confSnaps, unresolved)
		log.Printf(
			"[nginx] cert unresolved (proxy not ready) for %s; rolled back those service confs",
			strings.Join(unresolved, ", "),
		)

		return s.reloadNginxIfNeeded(ctx, false, restored, false, false)
	}

	pruned, err := s.pruneStaleInstalledCerts(projects, installedByName)
	if err != nil {
		return err
	}

	certsChanged = pruned || certsChanged

	anyConfChanged, err = s.rewriteConfigsIfCertsChanged(projects, validate, anyConfChanged, certsChanged)
	if err != nil {
		return err
	}

	if !proxyRunning {
		log.Printf("[nginx] done (configs only; waiting for %s)", proxyContainerName)

		return nil
	}

	return s.reloadNginxIfNeeded(ctx, isMainUpdated, anyConfChanged, certsChanged, forceReload)
}

func (s *RoutingService) finishNginxWithCerts(
	ctx context.Context,
	projects []Project,
	validate func() error,
	installedByName map[string]CertDescriptor,
	confSnaps map[string]ConfigSnapshot,
	publicIP string,
	isMainUpdated, anyConfChanged, certsChanged, forceReload bool,
) error {
	now := time.Now().UTC()
	s.logUpToDateCerts(projects, installedByName, now)

	issuedChanged, unresolvedNames, err := s.issueNeededCerts(
		ctx, projects, installedByName, confSnaps, publicIP, now, false,
	)
	if err != nil {
		return err
	}

	certsChanged = certsChanged || issuedChanged

	if len(unresolvedNames) > 0 {
		// Domains issued in this cycle get their HTTPS block now; only unresolved ones are rolled back.
		anyConfChanged, err = s.rewriteConfigsIfCertsChanged(projects, validate, anyConfChanged, certsChanged)
		if err != nil {
			return err
		}

		restored := s.fs.RestoreConfigSnapshots(confSnaps, unresolvedNames)
		log.Printf(
			"[nginx] cert unresolved for %s; rolled back those service confs",
			strings.Join(unresolvedNames, ", "),
		)

		return s.reloadNginxIfNeeded(ctx, false, restored || anyConfChanged, false, false)
	}

	pruned, err := s.pruneStaleInstalledCerts(projects, installedByName)
	if err != nil {
		return err
	}

	certsChanged = pruned || certsChanged

	anyConfChanged, err = s.rewriteConfigsIfCertsChanged(projects, validate, anyConfChanged, certsChanged)
	if err != nil {
		return err
	}

	return s.reloadNginxIfNeeded(ctx, isMainUpdated, anyConfChanged, certsChanged, forceReload)
}

func (s *RoutingService) reloadNginxIfNeeded(
	ctx context.Context,
	isMainUpdated, anyConfChanged, certsChanged, forceReload bool,
) error {
	pending, err := s.fs.HasReloadPending()
	if err != nil {
		return fmt.Errorf("reload pending: %w", err)
	}

	if !isMainUpdated && !anyConfChanged && !certsChanged && !forceReload && !pending {
		log.Printf("[nginx] no reload needed")
		log.Printf("[nginx] done")

		return nil
	}

	log.Printf(
		"[nginx] reloading %s (mainUpdated=%v confChanged=%v certsChanged=%v forceReload=%v pending=%v)",
		proxyContainerName, isMainUpdated, anyConfChanged, certsChanged, forceReload, pending,
	)

	err = s.nginxReload(ctx)
	if err != nil {
		if errors.Is(err, ErrProxyNotReady) {
			log.Printf("[nginx] done (waiting for %s)", proxyContainerName)

			return nil
		}

		return err
	}

	log.Printf("[nginx] done")

	return nil
}

func (s *RoutingService) nginxReload(ctx context.Context) error {
	err := s.fs.MarkReloadPending()
	if err != nil {
		return fmt.Errorf("mark reload pending: %w", err)
	}

	err = s.proxy.TestConfiguration(ctx, s.timeouts.NginxCLI)
	if err != nil {
		return fmt.Errorf("nginx test before reload: %w", err)
	}

	err = s.proxy.Reload(ctx, s.timeouts.NginxCLI)
	if err != nil {
		return fmt.Errorf("nginx reload: %w", err)
	}

	err = s.fs.ClearReloadPending()
	if err != nil {
		return fmt.Errorf("clear reload pending: %w", err)
	}

	return nil
}

func (s *RoutingService) logUpToDateCerts(
	projects []Project,
	installedByName map[string]CertDescriptor,
	now time.Time,
) {
	var upToDate []string

	for _, cfg := range projects {
		if label, ok := s.upToDateCertLabel(cfg, installedByName, now); ok {
			upToDate = append(upToDate, label)
		}
	}

	log.Printf("[nginx] up-to-date certs (per-project): %v", upToDate)
}

func (s *RoutingService) upToDateCertLabel(
	cfg Project,
	installedByName map[string]CertDescriptor,
	now time.Time,
) (string, bool) {
	name := certIdentity(cfg)
	if name == "" {
		return "", false
	}

	mode := s.resolveIssueMode(name, effectiveCertMode(cfg))

	cert, ok := installedByName[name]
	if !ok {
		return "", false
	}

	if mode == CertModeNone {
		if cert.Mode == CertModeNone {
			return name + "(none)", true
		}

		return "", false
	}

	if !s.certNeedsReissue(cert, mode, cfg.CertUpdateSec, now) {
		return name + "(" + cert.Mode + ")", true
	}

	return "", false
}
