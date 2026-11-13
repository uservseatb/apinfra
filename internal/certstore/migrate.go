package certstore

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/uservseatb/apinfra/internal/sys"
)

func (s *Store) migrateLegacyIfPresent() (*File, error) {
	legacyPath := filepath.Join(filepath.Dir(s.path), LegacyInstalledCert)

	data, err := os.ReadFile(legacyPath) // #nosec G304
	if err != nil {
		if os.IsNotExist(err) {
			return emptyFile(), nil
		}

		return nil, fmt.Errorf("read legacy %s: %w", legacyPath, err)
	}

	doc, err := s.migrateLegacyBytes(data)
	if err != nil {
		return nil, err
	}

	return doc, nil
}

func (s *Store) migrateLegacyBytes(data []byte) (*File, error) {
	props, err := sys.ParsePropertiesString(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse legacy %s: %w", LegacyInstalledCert, err)
	}

	doc := emptyFile()

	for id, val := range props {
		issuedAt, mode, parseErr := parseLegacyCertValue(val)
		if parseErr != nil {
			return nil, fmt.Errorf("legacy domain %s: %w", id, parseErr)
		}

		entry := DomainEntry{
			ActiveMode: mode,
			Certs:      map[string]TypeCert{},
		}

		if mode != "" {
			entry.Certs[mode] = TypeCert{IssuedAt: issuedAt.UTC()} //nolint:exhaustruct_v5
		}

		maybeAttachLiveProd(&entry, s.infraDir, id, issuedAt, mode)
		doc.Certificates[id] = entry
	}

	_, err = s.Save(doc)
	if err != nil {
		return nil, fmt.Errorf("write migrated %s: %w", s.path, err)
	}

	legacyPath := filepath.Join(filepath.Dir(s.path), LegacyInstalledCert)

	removeErr := os.Remove(legacyPath)
	if removeErr != nil && !os.IsNotExist(removeErr) {
		return doc, fmt.Errorf("remove legacy %s: %w", legacyPath, removeErr)
	}

	return doc, nil
}

func maybeAttachLiveProd(entry *DomainEntry, infraDir, id string, issuedAt time.Time, mode string) {
	if mode != modeProd && issuedModeOnDisk(infraDir, id) != modeProd {
		return
	}

	fullchain, privkey, readErr := readLivePEMs(infraDir, id)
	if readErr != nil || len(fullchain) == 0 || len(privkey) == 0 {
		return
	}

	ensureCerts(entry)

	prod := entry.Certs[modeProd]
	prod.IssuedAt = issuedAt.UTC()

	if prod.IssuedAt.IsZero() {
		prod.IssuedAt = time.Now().UTC()
	}

	prod.Certificate = encodePEM(fullchain)
	prod.PrivateKey = encodePEM(privkey)
	entry.Certs[modeProd] = prod

	if entry.ActiveMode == "" {
		entry.ActiveMode = modeProd
	}
}

func parseLegacyCertValue(v string) (time.Time, string, error) {
	mode := ""
	timePart := v

	if part, rest, ok := strings.Cut(v, "|"); ok {
		timePart = part
		mode = strings.TrimSpace(rest)
	}

	parsed, err := time.Parse(legacyCertTimeLayout, strings.TrimSpace(timePart))
	if err != nil {
		return time.Time{}, "", fmt.Errorf("parse cert time: %w", err)
	}

	return parsed, mode, nil
}

func readLivePEMs(infraDir, domain string) ([]byte, []byte, error) {
	dir := liveCertDir(infraDir, domain)

	fullchain, err := os.ReadFile(filepath.Join(dir, fullchainPEM)) // #nosec G304
	if err != nil {
		return nil, nil, fmt.Errorf("read fullchain: %w", err)
	}

	privkey, err := os.ReadFile(filepath.Join(dir, privkeyPEM)) // #nosec G304
	if err != nil {
		return nil, nil, fmt.Errorf("read privkey: %w", err)
	}

	return fullchain, privkey, nil
}

func issuedModeOnDisk(infraDir, domain string) string {
	data, err := os.ReadFile(filepath.Join(liveCertDir(infraDir, domain), issuedModeFile)) // #nosec G304
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(data))
}

func encodePEM(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}
