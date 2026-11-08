package acme

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	mockCertValidity = 10 * 365 * 24 * time.Hour
	mockRSAKeyBits   = 2048
	mockSerialBits   = 128
)

// writeMockCert writes a self-signed certificate under infra/mockcerts/<domain>/.
func writeMockCert(files fileWriter, infraDirPath, domainName string) error {
	if domainName == "" {
		return ErrEmptyDomain
	}

	key, err := rsa.GenerateKey(rand.Reader, mockRSAKeyBits)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), mockSerialBits))
	if err != nil {
		return fmt.Errorf("serial: %w", err)
	}

	tmpl := newMockCertificateTemplate(serial, domainName, time.Now().UTC())

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	dir := certHostDir(infraDirPath, domainName, ModeMock)

	err = os.MkdirAll(dir, dirModeTraversable)
	if err != nil { // #nosec G301 -- nginx must traverse mockcerts/
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der, Headers: nil})
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:    "RSA PRIVATE KEY",
		Bytes:   x509.MarshalPKCS1PrivateKey(key),
		Headers: nil,
	})

	paths := NewPaths(infraDirPath, domainName, ModeMock)

	_, err = files.Write(paths.Fullchain, string(certPEM))
	if err != nil {
		return fmt.Errorf("write %s: %w", paths.Fullchain, err)
	}

	_, err = files.WriteFile(paths.Privkey, string(keyPEM), fileModePrivate)
	if err != nil {
		return fmt.Errorf("write %s: %w", paths.Privkey, err)
	}

	err = writeIssuedMode(files, filepath.Dir(paths.Fullchain), ModeMock)
	if err != nil {
		return err
	}

	return nil
}

func newMockCertificateTemplate(serial *big.Int, domainName string, now time.Time) *x509.Certificate {
	//nolint:exhaustruct_v5 // only fields required for a self-signed server cert
	return &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: domainName,
		},
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    now.Add(mockCertValidity),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{domainName},
	}
}
