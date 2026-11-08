package acme_test

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/acme"
	"github.com/uservseatb/apinfra/internal/sys"
)

const testEmail = "ops@example.com"

func newClient(infra string) *acme.Client {
	return acme.NewClient(acme.Config{InfraDir: infra, FS: sys.FileWriter{}})
}

func TestClientIssue_MockWritesSelfSignedPair(t *testing.T) {
	t.Parallel()

	infra := t.TempDir()

	paths, err := newClient(infra).Issue(context.Background(), testDomain, "", acme.ModeMock, time.Second)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if paths != acme.NewPaths(infra, testDomain, acme.ModeMock) {
		t.Fatalf("paths = %+v", paths)
	}

	certPEM, err := os.ReadFile(paths.Fullchain)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("fullchain.pem is not PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	if cert.Subject.CommonName != testDomain || len(cert.DNSNames) != 1 || cert.DNSNames[0] != testDomain {
		t.Fatalf("cert names: CN=%q SAN=%v", cert.Subject.CommonName, cert.DNSNames)
	}

	if time.Until(cert.NotAfter) < 24*time.Hour || time.Since(cert.NotBefore) < 0 {
		t.Fatalf("validity window: %v - %v", cert.NotBefore, cert.NotAfter)
	}

	keyPEM, err := os.ReadFile(paths.Privkey)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("privkey.pem is not PEM")
	}

	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}

	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		t.Fatal("key does not match certificate")
	}

	info, err := os.Stat(paths.Privkey)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("privkey mode = %v (err %v), want 0600", info.Mode().Perm(), err)
	}

	if !acme.PEMsMatchMode(infra, testDomain, acme.ModeMock) {
		t.Fatal("mock issuance must leave a matching .issued-mode marker")
	}
}

func TestClientIssue_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		domain  string
		mode    string
		wantErr error
		wantMsg string
	}{
		{name: "empty_domain", domain: "", mode: acme.ModeMock, wantErr: acme.ErrEmptyDomain},
		{name: "empty_mode", domain: testDomain, mode: "", wantErr: acme.ErrEmptyDomain, wantMsg: "mode missing"},
		{name: "unknown_mode", domain: testDomain, mode: "staging", wantMsg: "invalid certificate mode"},
		{name: "none_is_not_issuable", domain: testDomain, mode: "none", wantMsg: "invalid certificate mode"},
		{name: "prod_requires_email", domain: testDomain, mode: acme.ModeProd, wantErr: acme.ErrEmailRequired},
		{name: "test_requires_email", domain: testDomain, mode: acme.ModeTest, wantErr: acme.ErrEmailRequired},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			infra := t.TempDir()

			_, err := newClient(infra).Issue(context.Background(), tcase.domain, "", tcase.mode, time.Second)
			if err == nil {
				t.Fatal("expected error")
			}

			if tcase.wantErr != nil && !errors.Is(err, tcase.wantErr) {
				t.Fatalf("err = %v, want %v", err, tcase.wantErr)
			}

			if tcase.wantMsg != "" && !strings.Contains(err.Error(), tcase.wantMsg) {
				t.Fatalf("err = %v, want containing %q", err, tcase.wantMsg)
			}

			if acme.FilesPresent(infra, tcase.domain, acme.ModeProd) || acme.FilesPresent(infra, tcase.domain, acme.ModeMock) {
				t.Fatal("failed issuance must not leave PEMs behind")
			}
		})
	}
}

func TestClientIssue_UnresponsiveCAFailsWithinTimeout(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := newCountingServer(t, func() { <-release })

	t.Cleanup(func() { close(release) })

	client := acme.NewClient(acme.Config{
		InfraDir:    t.TempDir(),
		FS:          sys.FileWriter{},
		Directories: acme.Directories{LetsEncryptProd: srv.url()},
	})

	start := time.Now()

	_, err := client.Issue(context.Background(), testDomain, testEmail, acme.ModeProd, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}

	// lego's own HTTP timeout and the wall-clock guard share the budget; either may fire first.
	if !errors.Is(err, acme.ErrTimeout) && !strings.Contains(err.Error(), "Timeout exceeded") {
		t.Fatalf("err = %v, want a timeout", err)
	}

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Issue blocked for %v despite a 200ms budget", elapsed)
	}
}

func TestClientIssue_CanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newClient(t.TempDir()).Issue(ctx, testDomain, testEmail, acme.ModeProd, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestClientIssue_FailedObtainLeavesNoPEMs(t *testing.T) {
	t.Parallel()

	infra := t.TempDir()
	srv := newCountingServer(t, nil)

	client := acme.NewClient(acme.Config{
		InfraDir:    infra,
		FS:          sys.FileWriter{},
		Directories: acme.Directories{LetsEncryptProd: srv.url()},
	})

	_, err := client.Issue(context.Background(), testDomain, testEmail, acme.ModeProd, 5*time.Second)
	if err == nil {
		t.Fatal("expected CA error")
	}

	if !strings.Contains(err.Error(), "provider letsencrypt") {
		t.Fatalf("err must name the provider: %v", err)
	}

	if acme.FilesPresent(infra, testDomain, acme.ModeProd) {
		t.Fatal("failed obtain must not write PEMs")
	}

	_, statErr := os.Stat(filepath.Join(infra, "certbot", "www"))
	if statErr != nil {
		t.Fatalf("webroot must exist for HTTP-01: %v", statErr)
	}
}
