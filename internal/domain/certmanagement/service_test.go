package certmanagement_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/domain/certmanagement"
	"github.com/uservseatb/apinfra/internal/events"
	"github.com/uservseatb/apinfra/internal/infrastructure/adapters"
	"github.com/uservseatb/apinfra/internal/sys"
)

const (
	testDomain  = "app.example.com"
	testEmail   = "ops@example.com"
	testTimeout = time.Second

	testTries                 = 3
	testBlockHours            = 12
	testDowngradeCertModeTTLH = 6
)

var (
	errTestRateLimited = errors.New("test: rate limited")
	errTestBoom        = errors.New("test: boom")
)

// fakeAcme is a state-based CA stand-in: successful issues are remembered as
// "PEMs on disk", so PEMsMatchMode reflects what was issued.
type fakeAcme struct {
	primary      func(mode string) error
	secondary    func(mode string) error
	hasSecondary bool

	issued         map[string]string
	primaryModes   []string
	secondaryModes []string
}

func newFakeAcme() *fakeAcme {
	return &fakeAcme{issued: map[string]string{}}
}

func (f *fakeAcme) Issue(_ context.Context, domain, _, mode string, _ time.Duration) (certmanagement.CertPaths, error) {
	f.primaryModes = append(f.primaryModes, mode)

	return finishIssue(f, f.primary, domain, mode)
}

func (f *fakeAcme) IssueSecondary(
	_ context.Context, domain, _, mode string, _ time.Duration,
) (certmanagement.CertPaths, error) {
	f.secondaryModes = append(f.secondaryModes, mode)

	return finishIssue(f, f.secondary, domain, mode)
}

func finishIssue(fake *fakeAcme, script func(string) error, domain, mode string) (certmanagement.CertPaths, error) {
	if script != nil {
		err := script(mode)
		if err != nil {
			return certmanagement.CertPaths{}, err
		}
	}

	fake.issued[domain] = mode

	return fake.PathsFor(domain, mode), nil
}

func (f *fakeAcme) HasSecondary() bool { return f.hasSecondary }

func (f *fakeAcme) PEMsMatchMode(domain, mode string) bool { return f.issued[domain] == mode }

func (f *fakeAcme) PathsFor(domain, mode string) certmanagement.CertPaths {
	return certmanagement.CertPaths{
		Fullchain: "/pems/" + domain + "/fullchain.pem",
		Privkey:   "/pems/" + domain + "/privkey.pem",
		Mode:      mode,
	}
}

func (f *fakeAcme) IsRateLimited(err error) bool { return errors.Is(err, errTestRateLimited) }

func (f *fakeAcme) RateLimitCooldown(err error, ttl time.Duration) (time.Duration, bool) {
	if !f.IsRateLimited(err) {
		return 0, false
	}

	return ttl, true
}

func alwaysRateLimited(string) error { return errTestRateLimited }

func alwaysFails(string) error { return errTestBoom }

type certEnv struct {
	svc        *certmanagement.CertificateService
	repo       *adapters.YamlStateRepository
	acme       *fakeAcme
	eventsPath string
}

func newCertEnv(t *testing.T, acme *fakeAcme) *certEnv {
	t.Helper()

	home := t.TempDir()
	infra := filepath.Join(home, "infra")
	eventsPath := filepath.Join(home, ".events")
	repo := adapters.NewYamlStateRepository(infra, sys.FileWriter{})

	svc := certmanagement.NewCertificateService(
		repo,
		acme,
		events.NewFileStore(eventsPath),
		certmanagement.CertRetryPolicy{
			TriesCount:           testTries,
			BlockDuration:        testBlockHours * time.Hour,
			DowngradeCertModeTTL: testDowngradeCertModeTTLH * time.Hour,
		},
	)

	return &certEnv{svc: svc, repo: repo, acme: acme, eventsPath: eventsPath}
}

func (e *certEnv) ensure(t *testing.T, mode string) (certmanagement.CertPaths, error) {
	t.Helper()

	paths, err := e.svc.EnsureCertificate(context.Background(), testDomain, testEmail, mode, testTimeout)
	if err != nil {
		return paths, fmt.Errorf("ensure certificate: %w", err)
	}

	return paths, nil
}

func (e *certEnv) downgrades(t *testing.T) map[string]certmanagement.DowngradeEntry {
	t.Helper()

	got, err := e.repo.LoadDowngrades(time.Time{})
	if err != nil {
		t.Fatalf("load downgrades: %v", err)
	}

	return got
}

func (e *certEnv) retries(t *testing.T) map[string]certmanagement.RetryEntry {
	t.Helper()

	got, err := e.repo.LoadRetries()
	if err != nil {
		t.Fatalf("load retries: %v", err)
	}

	return got
}

func (e *certEnv) events(t *testing.T) []map[string]string {
	t.Helper()

	file, err := os.Open(e.eventsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		t.Fatalf("open events: %v", err)
	}

	defer func() { _ = file.Close() }()

	var out []map[string]string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry map[string]string

		err = json.Unmarshal(scanner.Bytes(), &entry)
		if err != nil {
			t.Fatalf("decode event %q: %v", scanner.Text(), err)
		}

		out = append(out, entry)
	}

	return out
}

func eventKinds(evs []map[string]string) []string {
	kinds := make([]string, 0, len(evs))
	for _, ev := range evs {
		kinds = append(kinds, ev["kind"])
	}

	return kinds
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func TestEnsureCertificate_Outcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		requested     string
		acme          func() *fakeAcme
		wantErr       error
		wantMode      string
		wantPrimary   []string
		wantSecondary []string
		wantDowngrade string // "" = no downgrade entry expected
		wantEvents    []string
	}{
		{
			name:        "primary_ok_prod",
			requested:   certmanagement.ModeProd,
			acme:        newFakeAcme,
			wantMode:    certmanagement.ModeProd,
			wantPrimary: []string{certmanagement.ModeProd},
		},
		{
			name:        "primary_ok_test",
			requested:   certmanagement.ModeTest,
			acme:        newFakeAcme,
			wantMode:    certmanagement.ModeTest,
			wantPrimary: []string{certmanagement.ModeTest},
		},
		{
			name:        "primary_ok_mock",
			requested:   certmanagement.ModeMock,
			acme:        newFakeAcme,
			wantMode:    certmanagement.ModeMock,
			wantPrimary: []string{certmanagement.ModeMock},
		},
		{
			name:      "invalid_mode_rejected",
			requested: "staging",
			acme:      newFakeAcme,
			wantErr:   certmanagement.ErrInvalidCertMode,
		},
		{
			name:      "empty_mode_rejected",
			requested: "",
			acme:      newFakeAcme,
			wantErr:   certmanagement.ErrEmptyDomain,
		},
		{
			name:      "rate_limit_secondary_ok_keeps_mode_no_downgrade",
			requested: certmanagement.ModeProd,
			acme: func() *fakeAcme {
				f := newFakeAcme()
				f.primary = alwaysRateLimited
				f.hasSecondary = true

				return f
			},
			wantMode:      certmanagement.ModeProd,
			wantPrimary:   []string{certmanagement.ModeProd},
			wantSecondary: []string{certmanagement.ModeProd},
		},
		{
			name:      "rate_limit_no_secondary_downgrades_prod_to_test",
			requested: certmanagement.ModeProd,
			acme: func() *fakeAcme {
				f := newFakeAcme()
				f.primary = alwaysRateLimited

				return f
			},
			wantErr:       errTestRateLimited,
			wantPrimary:   []string{certmanagement.ModeProd},
			wantDowngrade: certmanagement.ModeTest,
			wantEvents:    []string{"cert_rate_limited"},
		},
		{
			name:      "rate_limit_both_cas_downgrades",
			requested: certmanagement.ModeProd,
			acme: func() *fakeAcme {
				f := newFakeAcme()
				f.primary = alwaysRateLimited
				f.secondary = alwaysRateLimited
				f.hasSecondary = true

				return f
			},
			wantErr:       errTestRateLimited,
			wantPrimary:   []string{certmanagement.ModeProd},
			wantSecondary: []string{certmanagement.ModeProd},
			wantDowngrade: certmanagement.ModeTest,
			wantEvents:    []string{"cert_rate_limited"},
		},
		{
			name:      "rate_limit_secondary_other_error_still_downgrades",
			requested: certmanagement.ModeProd,
			acme: func() *fakeAcme {
				f := newFakeAcme()
				f.primary = alwaysRateLimited
				f.secondary = alwaysFails
				f.hasSecondary = true

				return f
			},
			wantErr:       errTestRateLimited,
			wantPrimary:   []string{certmanagement.ModeProd},
			wantSecondary: []string{certmanagement.ModeProd},
			wantDowngrade: certmanagement.ModeTest,
			wantEvents:    []string{"cert_rate_limited"},
		},
		{
			name:      "rate_limit_test_downgrades_to_mock",
			requested: certmanagement.ModeTest,
			acme: func() *fakeAcme {
				f := newFakeAcme()
				f.primary = alwaysRateLimited

				return f
			},
			wantErr:       errTestRateLimited,
			wantPrimary:   []string{certmanagement.ModeTest},
			wantDowngrade: certmanagement.ModeMock,
			wantEvents:    []string{"cert_rate_limited"},
		},
		{
			name:      "rate_limit_mock_downgrades_to_none",
			requested: certmanagement.ModeMock,
			acme: func() *fakeAcme {
				f := newFakeAcme()
				f.primary = alwaysRateLimited

				return f
			},
			wantErr:       errTestRateLimited,
			wantPrimary:   []string{certmanagement.ModeMock},
			wantDowngrade: certmanagement.ModeNone,
			wantEvents:    []string{"cert_rate_limited"},
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newCertEnv(t, tcase.acme())
			started := time.Now()

			paths, err := env.ensure(t, tcase.requested)

			if tcase.wantErr != nil {
				if !errors.Is(err, tcase.wantErr) {
					t.Fatalf("err = %v, want %v", err, tcase.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}

				if paths.Mode != tcase.wantMode {
					t.Fatalf("paths.Mode = %q, want %q", paths.Mode, tcase.wantMode)
				}
			}

			if !equalStrings(env.acme.primaryModes, tcase.wantPrimary) {
				t.Fatalf("primary modes = %v, want %v", env.acme.primaryModes, tcase.wantPrimary)
			}

			if !equalStrings(env.acme.secondaryModes, tcase.wantSecondary) {
				t.Fatalf("secondary modes = %v, want %v", env.acme.secondaryModes, tcase.wantSecondary)
			}

			downgrades := env.downgrades(t)
			if tcase.wantDowngrade == "" {
				if len(downgrades) != 0 {
					t.Fatalf("unexpected downgrades: %+v", downgrades)
				}
			} else {
				entry, ok := downgrades[testDomain]
				if !ok || entry.Mode != tcase.wantDowngrade {
					t.Fatalf("downgrade = %+v, want mode %q", entry, tcase.wantDowngrade)
				}

				wantUntil := started.Add(testDowngradeCertModeTTLH * time.Hour)
				if d := entry.Until.Sub(wantUntil); d < -time.Minute || d > time.Minute {
					t.Fatalf("downgrade until = %v, want ~%v", entry.Until, wantUntil)
				}
			}

			if got := eventKinds(env.events(t)); !equalStrings(got, tcase.wantEvents) {
				t.Fatalf("events = %v, want %v", got, tcase.wantEvents)
			}

			if len(env.retries(t)) != 0 && tcase.wantErr != nil && errors.Is(tcase.wantErr, errTestRateLimited) {
				t.Fatalf("rate limit must not bump .retries: %+v", env.retries(t))
			}
		})
	}
}

func TestEnsureCertificate_RateLimitEventFields(t *testing.T) {
	t.Parallel()

	acme := newFakeAcme()
	acme.primary = alwaysRateLimited
	env := newCertEnv(t, acme)

	_, err := env.ensure(t, certmanagement.ModeProd)
	if !errors.Is(err, errTestRateLimited) {
		t.Fatalf("err = %v", err)
	}

	evs := env.events(t)
	if len(evs) != 1 {
		t.Fatalf("events = %v", evs)
	}

	want := map[string]string{"kind": "cert_rate_limited", "domain": testDomain, "from": "prod", "to": "test"}
	for key, val := range want {
		if evs[0][key] != val {
			t.Fatalf("event[%s] = %q, want %q (event=%v)", key, evs[0][key], val, evs[0])
		}
	}
}

func TestEnsureCertificate_RateLimitMockEventFields(t *testing.T) {
	t.Parallel()

	acme := newFakeAcme()
	acme.primary = alwaysRateLimited
	env := newCertEnv(t, acme)

	_, err := env.ensure(t, certmanagement.ModeMock)
	if !errors.Is(err, errTestRateLimited) {
		t.Fatalf("err = %v", err)
	}

	evs := env.events(t)
	if len(evs) != 1 {
		t.Fatalf("events = %v", evs)
	}

	want := map[string]string{"kind": "cert_rate_limited", "domain": testDomain, "from": "mock", "to": "none"}
	for key, val := range want {
		if evs[0][key] != val {
			t.Fatalf("event[%s] = %q, want %q (event=%v)", key, evs[0][key], val, evs[0])
		}
	}
}

func TestEnsureCertificate_DowngradeLifecycle(t *testing.T) {
	t.Parallel()

	acme := newFakeAcme()
	acme.primary = func(mode string) error {
		if mode == certmanagement.ModeProd {
			return errTestRateLimited
		}

		return nil
	}
	env := newCertEnv(t, acme)

	// Cycle 1: prod hits the rate limit and schedules prod -> test.
	_, err := env.ensure(t, certmanagement.ModeProd)
	if !errors.Is(err, errTestRateLimited) {
		t.Fatalf("cycle 1 err = %v", err)
	}

	// ResolveMode reflects the active override while settings still say prod.
	got, err := env.svc.ResolveMode(testDomain, certmanagement.ModeProd)
	if err != nil || got != certmanagement.ModeTest {
		t.Fatalf("ResolveMode = (%q, %v), want test", got, err)
	}

	// Cycle 2: same settings (prod) issues the downgraded test cert, no CA call in prod.
	paths, err := env.ensure(t, certmanagement.ModeProd)
	if err != nil || paths.Mode != certmanagement.ModeTest {
		t.Fatalf("cycle 2 = (%+v, %v), want test cert", paths, err)
	}

	wantPrimary := []string{certmanagement.ModeProd, certmanagement.ModeTest}
	if !equalStrings(acme.primaryModes, wantPrimary) {
		t.Fatalf("primary modes = %v, want %v", acme.primaryModes, wantPrimary)
	}

	// Cycle 3: idempotent — PEMs already match the effective mode, no CA call.
	paths, err = env.ensure(t, certmanagement.ModeProd)
	if err != nil || paths.Mode != certmanagement.ModeTest {
		t.Fatalf("cycle 3 = (%+v, %v), want test cert", paths, err)
	}

	if len(acme.primaryModes) != len(wantPrimary) {
		t.Fatalf("idempotent cycle called the CA: %v", acme.primaryModes)
	}

	if entry := env.downgrades(t)[testDomain]; entry.Mode != certmanagement.ModeTest {
		t.Fatalf("downgrade must persist until expiry, got %+v", entry)
	}
}

func TestEnsureCertificate_DowngradeCascadesToMock(t *testing.T) {
	t.Parallel()

	acme := newFakeAcme()
	acme.primary = func(mode string) error {
		if mode == certmanagement.ModeMock {
			return nil
		}

		return errTestRateLimited
	}
	env := newCertEnv(t, acme)

	for step, wantDowngrade := range []string{certmanagement.ModeTest, certmanagement.ModeMock} {
		_, err := env.ensure(t, certmanagement.ModeProd)
		if !errors.Is(err, errTestRateLimited) {
			t.Fatalf("step %d err = %v", step, err)
		}

		if entry := env.downgrades(t)[testDomain]; entry.Mode != wantDowngrade {
			t.Fatalf("step %d downgrade = %+v, want %q", step, entry, wantDowngrade)
		}
	}

	paths, err := env.ensure(t, certmanagement.ModeProd)
	if err != nil || paths.Mode != certmanagement.ModeMock {
		t.Fatalf("final = (%+v, %v), want mock", paths, err)
	}
}

func TestEnsureCertificate_DowngradeCascadesToNone(t *testing.T) {
	t.Parallel()

	acme := newFakeAcme()
	acme.primary = alwaysRateLimited
	env := newCertEnv(t, acme)

	for step, wantDowngrade := range []string{
		certmanagement.ModeTest,
		certmanagement.ModeMock,
		certmanagement.ModeNone,
	} {
		_, err := env.ensure(t, certmanagement.ModeProd)
		if !errors.Is(err, errTestRateLimited) {
			t.Fatalf("step %d err = %v, want rate limit", step, err)
		}

		if entry := env.downgrades(t)[testDomain]; entry.Mode != wantDowngrade {
			t.Fatalf("step %d downgrade = %+v, want %q", step, entry, wantDowngrade)
		}
	}

	_, err := env.ensure(t, certmanagement.ModeProd)
	if !errors.Is(err, certmanagement.ErrInvalidCertMode) {
		t.Fatalf("at none floor: err = %v, want %v", err, certmanagement.ErrInvalidCertMode)
	}

	if len(acme.primaryModes) != 3 {
		t.Fatalf("at none floor must not call CA again: primaryModes=%v", acme.primaryModes)
	}
}

func TestEnsureCertificate_DowngradeExpiry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		until     time.Time
		wantMode  string
		wantEntry bool
	}{
		{name: "active_override_applies", until: time.Now().Add(time.Hour), wantMode: certmanagement.ModeTest, wantEntry: true},
		{name: "expired_override_pruned", until: time.Now().Add(-time.Minute), wantMode: certmanagement.ModeProd, wantEntry: false},
		{name: "legacy_no_expiry_applies", until: time.Time{}, wantMode: certmanagement.ModeTest, wantEntry: true},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newCertEnv(t, newFakeAcme())

			err := env.repo.SaveDowngrades(map[string]certmanagement.DowngradeEntry{
				testDomain: {Mode: certmanagement.ModeTest, Until: tcase.until},
			})
			if err != nil {
				t.Fatalf("seed downgrade: %v", err)
			}

			paths, err := env.ensure(t, certmanagement.ModeProd)
			if err != nil || paths.Mode != tcase.wantMode {
				t.Fatalf("ensure = (%+v, %v), want mode %q", paths, err, tcase.wantMode)
			}

			_, has := env.downgrades(t)[testDomain]
			if has != tcase.wantEntry {
				t.Fatalf("downgrade entry present = %v, want %v", has, tcase.wantEntry)
			}
		})
	}
}

func TestEnsureCertificate_DowngradeNeverRaisesMode(t *testing.T) {
	t.Parallel()

	env := newCertEnv(t, newFakeAcme())

	err := env.repo.SaveDowngrades(map[string]certmanagement.DowngradeEntry{
		testDomain: {Mode: certmanagement.ModeTest, Until: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("seed downgrade: %v", err)
	}

	paths, err := env.ensure(t, certmanagement.ModeMock)
	if err != nil || paths.Mode != certmanagement.ModeMock {
		t.Fatalf("ensure = (%+v, %v), want mock (settings lower than override)", paths, err)
	}
}

func TestEnsureCertificate_ExistingPEMsSkipCAAndClearRetries(t *testing.T) {
	t.Parallel()

	acme := newFakeAcme()
	acme.issued[testDomain] = certmanagement.ModeProd
	env := newCertEnv(t, acme)

	err := env.repo.SaveRetries(map[string]certmanagement.RetryEntry{testDomain: {Attempts: 2}})
	if err != nil {
		t.Fatalf("seed retries: %v", err)
	}

	paths, err := env.ensure(t, certmanagement.ModeProd)
	if err != nil || paths.Mode != certmanagement.ModeProd {
		t.Fatalf("ensure = (%+v, %v)", paths, err)
	}

	if len(acme.primaryModes) != 0 {
		t.Fatalf("CA must not be called for matching PEMs: %v", acme.primaryModes)
	}

	if len(env.retries(t)) != 0 {
		t.Fatalf("attempts must be cleared: %+v", env.retries(t))
	}
}

func TestEnsureCertificate_SuccessClearsAttempts(t *testing.T) {
	t.Parallel()

	env := newCertEnv(t, newFakeAcme())

	err := env.repo.SaveRetries(map[string]certmanagement.RetryEntry{testDomain: {Attempts: 2}})
	if err != nil {
		t.Fatalf("seed retries: %v", err)
	}

	_, err = env.ensure(t, certmanagement.ModeProd)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	if len(env.retries(t)) != 0 {
		t.Fatalf("attempts must be cleared on success: %+v", env.retries(t))
	}
}

func TestEnsureCertificate_RetryAndBlock(t *testing.T) {
	t.Parallel()

	acme := newFakeAcme()
	acme.primary = alwaysFails
	env := newCertEnv(t, acme)

	for attempt := 1; attempt < testTries; attempt++ {
		_, err := env.ensure(t, certmanagement.ModeProd)
		if !errors.Is(err, errTestBoom) {
			t.Fatalf("attempt %d err = %v", attempt, err)
		}

		entry := env.retries(t)[testDomain]
		if entry.Attempts != attempt || entry.Blocked {
			t.Fatalf("attempt %d state = %+v", attempt, entry)
		}

		if len(env.events(t)) != 0 {
			t.Fatalf("attempt %d must not write events: %v", attempt, env.events(t))
		}
	}

	started := time.Now()

	_, err := env.ensure(t, certmanagement.ModeProd)
	if !errors.Is(err, errTestBoom) {
		t.Fatalf("final attempt err = %v", err)
	}

	entry := env.retries(t)[testDomain]
	if entry.Attempts != testTries || !entry.Blocked {
		t.Fatalf("expected blocked after %d attempts: %+v", testTries, entry)
	}

	wantUntil := started.Add(testBlockHours * time.Hour)
	if d := entry.BlockedUntil.Sub(wantUntil); d < -time.Minute || d > time.Minute {
		t.Fatalf("blocked_until = %v, want ~%v", entry.BlockedUntil, wantUntil)
	}

	evs := env.events(t)
	if len(evs) != 1 || evs[0]["kind"] != "cert_issue_failed" || evs[0]["domain"] != testDomain ||
		evs[0]["mode"] != certmanagement.ModeProd || evs[0]["attempts"] != "3" {
		t.Fatalf("events = %v", evs)
	}

	callsBefore := len(acme.primaryModes)

	_, err = env.ensure(t, certmanagement.ModeProd)
	if !errors.Is(err, certmanagement.ErrCertBlocked) {
		t.Fatalf("blocked err = %v, want ErrCertBlocked", err)
	}

	if len(acme.primaryModes) != callsBefore {
		t.Fatalf("blocked domain must not reach the CA")
	}
}

func TestEnsureCertificate_BlockExpiry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		entry       certmanagement.RetryEntry
		wantBlocked bool
	}{
		{
			name:        "active_block",
			entry:       certmanagement.RetryEntry{Attempts: testTries, Blocked: true, BlockedUntil: time.Now().Add(time.Hour)},
			wantBlocked: true,
		},
		{
			name:        "legacy_block_without_until",
			entry:       certmanagement.RetryEntry{Attempts: testTries, Blocked: true},
			wantBlocked: true,
		},
		{
			name:        "expired_block_retries_and_clears",
			entry:       certmanagement.RetryEntry{Attempts: testTries, Blocked: true, BlockedUntil: time.Now().Add(-time.Minute)},
			wantBlocked: false,
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			acme := newFakeAcme()
			env := newCertEnv(t, acme)

			err := env.repo.SaveRetries(map[string]certmanagement.RetryEntry{testDomain: tcase.entry})
			if err != nil {
				t.Fatalf("seed retries: %v", err)
			}

			paths, err := env.ensure(t, certmanagement.ModeProd)

			if tcase.wantBlocked {
				if !errors.Is(err, certmanagement.ErrCertBlocked) {
					t.Fatalf("err = %v, want ErrCertBlocked", err)
				}

				if len(acme.primaryModes) != 0 {
					t.Fatalf("blocked domain reached the CA")
				}

				return
			}

			if err != nil || paths.Mode != certmanagement.ModeProd {
				t.Fatalf("ensure = (%+v, %v), want success after block expiry", paths, err)
			}

			if len(env.retries(t)) != 0 {
				t.Fatalf("expired block must be cleared: %+v", env.retries(t))
			}
		})
	}
}

func TestEnsureCertificate_EmptyDomain(t *testing.T) {
	t.Parallel()

	env := newCertEnv(t, newFakeAcme())

	_, err := env.svc.EnsureCertificate(context.Background(), "", testEmail, certmanagement.ModeProd, testTimeout)
	if !errors.Is(err, certmanagement.ErrEmptyDomain) {
		t.Fatalf("err = %v, want ErrEmptyDomain", err)
	}
}
