package certstore_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/certstore"
)

const (
	testDomain = "app.example.com"
	modeProd   = "prod"
	modeTest   = "test"
)

func TestStore_MigrateLegacyInstalledCert(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	infra := filepath.Join(home, "infra")
	mustMkdir(t, filepath.Join(infra, "certbot", "conf", "live", testDomain))

	fullchain, privkey := mustSelfSignedPEM(t, time.Now().Add(30*24*time.Hour))
	writeFile(t, filepath.Join(infra, "certbot", "conf", "live", testDomain, "fullchain.pem"), string(fullchain))
	writeFile(t, filepath.Join(infra, "certbot", "conf", "live", testDomain, "privkey.pem"), string(privkey))
	writeFile(t, filepath.Join(infra, "certbot", "conf", "live", testDomain, ".issued-mode"), modeProd+"\n")

	issued := "2026-11-12T12:41:03.166"
	writeFile(t, filepath.Join(home, certstore.LegacyInstalledCert), testDomain+"="+issued+"|"+modeProd+"\n")

	store := certstore.New(infra)

	statuses, err := store.ListStatuses()
	if err != nil {
		t.Fatalf("ListStatuses: %v", err)
	}

	if len(statuses) != 1 || statuses[0].DomainName != testDomain || statuses[0].Mode != modeProd {
		t.Fatalf("statuses = %+v", statuses)
	}

	if !store.ProdReusable(testDomain, certstore.DefaultProdUpdateSec, time.Now().UTC()) {
		t.Fatal("expected migrated prod PEMs to be reusable")
	}

	_, statErr := os.Stat(filepath.Join(home, certstore.LegacyInstalledCert))
	if !os.IsNotExist(statErr) {
		t.Fatalf("legacy file should be removed, stat err=%v", statErr)
	}

	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat certs.json: %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("certs.json mode = %o, want 0600", info.Mode().Perm())
	}

	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}

	if !json.Valid(raw) || !strings.Contains(string(raw), `"active_mode"`) || !strings.Contains(string(raw), `"certs"`) {
		t.Fatalf("migrated file must be v2: %s", raw)
	}
}

func TestStore_MigrateFlatV1(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	infra := filepath.Join(home, "infra")
	mustMkdir(t, infra)

	now := time.Now().UTC()
	fullchain, privkey := mustSelfSignedPEM(t, now.Add(48*time.Hour))

	flatV1 := map[string]any{
		"accounts": map[string]any{},
		"certificates": map[string]any{
			testDomain: map[string]any{
				"issued_at":   now.Format(time.RFC3339Nano),
				"mode":        modeTest,
				"certificate": base64.StdEncoding.EncodeToString(fullchain),
				"private_key": base64.StdEncoding.EncodeToString(privkey),
			},
		},
	}

	payload, err := json.MarshalIndent(flatV1, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(home, certstore.FileName), string(payload)+"\n")

	store := certstore.New(infra)

	statuses, err := store.ListStatuses()
	if err != nil {
		t.Fatalf("ListStatuses: %v", err)
	}

	if len(statuses) != 1 || statuses[0].Mode != modeTest {
		t.Fatalf("statuses = %+v", statuses)
	}

	if !store.ProdReusable(testDomain, certstore.DefaultProdUpdateSec, now) {
		t.Fatal("v1 PEM must migrate into certs.prod")
	}

	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}

	body := string(raw)
	if !strings.Contains(body, `"active_mode": "test"`) || !strings.Contains(body, `"prod"`) {
		t.Fatalf("v1 rewrite incomplete: %s", raw)
	}
}

func TestStore_SaveStatusesKeepsProdIssuedAtAndOrphans(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	infra := filepath.Join(home, "infra")
	mustMkdir(t, infra)

	store := certstore.New(infra)
	now := time.Now().UTC()
	prodIssued := now.Add(-24 * time.Hour)
	fullchain, privkey := mustSelfSignedPEM(t, now.Add(48*time.Hour))

	err := store.PutProdMaterial(testDomain, "letsencrypt", fullchain, privkey)
	if err != nil {
		t.Fatalf("PutProdMaterial: %v", err)
	}

	_, err = store.SaveStatuses([]certstore.Status{{
		DomainName:    testDomain,
		IssueDateTime: prodIssued,
		Mode:          modeProd,
	}}, now)
	if err != nil {
		t.Fatalf("SaveStatuses prod issued: %v", err)
	}

	_, err = store.SaveStatuses([]certstore.Status{{
		DomainName:    testDomain,
		IssueDateTime: now,
		Mode:          modeTest,
	}}, now)
	if err != nil {
		t.Fatalf("SaveStatuses test mode: %v", err)
	}

	gotProd, ok, err := store.ProdIssuedAt(testDomain)
	if err != nil || !ok {
		t.Fatalf("ProdIssuedAt = (%v, %v, %v)", gotProd, ok, err)
	}

	if !gotProd.Equal(prodIssued) {
		t.Fatalf("prod issued_at = %v, want preserved %v", gotProd, prodIssued)
	}

	if !store.ProdReusable(testDomain, certstore.DefaultProdUpdateSec, now) {
		t.Fatal("prod blobs must survive mode=test")
	}

	_, err = store.SaveStatuses(nil, now)
	if err != nil {
		t.Fatalf("SaveStatuses empty: %v", err)
	}

	statuses, err := store.ListStatuses()
	if err != nil {
		t.Fatalf("ListStatuses: %v", err)
	}

	if len(statuses) != 1 || statuses[0].DomainName != testDomain {
		t.Fatalf("orphan prod entry missing: %+v", statuses)
	}
}

func TestStore_MaterializeProdUsesSoftTTL(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	infra := filepath.Join(home, "infra")
	mustMkdir(t, infra)

	store := certstore.New(infra)
	now := time.Now().UTC()
	fullchain, privkey := mustSelfSignedPEM(t, now.Add(24*time.Hour))

	err := store.PutProdMaterial(testDomain, "letsencrypt", fullchain, privkey)
	if err != nil {
		t.Fatalf("PutProdMaterial: %v", err)
	}

	before, ok, err := store.ProdIssuedAt(testDomain)
	if err != nil || !ok {
		t.Fatalf("ProdIssuedAt before = (%v, %v, %v)", before, ok, err)
	}

	_, err = store.SaveStatuses([]certstore.Status{{
		DomainName:    testDomain,
		IssueDateTime: now,
		Mode:          modeTest,
	}}, now)
	if err != nil {
		t.Fatal(err)
	}

	ok, err = store.MaterializeProd(testDomain, certstore.DefaultProdUpdateSec, now)
	if err != nil || !ok {
		t.Fatalf("MaterializeProd = (%v, %v)", ok, err)
	}

	live := filepath.Join(infra, "certbot", "conf", "live", testDomain)

	gotChain, err := os.ReadFile(filepath.Join(live, "fullchain.pem"))
	if err != nil {
		t.Fatalf("read materialized fullchain: %v", err)
	}

	if string(gotChain) != string(fullchain) {
		t.Fatal("materialized fullchain mismatch")
	}

	statuses, err := store.ListStatuses()
	if err != nil {
		t.Fatal(err)
	}

	if statuses[0].Mode != modeProd {
		t.Fatalf("active_mode after materialize = %q", statuses[0].Mode)
	}

	after, ok, err := store.ProdIssuedAt(testDomain)
	if err != nil || !ok {
		t.Fatal(err)
	}

	if !after.Equal(before) {
		t.Fatalf("materialize must not bump prod.issued_at: before=%v after=%v", before, after)
	}
}

func TestStore_ProdReusableExpiredSoftTTL(t *testing.T) {
	t.Parallel()

	store := certstore.New(filepath.Join(t.TempDir(), "infra"))
	now := time.Now().UTC()
	fullchain, privkey := mustSelfSignedPEM(t, now.Add(30*24*time.Hour))

	err := store.PutProdMaterial(testDomain, "letsencrypt", fullchain, privkey)
	if err != nil {
		t.Fatalf("PutProdMaterial: %v", err)
	}

	old := now.Add(-2 * time.Duration(certstore.DefaultProdUpdateSec) * time.Second)

	_, err = store.SaveStatuses([]certstore.Status{{
		DomainName:    testDomain,
		IssueDateTime: old,
		Mode:          modeProd,
	}}, now)
	if err != nil {
		t.Fatal(err)
	}

	if store.ProdReusable(testDomain, certstore.DefaultProdUpdateSec, now) {
		t.Fatal("expired soft TTL must not be reusable")
	}

	ok, err := store.MaterializeProd(testDomain, certstore.DefaultProdUpdateSec, now)
	if err != nil {
		t.Fatalf("MaterializeProd: %v", err)
	}

	if ok {
		t.Fatal("expired soft TTL must not materialize")
	}
}

func TestAccountKey(t *testing.T) {
	t.Parallel()

	got := certstore.AccountKey("LetsEncrypt", "Prod", " ops@example.com ")
	if got != "letsencrypt_prod_ops@example.com" {
		t.Fatalf("AccountKey = %q", got)
	}
}

func TestStore_PutGetAccount(t *testing.T) {
	t.Parallel()

	store := certstore.New(filepath.Join(t.TempDir(), "infra"))
	key := certstore.AccountKey("zerossl", "prod", "ops@example.com")
	acc := certstore.Account{
		Email:      "ops@example.com",
		URI:        "https://acme.zerossl.com/v2/DV90/account/1",
		PrivateKey: base64.StdEncoding.EncodeToString([]byte("-----BEGIN EC PRIVATE KEY-----\nM\n-----END EC PRIVATE KEY-----\n")),
	}

	err := store.PutAccount(key, acc)
	if err != nil {
		t.Fatalf("PutAccount: %v", err)
	}

	got, ok, err := store.GetAccount(key)
	if err != nil || !ok {
		t.Fatalf("GetAccount = (%v, %v, %v)", got, ok, err)
	}

	if got.Email != acc.Email || got.URI != acc.URI || got.PrivateKey != acc.PrivateKey {
		t.Fatalf("account mismatch: %+v", got)
	}
}

func mustSelfSignedPEM(t *testing.T, notAfter time.Time) ([]byte, []byte) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: testDomain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		DNSNames:     []string{testDomain},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}

	fullchain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privkey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	return fullchain, privkey
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()

	err := os.MkdirAll(path, 0o755)
	if err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
