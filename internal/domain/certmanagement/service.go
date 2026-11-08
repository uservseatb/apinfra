package certmanagement

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"
)

const (
	eventCertRateLimited = "cert_rate_limited"
	eventCertIssueFailed = "cert_issue_failed"

	eventFieldDomain       = "domain"
	eventFieldFrom         = "from"
	eventFieldTo           = "to"
	eventFieldAttempts     = "attempts"
	eventFieldMode         = "mode"
	eventFieldBlockedUntil = "blocked_until"
)

// CertificateService wraps ACME issuance with blocked / downgrade / fallback / retry policy.
type CertificateService struct {
	repo        StateRepository
	acme        AcmeClient
	events      EventRecorder
	retryPolicy CertRetryPolicy
	now         func() time.Time
}

// NewCertificateService builds a CertificateService. events may be nil.
func NewCertificateService(
	repo StateRepository,
	acme AcmeClient,
	events EventRecorder,
	policy CertRetryPolicy,
) *CertificateService {
	return &CertificateService{
		repo:        repo,
		acme:        acme,
		events:      events,
		retryPolicy: EffectiveRetryPolicy(policy),
		now:         time.Now,
	}
}

// ResolveMode returns the mode EnsureCertificate would use for domain right now,
// honouring an active downgrade override.
func (s *CertificateService) ResolveMode(domain, requested string) (string, error) {
	now := s.now().UTC()

	downgrade, err := s.repo.LoadDowngrades(now)
	if err != nil {
		return "", fmt.Errorf("load downgrade state for %s: %w", domain, err)
	}

	return EffectiveMode(requested, domain, downgrade, now), nil
}

// EnsureCertificate ensures PEMs exist for domain in mode (idempotent). Blocked /
// downgrade / fallback CA / retry bookkeeping happen here. CertPaths.Mode is the
// mode actually used, which may be lower than the requested mode while a
// downgrade override is active.
func (s *CertificateService) EnsureCertificate(
	ctx context.Context,
	domain, email, mode string,
	timeout time.Duration,
) (CertPaths, error) {
	paths, err := s.ensure(ctx, domain, email, mode, timeout)
	if err != nil {
		return CertPaths{}, fmt.Errorf("acme issue %s: %w", domain, err)
	}

	return paths, nil
}

func (s *CertificateService) ensure(
	ctx context.Context,
	domain, email, mode string,
	timeout time.Duration,
) (CertPaths, error) {
	err := validateCertRequest(domain, mode)
	if err != nil {
		return CertPaths{}, err
	}

	now := s.now().UTC()

	blocked, err := s.retryBlocked(domain, now)
	if err != nil {
		return CertPaths{}, fmt.Errorf("cert retry state for %s: %w", domain, err)
	}

	if blocked {
		return CertPaths{}, fmt.Errorf("%w: %s (see .retries)", ErrCertBlocked, domain)
	}

	downgrade, err := s.repo.LoadDowngrades(now)
	if err != nil {
		return CertPaths{}, fmt.Errorf("load downgrade state for %s: %w", domain, err)
	}

	effective := EffectiveMode(mode, domain, downgrade, now)
	if effective == ModeNone {
		return CertPaths{}, fmt.Errorf("%w %q for %s (use ResolveMode; none is not issuable)",
			ErrInvalidCertMode, ModeNone, domain)
	}

	err = validateCertRequest(domain, effective)
	if err != nil {
		return CertPaths{}, fmt.Errorf("downgrade override: %w", err)
	}

	if s.acme.PEMsMatchMode(domain, effective) {
		log.Printf(
			"[acme] existing cert domain=%s mode=%s (requested=%s); skip CA",
			domain, effective, mode,
		)
		s.clearRetries(domain, now)

		return s.acme.PathsFor(domain, effective), nil
	}

	if mode != effective {
		log.Printf("[acme] using downgrade domain=%s requested=%s effective=%s", domain, mode, effective)
	}

	paths, err := s.issueFor(ctx, domain, email, effective, timeout, now)
	if err != nil {
		return CertPaths{}, err
	}

	s.clearRetries(domain, now)

	return paths, nil
}

func validateCertRequest(domain, mode string) error {
	if domain == "" {
		return ErrEmptyDomain
	}

	switch mode {
	case "":
		return fmt.Errorf("%w: mode missing for %s", ErrEmptyDomain, domain)
	case ModeProd, ModeTest, ModeMock:
		return nil
	default:
		return fmt.Errorf("%w %q for %s", ErrInvalidCertMode, mode, domain)
	}
}

func (s *CertificateService) issueFor(
	ctx context.Context,
	domain, email, mode string,
	timeout time.Duration,
	now time.Time,
) (CertPaths, error) {
	paths, err := s.obtainWithFallback(ctx, domain, email, mode, timeout)
	if err == nil {
		return paths, nil
	}

	cooldown, limited := s.acme.RateLimitCooldown(err, s.retryPolicy.DowngradeCertModeTTL)
	if limited {
		s.handleRateLimit(domain, mode, now, cooldown)

		if s.acme.IsRateLimited(err) {
			return CertPaths{}, err
		}

		return CertPaths{}, fmt.Errorf("rate limited: %w", err)
	}

	s.handleFailure(domain, mode, now, err)

	return CertPaths{}, err
}

func (s *CertificateService) obtainWithFallback(
	ctx context.Context,
	domain, email, mode string,
	timeout time.Duration,
) (CertPaths, error) {
	log.Printf("[acme] obtain primary domain=%s mode=%s", domain, mode)

	paths, primaryErr := s.acme.Issue(ctx, domain, email, mode, timeout)
	if primaryErr == nil {
		return paths, nil
	}

	if !s.acme.IsRateLimited(primaryErr) {
		return CertPaths{}, fmt.Errorf("primary CA: %w", primaryErr)
	}

	if !s.acme.HasSecondary() {
		log.Printf(
			"[acme] rate limited domain=%s mode=%s; no secondary CA (set certs.zerossl.AcmeKid/Hmac for ZeroSSL fallback)",
			domain, mode,
		)

		return CertPaths{}, fmt.Errorf("primary CA: %w", primaryErr)
	}

	log.Printf("[acme] rate limited domain=%s mode=%s; trying secondary CA", domain, mode)

	paths, secondaryErr := s.acme.IssueSecondary(ctx, domain, email, mode, timeout)

	switch {
	case secondaryErr == nil:
		log.Printf("[acme] secondary CA ok domain=%s mode=%s", domain, mode)

		return paths, nil
	case s.acme.IsRateLimited(secondaryErr):
		log.Printf("[acme] secondary CA also rate limited domain=%s mode=%s", domain, mode)

		return CertPaths{}, fmt.Errorf("secondary CA: %w", secondaryErr)
	default:
		log.Printf(
			"[acme] secondary CA failed domain=%s: %v (will downgrade after both providers)",
			domain, secondaryErr,
		)

		return CertPaths{}, fmt.Errorf("%w (secondary: %w)", primaryErr, secondaryErr)
	}
}

func (s *CertificateService) handleRateLimit(domain, mode string, now time.Time, cooldown time.Duration) {
	next, ok := NextLowerMode(mode)
	if !ok {
		log.Printf("[acme] rate limited domain=%s mode=%s (no lower mode)", domain, mode)
		s.writeRateLimitedEvent(domain, mode, mode)

		return
	}

	until := now.Add(cooldown)

	err := s.setDowngradeMode(domain, next, until, now)
	if err != nil {
		log.Printf("[acme] WARN set .downgradeCertMode for %s: %v", domain, err)
	}

	log.Printf(
		"[acme] rate limited domain=%s downgrade %s -> %s until=%s",
		domain, mode, next, until.Format(CertTimeLayout),
	)
	s.writeRateLimitedEvent(domain, mode, next)
}

func (s *CertificateService) handleFailure(domain, mode string, now time.Time, cause error) {
	log.Printf("[acme] WARN cert issuance failed domain=%s mode=%s: %v", domain, mode, cause)

	attempts, blocked, err := s.bumpRetryAttempt(domain, now)
	if err != nil {
		log.Printf("[acme] WARN bump .retries for %s: %v", domain, err)

		return
	}

	log.Printf("[acme] cert retry domain=%s attempts=%d/%d", domain, attempts, s.retryPolicy.TriesCount)

	if !blocked {
		return
	}

	blockedUntil := now.Add(s.retryPolicy.BlockDuration).Format(CertTimeLayout)
	log.Printf("[acme] cert issue blocked domain=%s after %d attempts until=%s", domain, attempts, blockedUntil)

	s.writeEvent(eventCertIssueFailed, map[string]string{
		eventFieldDomain:       domain,
		eventFieldAttempts:     strconv.Itoa(attempts),
		eventFieldMode:         mode,
		eventFieldBlockedUntil: blockedUntil,
	})
}

func (s *CertificateService) writeRateLimitedEvent(domain, from, to string) {
	s.writeEvent(eventCertRateLimited, map[string]string{
		eventFieldDomain: domain,
		eventFieldFrom:   from,
		eventFieldTo:     to,
	})
}

func (s *CertificateService) writeEvent(kind string, fields map[string]string) {
	if s.events == nil {
		log.Printf("[acme] WARN skip event kind=%s domain=%s (events store nil)", kind, fields[eventFieldDomain])

		return
	}

	err := s.events.Write(kind, fields)
	if err != nil {
		log.Printf("[acme] WARN write %s event for %s: %v", kind, fields[eventFieldDomain], err)

		return
	}

	log.Printf("[acme] event written kind=%s domain=%s", kind, fields[eventFieldDomain])
}

func (s *CertificateService) retryBlocked(domain string, now time.Time) (bool, error) {
	file, err := s.repo.LoadRetries()
	if err != nil {
		return false, fmt.Errorf("load cert retries: %w", err)
	}

	entry, ok := file[domain]
	if !ok || !entry.Blocked {
		return false, nil
	}

	if entry.BlockedUntil.IsZero() {
		// Legacy permanent block without until: treat as still blocked.
		return true, nil
	}

	if entry.BlockedUntil.After(now.UTC()) {
		return true, nil
	}

	delete(file, domain)

	err = s.repo.SaveRetries(file)
	if err != nil {
		return false, fmt.Errorf("clear expired cert retry for %s: %w", domain, err)
	}

	return false, nil
}

func (s *CertificateService) clearRetries(domain string, now time.Time) {
	err := s.clearRetryAttempts(domain, now)
	if err != nil {
		log.Printf("[acme] WARN clear .retries for %s: %v", domain, err)
	}
}

func (s *CertificateService) clearRetryAttempts(domain string, now time.Time) error {
	file, err := s.repo.LoadRetries()
	if err != nil {
		return fmt.Errorf("load cert retries: %w", err)
	}

	entry, ok := file[domain]
	if !ok {
		return nil
	}

	if entry.Blocked {
		until := entry.BlockedUntil
		if until.IsZero() || until.After(now.UTC()) {
			// Active blocked entries are cleared only by expiry or manual delete.
			return nil
		}
	}

	delete(file, domain)

	err = s.repo.SaveRetries(file)
	if err != nil {
		return fmt.Errorf("save cert retries: %w", err)
	}

	return nil
}

func (s *CertificateService) bumpRetryAttempt(domain string, now time.Time) (int, bool, error) {
	policy := EffectiveRetryPolicy(s.retryPolicy)

	file, err := s.repo.LoadRetries()
	if err != nil {
		return 0, false, fmt.Errorf("load cert retries: %w", err)
	}

	entry := file[domain]
	entry.Attempts++

	if entry.Attempts >= policy.TriesCount {
		entry.Blocked = true
		entry.BlockedUntil = now.UTC().Add(policy.BlockDuration)
	}

	file[domain] = entry

	err = s.repo.SaveRetries(file)
	if err != nil {
		return 0, false, fmt.Errorf("save cert retries: %w", err)
	}

	return entry.Attempts, entry.Blocked, nil
}

func (s *CertificateService) setDowngradeMode(domain, mode string, until, now time.Time) error {
	modes, err := s.repo.LoadDowngrades(now)
	if err != nil {
		return fmt.Errorf("load cert downgrades: %w", err)
	}

	modes[domain] = DowngradeEntry{Mode: mode, Until: until.UTC()}

	err = s.repo.SaveDowngrades(modes)
	if err != nil {
		return fmt.Errorf("save cert downgrades: %w", err)
	}

	return nil
}
