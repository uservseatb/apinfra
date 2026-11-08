package nginx

import (
	"context"
	"fmt"
	"log"
	"time"
)

// RoutingService orchestrates nginx conf sync, cert issuance, and proxy reload.
type RoutingService struct {
	fs       IngressFileSystem
	proxy    ProxyController
	dns      DNSResolver
	certs    CertificateIssuer
	timeouts ProcessTimeouts
}

// NewRoutingService wires domain ports. timeouts may be zero (defaults applied per call).
func NewRoutingService(
	ingressFS IngressFileSystem,
	proxy ProxyController,
	dns DNSResolver,
	certs CertificateIssuer,
	timeouts ProcessTimeouts,
) *RoutingService {
	return &RoutingService{
		fs:       ingressFS,
		proxy:    proxy,
		dns:      dns,
		certs:    certs,
		timeouts: timeouts,
	}
}

// EnsureHostFiles writes bind-mounted nginx files before compose.
func (s *RoutingService) EnsureHostFiles() error {
	err := s.fs.EnsureHostFiles()
	if err != nil {
		return fmt.Errorf("ensure host files: %w", err)
	}

	return nil
}

// ProcessRouting generates per-service conf.d files, manages certs, and safely reloads nginx.
func (s *RoutingService) ProcessRouting(ctx context.Context, params RoutingParams) error {
	if ctx == nil {
		return ErrNilContext
	}

	timeouts := s.timeouts.withDefaults()
	s.timeouts = timeouts

	logNginxProcessStart(len(params.Projects), timeouts, params.ForceReload)

	isMainUpdated, err := s.fs.UpdateMainNginxConfig()
	if err != nil {
		return fmt.Errorf("update main nginx config: %w", err)
	}

	err = s.fs.PrepareConfDir()
	if err != nil {
		return fmt.Errorf("prepare conf dir: %w", err)
	}

	validate := s.validateFunc(ctx, params.ProxyRunning)

	now := time.Now().UTC()

	confSnaps := s.fs.SnapshotConfigs(identityToConfFileNames(params.Projects))

	installedByName, err := s.installedCertMap()
	if err != nil {
		return fmt.Errorf("load .installedCert: %w", err)
	}

	certsChanged, err := s.syncNoneCertModes(params.Projects, installedByName)
	if err != nil {
		return err
	}

	anyConfChanged, err := s.syncNginxServiceConfigs(params.Projects, validate)
	if err != nil {
		return err
	}

	canIssue, reloaded, err := s.prepareCertIssuance(
		ctx, params.Projects, installedByName,
		params.ProxyRunning, params.ProxyReady,
		isMainUpdated || anyConfChanged || params.ForceReload, now,
	)
	if err != nil {
		return err
	}

	forceReload := params.ForceReload
	if reloaded {
		// Sync conf is already live for HTTP-01. Clear main/force so the success
		// path does not reload for those; keep anyConfChanged so a cert failure
		// still reloads after rolling confs back to the pre-cycle snapshot.
		isMainUpdated, forceReload = false, false
	}

	if !canIssue {
		return s.finishNginxWithoutCerts(
			ctx, params.Projects, validate, confSnaps, installedByName,
			params.ProxyRunning, isMainUpdated, anyConfChanged, certsChanged, forceReload,
		)
	}

	return s.finishNginxWithCerts(
		ctx, params.Projects, validate, installedByName, confSnaps,
		params.PublicIP, isMainUpdated, anyConfChanged, certsChanged, forceReload,
	)
}

func (s *RoutingService) validateFunc(ctx context.Context, proxyRunning bool) func() error {
	if !proxyRunning {
		log.Printf(
			"[nginx] waiting for %s (not running yet; skip nginx -t until next cycle)",
			proxyContainerName,
		)

		return nil
	}

	log.Printf("[nginx] proxy %s running=true (nginx -t enabled)", proxyContainerName)

	return func() error {
		return s.proxy.TestConfiguration(ctx, s.timeouts.NginxCLI)
	}
}

func (s *RoutingService) installedCertMap() (map[string]CertDescriptor, error) {
	installed, err := s.fs.LoadInstalledCerts()
	if err != nil {
		return nil, fmt.Errorf("load installed certs: %w", err)
	}

	installedByName := make(map[string]CertDescriptor, len(installed))
	for _, c := range installed {
		installedByName[c.DomainName] = c
	}

	return installedByName, nil
}

func logNginxProcessStart(serviceCount int, timeouts ProcessTimeouts, forceReload bool) {
	log.Printf(
		"[nginx] start services=%d certTimeout=%s nginxCLI=%s domainPrecheck=%s forceReload=%v",
		serviceCount, timeouts.CertIssue, timeouts.NginxCLI, timeouts.DomainPrecheck, forceReload,
	)
}
