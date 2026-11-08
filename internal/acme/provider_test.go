package acme_test

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/acme"
	"github.com/uservseatb/apinfra/internal/sys"
)

// TestMain makes lego trust httptest's TLS certificate (lego insists on HTTPS
// directories). All httptest TLS servers share the same certificate.
func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	dir, err := os.MkdirTemp("", "acme-test-ca")
	if err != nil {
		return 1
	}

	defer func() { _ = os.RemoveAll(dir) }()

	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()

	caPath := filepath.Join(dir, "ca.pem")

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	err = os.WriteFile(caPath, pemBytes, 0o600)
	if err != nil {
		return 1
	}

	err = os.Setenv("LEGO_CA_CERTIFICATES", caPath)
	if err != nil {
		return 1
	}

	return m.Run()
}

type countingServer struct {
	srv  *httptest.Server
	hits atomic.Int32
}

func (c *countingServer) url() string { return c.srv.URL }

func (c *countingServer) count() int { return int(c.hits.Load()) }

// newCountingServer answers every request with 500 (the CA is "down") and counts hits,
// which is enough to observe which ACME directory a Client talks to.
func newCountingServer(t *testing.T, onHit func()) *countingServer {
	t.Helper()

	srv := &countingServer{}

	srv.srv = httptest.NewTLSServer(http.HandlerFunc(func(resp http.ResponseWriter, _ *http.Request) {
		srv.hits.Add(1)

		if onHit != nil {
			onHit()
		}

		http.Error(resp, "down", http.StatusInternalServerError)
	}))

	t.Cleanup(srv.srv.Close)

	return srv
}

func TestClient_ProviderSelection(t *testing.T) {
	t.Parallel()

	const (
		kid  = "kid"
		hmac = "hmac"
	)

	const (
		onlyProd    = "prod"
		onlyStaging = "staging"
		onlyZeroSSL = "zerossl"
	)

	tests := []struct {
		name          string
		provider      string
		eab           bool
		mode          string
		secondary     bool
		wantSecondary bool
		wantServer    string // the only directory that must be contacted
	}{
		{name: "default_provider_prod", mode: acme.ModeProd, wantServer: onlyProd},
		{name: "letsencrypt_prod", provider: "letsencrypt", mode: acme.ModeProd, wantServer: onlyProd},
		{name: "letsencrypt_test_uses_staging", provider: "letsencrypt", mode: acme.ModeTest, wantServer: onlyStaging},
		{name: "provider_name_is_case_insensitive", provider: " LetsEncrypt ", mode: acme.ModeProd, wantServer: onlyProd},
		{name: "zerossl_prod", provider: "zerossl", eab: true, mode: acme.ModeProd, wantSecondary: true, wantServer: onlyZeroSSL},
		{name: "zerossl_has_no_staging", provider: "zerossl", eab: true, mode: acme.ModeTest, wantSecondary: true, wantServer: onlyZeroSSL},
		{name: "unknown_provider_falls_back_to_letsencrypt", provider: "nope", mode: acme.ModeProd, wantServer: onlyProd},
		{
			name: "letsencrypt_with_eab_has_zerossl_secondary", provider: "letsencrypt", eab: true, mode: acme.ModeProd,
			secondary: true, wantSecondary: true, wantServer: onlyZeroSSL,
		},
		{
			name: "zerossl_with_eab_has_letsencrypt_secondary", provider: "zerossl", eab: true, mode: acme.ModeProd,
			secondary: true, wantSecondary: true, wantServer: onlyProd,
		},
		{
			name: "secondary_follows_mode_for_letsencrypt", provider: "zerossl", eab: true, mode: acme.ModeTest,
			secondary: true, wantSecondary: true, wantServer: onlyStaging,
		},
		{name: "secondary_missing_without_eab", provider: "letsencrypt", mode: acme.ModeProd, secondary: true},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			servers := map[string]*countingServer{
				onlyProd:    newCountingServer(t, nil),
				onlyStaging: newCountingServer(t, nil),
				onlyZeroSSL: newCountingServer(t, nil),
			}

			cfg := acme.Config{
				InfraDir: t.TempDir(), Provider: tcase.provider, FS: sys.FileWriter{},
				Directories: acme.Directories{
					LetsEncryptProd:    servers[onlyProd].url(),
					LetsEncryptStaging: servers[onlyStaging].url(),
					ZeroSSL:            servers[onlyZeroSSL].url(),
				},
			}
			if tcase.eab {
				cfg.KID, cfg.HMAC = kid, hmac
			}

			client := acme.NewClient(cfg)

			if client.HasSecondary() != tcase.wantSecondary {
				t.Fatalf("HasSecondary = %v, want %v", client.HasSecondary(), tcase.wantSecondary)
			}

			issue := client.Issue
			if tcase.secondary {
				issue = client.IssueSecondary
			}

			_, err := issue(context.Background(), testDomain, testEmail, tcase.mode, 10*time.Second)
			if err == nil {
				t.Fatal("every CA is down, issuance must fail")
			}

			for name, srv := range servers {
				contacted := srv.count() > 0
				if want := name == tcase.wantServer; contacted != want {
					t.Fatalf("directory %s contacted = %v, want %v (err: %v)", name, contacted, want, err)
				}
			}
		})
	}
}
