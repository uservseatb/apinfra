package nginx_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/domain/certmanagement"
	"github.com/uservseatb/apinfra/internal/domain/nginx"
	"github.com/uservseatb/apinfra/internal/events"
	"github.com/uservseatb/apinfra/internal/infrastructure"
	"github.com/uservseatb/apinfra/internal/infrastructure/adapters"
	"github.com/uservseatb/apinfra/internal/sys"
)

const (
	testNamespace = "dev"
	domainApp     = "app.example.com"
	domainOld     = "old.example.com"
	testEmail     = "ops@example.com"
	appConfFile   = "dev_web.conf"
	upstreamWeb   = `proxy_pass "http://web.apinfra_web:80"`
	sslLiveApp    = "/etc/nginx/ssl/live/app.example.com"
	mockDirApp    = "/etc/nginx/mockcerts/app.example.com"
	certUpdateSec = 86400
)

var (
	errTestRateLimited = errors.New("test: rate limited")
	errTestBoom        = errors.New("test: boom")
	errTestDNS         = errors.New("test: dig failed")
)

type fakeProxy struct {
	testErr         error
	reloadErr       error
	failReloadsLeft int
	reloads         int
}

func (p *fakeProxy) TestConfiguration(context.Context, time.Duration) error { return p.testErr }

func (p *fakeProxy) Reload(context.Context, time.Duration) error {
	p.reloads++

	if p.failReloadsLeft > 0 {
		p.failReloadsLeft--

		return errTestBoom
	}

	return p.reloadErr
}

type fakeDNS struct {
	ips   []string
	err   error
	calls int
}

func (d *fakeDNS) LookupShortIPs(context.Context, string, time.Duration) ([]string, error) {
	d.calls++

	return d.ips, d.err
}

// fakeAcme is a CA stand-in that writes real PEM files so VhostFS sees them.
type fakeAcme struct {
	infra string
	fail  func(mode string) error
	// failFor makes issuance fail for specific domains only.
	failFor map[string]error
	issued  map[string]string
	calls   []string
}

func certDir(infra, domain, mode string) string {
	if mode == certmanagement.ModeMock {
		return filepath.Join(infra, "mockcerts", domain)
	}

	return filepath.Join(infra, "certbot", "conf", "live", domain)
}

func (f *fakeAcme) Issue(_ context.Context, domain, _, mode string, _ time.Duration) (certmanagement.CertPaths, error) {
	f.calls = append(f.calls, domain+":"+mode)

	err := f.failFor[domain]
	if err != nil {
		return certmanagement.CertPaths{}, err
	}

	if f.fail != nil {
		err = f.fail(mode)
		if err != nil {
			return certmanagement.CertPaths{}, err
		}
	}

	dir := certDir(f.infra, domain, mode)

	err = os.MkdirAll(dir, 0o755)
	if err != nil {
		return certmanagement.CertPaths{}, fmt.Errorf("mkdir: %w", err)
	}

	for _, name := range []string{"fullchain.pem", "privkey.pem"} {
		err = os.WriteFile(filepath.Join(dir, name), []byte("pem-"+mode), 0o600)
		if err != nil {
			return certmanagement.CertPaths{}, fmt.Errorf("write pem: %w", err)
		}
	}

	f.issued[domain] = mode

	return f.PathsFor(domain, mode), nil
}

func (f *fakeAcme) IssueSecondary(
	context.Context, string, string, string, time.Duration,
) (certmanagement.CertPaths, error) {
	return certmanagement.CertPaths{}, errTestBoom
}

func (f *fakeAcme) HasSecondary() bool { return false }

func (f *fakeAcme) PEMsMatchMode(domain, mode string) bool {
	_, err := os.Stat(filepath.Join(certDir(f.infra, domain, mode), "fullchain.pem"))

	return err == nil && f.issued[domain] == mode
}

func (f *fakeAcme) PathsFor(domain, mode string) certmanagement.CertPaths {
	dir := certDir(f.infra, domain, mode)

	return certmanagement.CertPaths{
		Fullchain: filepath.Join(dir, "fullchain.pem"),
		Privkey:   filepath.Join(dir, "privkey.pem"),
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

type routeEnv struct {
	home  string
	infra string
	proxy *fakeProxy
	dns   *fakeDNS
	acme  *fakeAcme
	repo  *adapters.YamlStateRepository
	svc   *nginx.RoutingService
}

func newRouteEnv(t *testing.T) *routeEnv {
	t.Helper()

	home := t.TempDir()
	infra := filepath.Join(home, "infra")

	repo := adapters.NewYamlStateRepository(infra, sys.FileWriter{})
	acme := &fakeAcme{infra: infra, issued: map[string]string{}}
	proxy := &fakeProxy{}
	dns := &fakeDNS{ips: []string{"127.0.0.1"}}

	certSvc := certmanagement.NewCertificateService(
		repo, acme, events.NewFileStore(filepath.Join(home, ".events")),
		certmanagement.CertRetryPolicy{TriesCount: 3, BlockDuration: time.Hour, DowngradeCertModeTTL: 6 * time.Hour},
	)

	svc := nginx.NewRoutingService(
		infrastructure.NewVhostFS(infra), proxy, dns, adapters.NewCertIssuerBridge(certSvc), nginx.ProcessTimeouts{},
	)

	return &routeEnv{home: home, infra: infra, proxy: proxy, dns: dns, acme: acme, repo: repo, svc: svc}
}

func (e *routeEnv) run(params nginx.RoutingParams) error {
	err := e.svc.ProcessRouting(context.Background(), params)
	if err != nil {
		return fmt.Errorf("process routing: %w", err)
	}

	return nil
}

func (e *routeEnv) runLive(projects ...nginx.Project) error {
	return e.run(nginx.RoutingParams{Projects: projects, ProxyRunning: true, ProxyReady: true})
}

func (e *routeEnv) confPath(name string) string {
	return filepath.Join(e.infra, "nginx", "conf", name)
}

func (e *routeEnv) conf(t *testing.T, name string) (string, bool) {
	t.Helper()

	data, err := os.ReadFile(e.confPath(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false
		}

		t.Fatalf("read conf %s: %v", name, err)
	}

	return string(data), true
}

func (e *routeEnv) homeFile(t *testing.T, name string) (string, bool) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(e.home, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false
		}

		t.Fatalf("read %s: %v", name, err)
	}

	return string(data), true
}

// installed parses .installedCert into domain -> mode.
func (e *routeEnv) installed(t *testing.T) map[string]string {
	t.Helper()

	out := map[string]string{}

	raw, ok := e.homeFile(t, ".installedCert")
	if !ok {
		return out
	}

	for line := range strings.SplitSeq(raw, "\n") {
		domain, rest, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}

		_, mode, _ := strings.Cut(rest, "|")
		out[domain] = mode
	}

	return out
}

func (e *routeEnv) fileExists(rel string) bool {
	_, err := os.Stat(filepath.Join(e.infra, rel))

	return err == nil
}

func domainProject(certType string) nginx.Project {
	return nginx.Project{
		Name:           "web",
		Namespace:      testNamespace,
		DomainName:     domainApp,
		EntryPointPort: "80",
		ProxyHost:      "web.apinfra_web",
		CertIssueType:  certType,
		CertUpdateSec:  certUpdateSec,
		CertAcmeEmail:  testEmail,
	}
}

func portProject(certType string) nginx.Project {
	return nginx.Project{
		Name:           "api",
		Namespace:      testNamespace,
		InboundPort:    "9999",
		EntryPointPort: "8080",
		ProxyHost:      "api.apinfra_api",
		CertIssueType:  certType,
		CertUpdateSec:  certUpdateSec,
	}
}

func equalStrings(a, b []string) bool { return slices.Equal(a, b) }

func assertContains(t *testing.T, haystack string, needles ...string) {
	t.Helper()

	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			t.Fatalf("expected to contain %q, got:\n%s", needle, haystack)
		}
	}
}

func assertAbsent(t *testing.T, haystack string, needles ...string) {
	t.Helper()

	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			t.Fatalf("expected NOT to contain %q, got:\n%s", needle, haystack)
		}
	}
}

func TestProcessRouting_RendersConfPerRoutingMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		project       nginx.Project
		confName      string
		wantInConf    []string
		wantNotInConf []string
		wantInstalled map[string]string
		wantACME      []string
		wantDNSCalls  int
		wantFiles     []string
	}{
		{
			name:          "domain_prod",
			project:       domainProject("prod"),
			confName:      appConfFile,
			wantInConf:    []string{"server_name app.example.com", "return 301 https://", sslLiveApp + "/fullchain.pem", upstreamWeb},
			wantInstalled: map[string]string{domainApp: "prod"},
			wantACME:      []string{domainApp + ":prod"},
			wantDNSCalls:  1,
			wantFiles:     []string{"certbot/conf/live/app.example.com/fullchain.pem"},
		},
		{
			name:          "domain_test",
			project:       domainProject("test"),
			confName:      appConfFile,
			wantInConf:    []string{sslLiveApp + "/privkey.pem", upstreamWeb},
			wantInstalled: map[string]string{domainApp: "test"},
			wantACME:      []string{domainApp + ":test"},
			wantDNSCalls:  1,
			wantFiles:     []string{"certbot/conf/live/app.example.com/privkey.pem"},
		},
		{
			name:          "domain_mock_skips_dns",
			project:       domainProject("mock"),
			confName:      appConfFile,
			wantInConf:    []string{mockDirApp + "/fullchain.pem", upstreamWeb},
			wantNotInConf: []string{"/etc/nginx/ssl/live"},
			wantInstalled: map[string]string{domainApp: "mock"},
			wantACME:      []string{domainApp + ":mock"},
			wantFiles:     []string{"mockcerts/app.example.com/fullchain.pem"},
		},
		{
			name:          "domain_none_plain_http",
			project:       domainProject("none"),
			confName:      appConfFile,
			wantInConf:    []string{"listen 80;", "server_name app.example.com", upstreamWeb},
			wantNotInConf: []string{"ssl_certificate", "return 301"},
			wantInstalled: map[string]string{domainApp: "none"},
		},
		{
			name:          "port_none_plain_http",
			project:       portProject("none"),
			confName:      "dev_api.conf",
			wantInConf:    []string{"listen 9999;", `proxy_pass "http://api.apinfra_api:8080"`},
			wantNotInConf: []string{"ssl_certificate"},
			wantInstalled: map[string]string{"port-9999": "none"},
		},
		{
			name:          "port_mock_https",
			project:       portProject("mock"),
			confName:      "dev_api.conf",
			wantInConf:    []string{"listen 9999 ssl;", "/etc/nginx/mockcerts/port-9999/fullchain.pem"},
			wantInstalled: map[string]string{"port-9999": "mock"},
			wantACME:      []string{"port-9999:mock"},
			wantFiles:     []string{"mockcerts/port-9999/fullchain.pem"},
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newRouteEnv(t)

			err := env.runLive(tcase.project)
			if err != nil {
				t.Fatalf("ProcessRouting: %v", err)
			}

			conf, ok := env.conf(t, tcase.confName)
			if !ok {
				t.Fatalf("conf %s was not written", tcase.confName)
			}

			assertContains(t, conf, tcase.wantInConf...)
			assertAbsent(t, conf, tcase.wantNotInConf...)

			gotInstalled := env.installed(t)
			if len(gotInstalled) != len(tcase.wantInstalled) {
				t.Fatalf("installed = %v, want %v", gotInstalled, tcase.wantInstalled)
			}

			for domain, mode := range tcase.wantInstalled {
				if gotInstalled[domain] != mode {
					t.Fatalf("installed = %v, want %v", gotInstalled, tcase.wantInstalled)
				}
			}

			if !equalStrings(env.acme.calls, tcase.wantACME) {
				t.Fatalf("acme calls = %v, want %v", env.acme.calls, tcase.wantACME)
			}

			if env.dns.calls != tcase.wantDNSCalls {
				t.Fatalf("dns calls = %d, want %d", env.dns.calls, tcase.wantDNSCalls)
			}

			for _, rel := range tcase.wantFiles {
				if !env.fileExists(rel) {
					t.Fatalf("expected %s to exist", rel)
				}
			}

			// Second cycle against unchanged state must be a no-op.
			reloads := env.proxy.reloads

			err = env.runLive(tcase.project)
			if err != nil {
				t.Fatalf("second ProcessRouting: %v", err)
			}

			if env.proxy.reloads != reloads {
				t.Fatalf("idempotent cycle reloaded nginx: %d -> %d", reloads, env.proxy.reloads)
			}

			if !equalStrings(env.acme.calls, tcase.wantACME) {
				t.Fatalf("idempotent cycle called the CA again: %v", env.acme.calls)
			}

			again, _ := env.conf(t, tcase.confName)
			if again != conf {
				t.Fatalf("conf changed on idempotent cycle:\n%s\n---\n%s", conf, again)
			}
		})
	}
}

func TestProcessRouting_DowngradeLifecycle(t *testing.T) {
	// Not parallel: asserts on the global log output.
	var logs bytes.Buffer

	prevOut, prevFlags, prevPrefix := log.Writer(), log.Flags(), log.Prefix()

	log.SetOutput(&logs)
	log.SetFlags(0)
	log.SetPrefix("")

	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		log.SetPrefix(prevPrefix)
	})

	env := newRouteEnv(t)
	rateLimitProd := true
	env.acme.fail = func(mode string) error {
		if mode == certmanagement.ModeProd && rateLimitProd {
			return errTestRateLimited
		}

		return nil
	}

	project := domainProject("prod")

	// Cycle 1: prod is rate limited -> conf rolled back, downgrade to test scheduled.
	err := env.runLive(project)
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}

	if _, ok := env.conf(t, appConfFile); ok {
		t.Fatal("cycle 1: conf must be rolled back when the cert is unresolved")
	}

	if len(env.installed(t)) != 0 {
		t.Fatalf("cycle 1: nothing may be recorded as installed: %v", env.installed(t))
	}

	downgrade, ok := env.homeFile(t, ".downgradeCertMode")
	if !ok {
		t.Fatal("cycle 1: .downgradeCertMode missing")
	}

	assertContains(t, downgrade, domainApp, "mode: test")

	evs, _ := env.homeFile(t, ".events")
	assertContains(t, evs, "cert_rate_limited", `"from":"prod"`, `"to":"test"`)

	// Cycle 2: settings still say prod, the override issues a test cert.
	err = env.runLive(project)
	if err != nil {
		t.Fatalf("cycle 2: %v", err)
	}

	if want := []string{domainApp + ":prod", domainApp + ":test"}; !equalStrings(env.acme.calls, want) {
		t.Fatalf("cycle 2: acme calls = %v, want %v", env.acme.calls, want)
	}

	if got := env.installed(t)[domainApp]; got != "test" {
		t.Fatalf("cycle 2: installed mode = %q, want test", got)
	}

	conf, _ := env.conf(t, appConfFile)
	assertContains(t, conf, sslLiveApp+"/fullchain.pem", upstreamWeb)

	assertContains(t, logs.String(), "using downgrade domain=app.example.com requested=prod effective=test")

	// Cycle 3: nothing changed -> no CA call, no reload, same conf.
	calls, reloads := len(env.acme.calls), env.proxy.reloads

	err = env.runLive(project)
	if err != nil {
		t.Fatalf("cycle 3: %v", err)
	}

	if len(env.acme.calls) != calls || env.proxy.reloads != reloads {
		t.Fatalf("cycle 3 must be a no-op: calls %d->%d reloads %d->%d",
			calls, len(env.acme.calls), reloads, env.proxy.reloads)
	}

	// Cycle 4: override expires and the CA recovered -> reissue prod.
	rateLimitProd = false

	err = env.repo.SaveDowngrades(map[string]certmanagement.DowngradeEntry{
		domainApp: {Mode: certmanagement.ModeTest, Until: time.Now().Add(-time.Minute)},
	})
	if err != nil {
		t.Fatalf("expire downgrade: %v", err)
	}

	err = env.runLive(project)
	if err != nil {
		t.Fatalf("cycle 4: %v", err)
	}

	if got := env.acme.calls[len(env.acme.calls)-1]; got != domainApp+":prod" {
		t.Fatalf("cycle 4: last acme call = %q, want prod reissue", got)
	}

	if got := env.installed(t)[domainApp]; got != "prod" {
		t.Fatalf("cycle 4: installed mode = %q, want prod", got)
	}

	left, err := env.repo.LoadDowngrades(time.Time{})
	if err != nil || len(left) != 0 {
		t.Fatalf("cycle 4: expired downgrade must be pruned: (%v, %v)", left, err)
	}
}

func TestProcessRouting_DowngradeToNoneAppliesHTTP(t *testing.T) {
	t.Parallel()

	env := newRouteEnv(t)
	project := domainProject("prod")

	err := env.repo.SaveDowngrades(map[string]certmanagement.DowngradeEntry{
		domainApp: {Mode: certmanagement.ModeNone, Until: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("seed downgrade none: %v", err)
	}

	err = env.runLive(project)
	if err != nil {
		t.Fatalf("ProcessRouting: %v", err)
	}

	if len(env.acme.calls) != 0 {
		t.Fatalf("downgrade to none must skip ACME, got calls=%v", env.acme.calls)
	}

	if got := env.installed(t)[domainApp]; got != "none" {
		t.Fatalf("installed mode = %q, want none", got)
	}

	conf, ok := env.conf(t, appConfFile)
	if !ok {
		t.Fatal("expected conf to be written")
	}

	assertContains(t, conf, "listen 80;", "server_name app.example.com", upstreamWeb)
	assertAbsent(t, conf, "ssl_certificate", "return 301")
}

func TestProcessRouting_DNSPreflight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ips        []string
		dnsErr     error
		publicIP   string
		wantIssued bool
	}{
		{name: "empty_answer_skips", ips: nil, wantIssued: false},
		{name: "lookup_error_skips", dnsErr: errTestDNS, wantIssued: false},
		{name: "public_ip_mismatch_skips", ips: []string{"1.1.1.1"}, publicIP: "2.2.2.2", wantIssued: false},
		{name: "public_ip_among_answers", ips: []string{"1.1.1.1", "2.2.2.2"}, publicIP: "2.2.2.2", wantIssued: true},
		{name: "any_ip_without_public_ip", ips: []string{"1.1.1.1"}, wantIssued: true},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newRouteEnv(t)
			env.dns.ips, env.dns.err = tcase.ips, tcase.dnsErr

			err := env.run(nginx.RoutingParams{
				Projects:     []nginx.Project{domainProject("prod")},
				ProxyRunning: true,
				ProxyReady:   true,
				PublicIP:     tcase.publicIP,
			})
			if err != nil {
				t.Fatalf("ProcessRouting: %v", err)
			}

			_, confExists := env.conf(t, appConfFile)
			if confExists != tcase.wantIssued {
				t.Fatalf("conf exists = %v, want %v", confExists, tcase.wantIssued)
			}

			if issued := len(env.acme.calls) > 0; issued != tcase.wantIssued {
				t.Fatalf("CA called = %v, want %v", issued, tcase.wantIssued)
			}

			for _, name := range []string{".retries", ".downgradeCertMode", ".installedCert"} {
				_, exists := env.homeFile(t, name)
				if exists && !tcase.wantIssued {
					t.Fatalf("%s must not be written when DNS preflight fails", name)
				}
			}
		})
	}
}

func TestProcessRouting_IssueFailureRestoresPreviousConf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		fail         error
		wantAttempts int
		wantEvent    bool
	}{
		{name: "other_error_bumps_retries", fail: errTestBoom, wantAttempts: 1},
		{name: "rate_limit_does_not_bump_retries", fail: errTestRateLimited, wantEvent: true},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newRouteEnv(t)
			env.acme.fail = func(string) error { return tcase.fail }

			const previous = "# previous port conf\n"

			err := os.MkdirAll(filepath.Dir(env.confPath(appConfFile)), 0o755)
			if err != nil {
				t.Fatal(err)
			}

			err = os.WriteFile(env.confPath(appConfFile), []byte(previous), 0o600)
			if err != nil {
				t.Fatal(err)
			}

			err = env.runLive(domainProject("prod"))
			if err != nil {
				t.Fatalf("ProcessRouting must soft-fail issuance: %v", err)
			}

			conf, _ := env.conf(t, appConfFile)
			if conf != previous {
				t.Fatalf("previous conf not restored, got:\n%s", conf)
			}

			if len(env.installed(t)) != 0 {
				t.Fatalf("installed must stay empty: %v", env.installed(t))
			}

			retries, _ := env.homeFile(t, ".retries")
			if tcase.wantAttempts > 0 {
				assertContains(t, retries, fmt.Sprintf("attempts: %d", tcase.wantAttempts))
			} else if retries != "" {
				t.Fatalf("unexpected .retries: %s", retries)
			}

			evs, _ := env.homeFile(t, ".events")
			if has := strings.Contains(evs, "cert_rate_limited"); has != tcase.wantEvent {
				t.Fatalf("rate-limit event present = %v, want %v (%s)", has, tcase.wantEvent, evs)
			}
		})
	}
}

func TestProcessRouting_NginxTestFailureRollsBackConf(t *testing.T) {
	t.Parallel()

	env := newRouteEnv(t)
	env.proxy.testErr = fmt.Errorf("%w: broken", nginx.ErrNginxTestFailed)

	err := env.runLive(domainProject("none"))
	if !errors.Is(err, nginx.ErrNginxTestFailed) {
		t.Fatalf("err = %v, want ErrNginxTestFailed", err)
	}

	if _, ok := env.conf(t, appConfFile); ok {
		t.Fatal("conf failing nginx -t must be rolled back")
	}
}

func TestProcessRouting_ProxyNotReadySkipsIssuance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params func(p nginx.Project) nginx.RoutingParams
		setup  func(e *routeEnv)
	}{
		{
			name: "exec_unavailable_during_reload",
			params: func(p nginx.Project) nginx.RoutingParams {
				return nginx.RoutingParams{Projects: []nginx.Project{p}, ProxyRunning: true, ProxyReady: true}
			},
			setup: func(e *routeEnv) { e.proxy.testErr = nginx.ErrProxyNotReady },
		},
		{
			name: "proxy_not_running",
			params: func(p nginx.Project) nginx.RoutingParams {
				return nginx.RoutingParams{Projects: []nginx.Project{p}}
			},
			setup: func(*routeEnv) {},
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newRouteEnv(t)
			tcase.setup(env)

			err := env.run(tcase.params(domainProject("prod")))
			if err != nil {
				t.Fatalf("ProcessRouting: %v", err)
			}

			if len(env.acme.calls) != 0 {
				t.Fatalf("CA must not be called: %v", env.acme.calls)
			}

			if _, ok := env.conf(t, appConfFile); ok {
				t.Fatal("conf with unresolved cert must be rolled back")
			}
		})
	}
}

func TestProcessRouting_NoneModeConfsWrittenWithoutProxy(t *testing.T) {
	t.Parallel()

	env := newRouteEnv(t)

	err := env.run(nginx.RoutingParams{Projects: []nginx.Project{domainProject("none")}})
	if err != nil {
		t.Fatalf("ProcessRouting: %v", err)
	}

	conf, ok := env.conf(t, appConfFile)
	if !ok {
		t.Fatal("none-mode conf must be written even when the proxy is down")
	}

	assertContains(t, conf, upstreamWeb)

	if env.proxy.reloads != 0 {
		t.Fatalf("no reload expected without a running proxy, got %d", env.proxy.reloads)
	}

	if got := env.installed(t)[domainApp]; got != "none" {
		t.Fatalf("installed = %v, want none", env.installed(t))
	}
}

func TestProcessRouting_NoneKeepsPreviousIssueTime(t *testing.T) {
	t.Parallel()

	env := newRouteEnv(t)

	const issuedAt = "2026-01-02T03:04:05.000"

	err := os.MkdirAll(env.home, 0o755)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(env.home, ".installedCert"), []byte(domainApp+"="+issuedAt+"|prod"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = env.runLive(domainProject("none"))
	if err != nil {
		t.Fatalf("ProcessRouting: %v", err)
	}

	raw, _ := env.homeFile(t, ".installedCert")
	if strings.TrimSpace(raw) != domainApp+"="+issuedAt+"|none" {
		t.Fatalf(".installedCert = %q, want issue time preserved with mode none", raw)
	}
}

func TestProcessRouting_RemovedProjectIsPruned(t *testing.T) {
	t.Parallel()

	env := newRouteEnv(t)

	removed := domainProject("mock")
	removed.Name = "old"
	removed.DomainName = domainOld
	removed.ProxyHost = "web.apinfra_old"

	kept := portProject("none")

	err := env.runLive(removed, kept)
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}

	if _, ok := env.conf(t, "dev_old.conf"); !ok || !env.fileExists("mockcerts/old.example.com/fullchain.pem") {
		t.Fatal("cycle 1 must create conf and cert for the soon-removed project")
	}

	err = env.runLive(kept)
	if err != nil {
		t.Fatalf("cycle 2: %v", err)
	}

	if _, ok := env.conf(t, "dev_old.conf"); ok {
		t.Fatal("stale conf must be removed")
	}

	if env.fileExists("mockcerts/old.example.com") {
		t.Fatal("stale cert material must be removed")
	}

	installed := env.installed(t)
	if _, still := installed[domainOld]; still || installed["port-9999"] != "none" {
		t.Fatalf("installed = %v, want only port-9999=none", installed)
	}

	if _, ok := env.conf(t, "dev_api.conf"); !ok {
		t.Fatal("kept project conf must remain")
	}
}

func TestProcessRouting_ExpiredCertIsReissued(t *testing.T) {
	t.Parallel()

	env := newRouteEnv(t)
	project := domainProject("mock")

	err := env.runLive(project)
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}

	old := time.Now().UTC().Add(-2 * certUpdateSec * time.Second).Format(certmanagement.CertTimeLayout)

	err = os.WriteFile(filepath.Join(env.home, ".installedCert"), []byte(domainApp+"="+old+"|mock"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = env.runLive(project)
	if err != nil {
		t.Fatalf("cycle 2: %v", err)
	}

	if want := []string{domainApp + ":mock", domainApp + ":mock"}; !equalStrings(env.acme.calls, want) {
		t.Fatalf("acme calls = %v, want a reissue after expiry", env.acme.calls)
	}

	raw, _ := env.homeFile(t, ".installedCert")
	if strings.Contains(raw, old) {
		t.Fatalf(".installedCert must carry the new issue time: %s", raw)
	}
}

func TestProcessRouting_FailedReissueKeepsExistingCertMaterial(t *testing.T) {
	t.Parallel()

	env := newRouteEnv(t)
	project := domainProject("mock")

	err := env.runLive(project)
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}

	pemPath := filepath.Join(certDir(env.infra, domainApp, certmanagement.ModeMock), "fullchain.pem")

	before, err := os.ReadFile(pemPath)
	if err != nil {
		t.Fatalf("read issued pem: %v", err)
	}

	expired := time.Now().UTC().Add(-2 * certUpdateSec * time.Second).Format(certmanagement.CertTimeLayout)

	err = os.WriteFile(filepath.Join(env.home, ".installedCert"), []byte(domainApp+"="+expired+"|mock"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	env.acme.fail = func(string) error { return errTestBoom }

	err = env.runLive(project)
	if err != nil {
		t.Fatalf("cycle 2 must soft-fail issuance: %v", err)
	}

	after, err := os.ReadFile(pemPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("existing PEM must survive a failed reissue: %q, err=%v (was %q)", after, err, before)
	}

	if env.fileExists("mockcerts/app.example.com.reissue") {
		t.Fatal("reissue stash must not be left behind")
	}
}

func TestProcessRouting_OneDomainFailureDoesNotAffectOthers(t *testing.T) {
	t.Parallel()

	env := newRouteEnv(t)
	env.acme.failFor = map[string]error{domainOld: errTestBoom}

	healthy := domainProject("prod")

	failing := domainProject("prod")
	failing.Name = "old"
	failing.DomainName = domainOld
	failing.ProxyHost = "web.apinfra_old"

	plain := portProject("none")

	err := env.runLive(healthy, failing, plain)
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}

	healthyConf, ok := env.conf(t, appConfFile)
	if !ok {
		t.Fatal("healthy domain conf must be written")
	}

	assertContains(t, healthyConf, sslLiveApp+"/fullchain.pem")

	if _, ok := env.conf(t, "dev_old.conf"); ok {
		t.Fatal("failing domain conf must be rolled back")
	}

	if _, ok := env.conf(t, "dev_api.conf"); !ok {
		t.Fatal("port project conf must be written")
	}

	installed := env.installed(t)
	if installed[domainApp] != "prod" || installed["port-9999"] != "none" {
		t.Fatalf("installed = %v, want app=prod and port-9999=none", installed)
	}

	if _, bad := installed[domainOld]; bad {
		t.Fatalf("failed domain must not be installed: %v", installed)
	}

	retries, _ := env.homeFile(t, ".retries")
	assertContains(t, retries, domainOld)
	assertAbsent(t, retries, domainApp)

	// Next cycle, CA still failing for one domain: the healthy domain is already
	// served over HTTPS, nothing is reissued for it, only the failing one is retried.
	callsBefore := len(env.acme.calls)

	err = env.runLive(healthy, failing, plain)
	if err != nil {
		t.Fatalf("cycle 2: %v", err)
	}

	if got := env.acme.calls[callsBefore:]; !equalStrings(got, []string{domainOld + ":prod"}) {
		t.Fatalf("cycle 2 CA calls = %v, want only %s:prod", got, domainOld)
	}

	conf, _ := env.conf(t, appConfFile)
	assertContains(t, conf, sslLiveApp+"/fullchain.pem")

	if _, ok := env.conf(t, "dev_old.conf"); ok {
		t.Fatal("still-failing domain conf must stay absent")
	}

	// CA recovers.
	env.acme.failFor = nil
	callsBefore = len(env.acme.calls)

	err = env.runLive(healthy, failing, plain)
	if err != nil {
		t.Fatalf("cycle 3: %v", err)
	}

	if got := env.acme.calls[callsBefore:]; !equalStrings(got, []string{domainOld + ":prod"}) {
		t.Fatalf("cycle 3 CA calls = %v, want only %s:prod", got, domainOld)
	}

	if env.installed(t)[domainOld] != "prod" {
		t.Fatalf("recovered domain must be installed: %v", env.installed(t))
	}
}

func TestProcessRouting_ReloadPendingRetriesAfterFailure(t *testing.T) {
	t.Parallel()

	env := newRouteEnv(t)
	vhostFS := infrastructure.NewVhostFS(env.infra)

	proj := portProject("none")
	proj.EntryPointPort = "80"

	err := env.runLive(proj)
	if err != nil {
		t.Fatalf("cycle 1: %v", err)
	}

	pending, err := vhostFS.HasReloadPending()
	if err != nil || pending {
		t.Fatalf("after success HasReloadPending = %v, %v", pending, err)
	}

	proj.EntryPointPort = "8080"
	env.proxy.failReloadsLeft = 1

	err = env.runLive(proj)
	if err == nil {
		t.Fatal("cycle 2: want reload failure")
	}

	pending, err = vhostFS.HasReloadPending()
	if err != nil || !pending {
		t.Fatalf("after failed reload HasReloadPending = %v, %v", pending, err)
	}

	conf, ok := env.conf(t, "dev_api.conf")
	if !ok {
		t.Fatal("conf missing after failed reload")
	}

	assertContains(t, conf, `proxy_pass "http://api.apinfra_api:8080"`)

	reloadsBefore := env.proxy.reloads

	err = env.runLive(proj)
	if err != nil {
		t.Fatalf("cycle 3 (unchanged): %v", err)
	}

	if env.proxy.reloads != reloadsBefore+1 {
		t.Fatalf("unchanged cycle must retry reload: %d -> %d", reloadsBefore, env.proxy.reloads)
	}

	pending, err = vhostFS.HasReloadPending()
	if err != nil || pending {
		t.Fatalf("after successful retry HasReloadPending = %v, %v", pending, err)
	}

	reloadsIdle := env.proxy.reloads

	err = env.runLive(proj)
	if err != nil {
		t.Fatalf("cycle 4: %v", err)
	}

	if env.proxy.reloads != reloadsIdle {
		t.Fatalf("healthy unchanged cycle must not reload: %d -> %d", reloadsIdle, env.proxy.reloads)
	}
}

func TestProcessRouting_EnsureHostFilesPendingTriggersReload(t *testing.T) {
	t.Parallel()

	env := newRouteEnv(t)
	vhostFS := infrastructure.NewVhostFS(env.infra)

	err := env.runLive(portProject("none"))
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}

	reloadsBefore := env.proxy.reloads
	nginxConf := filepath.Join(env.infra, "nginx", "nginx.conf")

	err = os.WriteFile(nginxConf, []byte("# stale asset\n"), 0o600)
	if err != nil {
		t.Fatalf("seed stale nginx.conf: %v", err)
	}

	err = vhostFS.EnsureHostFiles()
	if err != nil {
		t.Fatalf("EnsureHostFiles: %v", err)
	}

	pending, err := vhostFS.HasReloadPending()
	if err != nil || !pending {
		t.Fatalf("asset change must mark pending: has=%v err=%v", pending, err)
	}

	err = env.runLive(portProject("none"))
	if err != nil {
		t.Fatalf("ProcessRouting after asset change: %v", err)
	}

	if env.proxy.reloads != reloadsBefore+1 {
		t.Fatalf("pending asset change must reload: %d -> %d", reloadsBefore, env.proxy.reloads)
	}

	pending, err = vhostFS.HasReloadPending()
	if err != nil || pending {
		t.Fatalf("after reload HasReloadPending = %v, %v", pending, err)
	}
}
