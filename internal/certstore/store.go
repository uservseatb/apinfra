// Package certstore persists certificate status, prod PEM cache, and ACME accounts
// in a single host JSON file (~/.certs.json).
package certstore

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// FileName is the host state file next to the infra directory.
	FileName = ".certs.json"
	// LegacyInstalledCert is the pre-JSON properties file.
	LegacyInstalledCert = ".installedCert"

	fileModePrivate    = 0o600
	dirModeTraversable = 0o755

	modeProd = "prod"

	fullchainPEM   = "fullchain.pem"
	privkeyPEM     = "privkey.pem"
	issuedModeFile = ".issued-mode"

	legacyCertTimeLayout = "2006-01-02T15:04:05.000"

	// DefaultProdUpdateSec is the default soft TTL for orphan prod retention (2 months).
	DefaultProdUpdateSec = int64(5184000)
)

var (
	errEmptyCertIdentity = errors.New("empty cert identity")
	errEmptyProdMaterial = errors.New("empty prod PEM material")
	errEmptyAccountKey   = errors.New("empty account key")
)

// Store reads and writes ~/.certs.json for one infra tree.
type Store struct {
	path     string
	infraDir string
}

// New builds a Store rooted at infraDir (JSON lives in Dir(infraDir)).
func New(infraDir string) *Store {
	clean := filepath.Clean(infraDir)

	return &Store{
		path:     filepath.Join(filepath.Dir(clean), FileName),
		infraDir: clean,
	}
}

// Path returns the absolute path of the JSON file.
func (s *Store) Path() string {
	return s.path
}

// InfraDir returns the infra directory this store materializes PEMs into.
func (s *Store) InfraDir() string {
	return s.infraDir
}

// File is the on-disk JSON document (v2).
type File struct {
	Accounts     map[string]Account     `json:"accounts"`
	Certificates map[string]DomainEntry `json:"certificates"`
}

// Account is a persisted ACME account (lego registration.User).
type Account struct {
	Email      string `json:"email"`
	URI        string `json:"uri"`
	PrivateKey string `json:"private_key"` // base64 of PEM
}

// DomainEntry is per-identity active mode plus per-type certificate metadata.
type DomainEntry struct {
	ActiveMode string              `json:"active_mode"`
	Certs      map[string]TypeCert `json:"certs"`
}

// TypeCert is one certificate type under a domain (PEM only for prod).
type TypeCert struct {
	IssuedAt    time.Time `json:"issued_at"`
	Provider    string    `json:"provider,omitempty"`
	Certificate string    `json:"certificate,omitempty"` // base64 fullchain; prod only
	PrivateKey  string    `json:"private_key,omitempty"` // base64 privkey; prod only
}

// Status is the active-mode view used by nginx.
type Status struct {
	DomainName    string
	IssueDateTime time.Time
	Mode          string // active_mode
}

// Load reads the store, migrating legacy / v1 formats when needed.
func (s *Store) Load() (*File, error) {
	data, err := os.ReadFile(s.path) // #nosec G304 -- host state next to infra
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s: %w", s.path, err)
		}

		migrated, migErr := s.migrateLegacyIfPresent()
		if migErr != nil {
			return nil, migErr
		}

		return migrated, nil
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return emptyFile(), nil
	}

	if !json.Valid(trimmed) {
		migrated, migErr := s.migrateLegacyBytes(trimmed)
		if migErr != nil {
			return nil, migErr
		}

		return migrated, nil
	}

	doc, err := parseCertsJSON(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}

	if needsV1Rewrite(trimmed, doc) {
		_, saveErr := s.Save(doc)
		if saveErr != nil {
			return nil, fmt.Errorf("rewrite migrated %s: %w", s.path, saveErr)
		}
	}

	return doc, nil
}

// Save writes the file atomically with mode 0600.
func (s *Store) Save(doc *File) (bool, error) {
	if doc == nil {
		doc = emptyFile()
	}

	normalizeFile(doc)

	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal %s: %w", s.path, err)
	}

	payload = append(payload, '\n')

	existing, readErr := os.ReadFile(s.path) // #nosec G304
	if readErr == nil && bytes.Equal(existing, payload) {
		chmodErr := os.Chmod(s.path, fileModePrivate)
		if chmodErr != nil {
			return false, fmt.Errorf("chmod %s: %w", s.path, chmodErr)
		}

		return false, nil
	}

	err = atomicWrite(s.path, payload, fileModePrivate)
	if err != nil {
		return false, err
	}

	return true, nil
}

// ListStatuses returns active_mode rows (including orphaned prod cache domains).
func (s *Store) ListStatuses() ([]Status, error) {
	doc, err := s.Load()
	if err != nil {
		return nil, err
	}

	out := make([]Status, 0, len(doc.Certificates))
	for id, entry := range doc.Certificates {
		out = append(out, statusFromEntry(id, entry))
	}

	return out, nil
}

// SaveStatuses upserts active_mode + that type's issued_at; keeps other types and prod PEMs.
// Orphans not in statuses are kept only while prod soft TTL is still valid.
func (s *Store) SaveStatuses(statuses []Status, now time.Time) (bool, error) {
	doc, err := s.Load()
	if err != nil {
		return false, err
	}

	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	desired := make(map[string]Status, len(statuses))
	for _, st := range statuses {
		if st.DomainName == "" {
			continue
		}

		desired[st.DomainName] = st
	}

	next := make(map[string]DomainEntry, len(desired)+len(doc.Certificates))

	for id, st := range desired {
		entry := doc.Certificates[id]
		applyStatus(&entry, st)
		next[id] = entry
	}

	for id, entry := range doc.Certificates {
		if _, ok := next[id]; ok {
			continue
		}

		if prodReusable(entry, DefaultProdUpdateSec, now) {
			next[id] = entry
		}
	}

	doc.Certificates = next

	return s.Save(doc)
}

// PutProdMaterial stores prod PEMs and sets prod.issued_at=now; active_mode becomes prod.
func (s *Store) PutProdMaterial(id, provider string, fullchain, privkey []byte) error {
	if id == "" {
		return errEmptyCertIdentity
	}

	if len(fullchain) == 0 || len(privkey) == 0 {
		return fmt.Errorf("%w for %s", errEmptyProdMaterial, id)
	}

	doc, err := s.Load()
	if err != nil {
		return err
	}

	entry := doc.Certificates[id]
	ensureCerts(&entry)

	prod := entry.Certs[modeProd]
	prod.Provider = provider
	prod.Certificate = base64.StdEncoding.EncodeToString(fullchain)
	prod.PrivateKey = base64.StdEncoding.EncodeToString(privkey)
	prod.IssuedAt = time.Now().UTC()
	entry.Certs[modeProd] = prod
	entry.ActiveMode = modeProd

	doc.Certificates[id] = entry

	_, err = s.Save(doc)

	return err
}

// PutProdMaterialFromDisk reads live/ PEMs and stores them as prod cache.
func (s *Store) PutProdMaterialFromDisk(id, provider string) error {
	dir := liveCertDir(s.infraDir, id)

	fullchain, err := os.ReadFile(filepath.Join(dir, fullchainPEM)) // #nosec G304
	if err != nil {
		return fmt.Errorf("read fullchain for %s: %w", id, err)
	}

	privkey, err := os.ReadFile(filepath.Join(dir, privkeyPEM)) // #nosec G304
	if err != nil {
		return fmt.Errorf("read privkey for %s: %w", id, err)
	}

	return s.PutProdMaterial(id, provider, fullchain, privkey)
}

// ProdReusable reports whether prod PEM exists and soft TTL (issued_at + updateSec) is still valid.
func (s *Store) ProdReusable(id string, updateSec int64, now time.Time) bool {
	doc, err := s.Load()
	if err != nil {
		return false
	}

	if now.IsZero() {
		now = time.Now().UTC()
	}

	if updateSec <= 0 {
		updateSec = DefaultProdUpdateSec
	}

	return prodReusable(doc.Certificates[id], updateSec, now.UTC())
}

// MaterializeProd writes cached prod PEMs to live/ when soft TTL allows.
// Sets active_mode=prod without changing prod.issued_at.
func (s *Store) MaterializeProd(id string, updateSec int64, now time.Time) (bool, error) {
	doc, err := s.Load()
	if err != nil {
		return false, err
	}

	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	if updateSec <= 0 {
		updateSec = DefaultProdUpdateSec
	}

	entry, ok := doc.Certificates[id]
	if !ok || !prodReusable(entry, updateSec, now) {
		return false, nil
	}

	prod := entry.Certs[modeProd]

	fullchain, err := base64.StdEncoding.DecodeString(prod.Certificate)
	if err != nil {
		return false, fmt.Errorf("decode fullchain for %s: %w", id, err)
	}

	privkey, err := base64.StdEncoding.DecodeString(prod.PrivateKey)
	if err != nil {
		return false, fmt.Errorf("decode privkey for %s: %w", id, err)
	}

	dir := liveCertDir(s.infraDir, id)

	err = os.MkdirAll(dir, dirModeTraversable) // #nosec G301
	if err != nil {
		return false, fmt.Errorf("mkdir %s: %w", dir, err)
	}

	err = os.WriteFile(filepath.Join(dir, fullchainPEM), fullchain, fileModePrivate) // #nosec G306
	if err != nil {
		return false, fmt.Errorf("write fullchain for %s: %w", id, err)
	}

	err = os.WriteFile(filepath.Join(dir, privkeyPEM), privkey, fileModePrivate) // #nosec G306
	if err != nil {
		return false, fmt.Errorf("write privkey for %s: %w", id, err)
	}

	err = os.WriteFile(filepath.Join(dir, issuedModeFile), []byte(modeProd+"\n"), fileModePrivate) // #nosec G306
	if err != nil {
		return false, fmt.Errorf("write issued-mode for %s: %w", id, err)
	}

	entry.ActiveMode = modeProd
	doc.Certificates[id] = entry

	_, err = s.Save(doc)
	if err != nil {
		return false, err
	}

	return true, nil
}

// ProdIssuedAt returns certs.prod.issued_at when present.
func (s *Store) ProdIssuedAt(id string) (time.Time, bool, error) {
	doc, err := s.Load()
	if err != nil {
		return time.Time{}, false, err
	}

	entry, ok := doc.Certificates[id]
	if !ok || entry.Certs == nil {
		return time.Time{}, false, nil
	}

	prod, ok := entry.Certs[modeProd]
	if !ok || prod.IssuedAt.IsZero() {
		return time.Time{}, false, nil
	}

	return prod.IssuedAt.UTC(), true, nil
}

// GetAccount returns a persisted ACME account by key.
func (s *Store) GetAccount(key string) (Account, bool, error) {
	doc, err := s.Load()
	if err != nil {
		return Account{}, false, err
	}

	acc, ok := doc.Accounts[key]
	if !ok || acc.PrivateKey == "" {
		return Account{}, false, nil //nolint:exhaustruct_v5 // empty Account is the not-found value
	}

	return acc, true, nil
}

// PutAccount stores an ACME account.
func (s *Store) PutAccount(key string, acc Account) error {
	if key == "" {
		return errEmptyAccountKey
	}

	doc, err := s.Load()
	if err != nil {
		return err
	}

	doc.Accounts[key] = acc

	_, err = s.Save(doc)

	return err
}

// AccountKey builds accounts map key: <provider>_<kind>_<email>.
func AccountKey(provider, kind, email string) string {
	return strings.ToLower(provider) + "_" + strings.ToLower(kind) + "_" + strings.TrimSpace(email)
}

func emptyFile() *File {
	return &File{
		Accounts:     map[string]Account{},
		Certificates: map[string]DomainEntry{},
	}
}

func normalizeFile(doc *File) {
	if doc.Accounts == nil {
		doc.Accounts = map[string]Account{}
	}

	if doc.Certificates == nil {
		doc.Certificates = map[string]DomainEntry{}
	}

	for id, entry := range doc.Certificates {
		ensureCerts(&entry)
		doc.Certificates[id] = entry
	}
}

func ensureCerts(entry *DomainEntry) {
	if entry.Certs == nil {
		entry.Certs = map[string]TypeCert{}
	}
}

func applyStatus(entry *DomainEntry, status Status) {
	ensureCerts(entry)
	entry.ActiveMode = status.Mode

	typeCert := entry.Certs[status.Mode]
	typeCert.IssuedAt = status.IssueDateTime.UTC()

	if status.Mode != modeProd {
		typeCert.Certificate = ""
		typeCert.PrivateKey = ""
		typeCert.Provider = ""
	}

	entry.Certs[status.Mode] = typeCert
}

func statusFromEntry(id string, entry DomainEntry) Status {
	mode := entry.ActiveMode
	issued := time.Time{}

	if entry.Certs != nil {
		if tc, ok := entry.Certs[mode]; ok {
			issued = tc.IssuedAt.UTC()
		}
	}

	return Status{
		DomainName:    id,
		IssueDateTime: issued,
		Mode:          mode,
	}
}

func prodReusable(entry DomainEntry, updateSec int64, now time.Time) bool {
	if entry.Certs == nil {
		return false
	}

	prod, ok := entry.Certs[modeProd]
	if !ok || prod.Certificate == "" || prod.PrivateKey == "" || prod.IssuedAt.IsZero() {
		return false
	}

	expiresAt := prod.IssuedAt.UTC().Add(time.Duration(updateSec) * time.Second)

	return expiresAt.After(now)
}

func liveCertDir(infraDir, domain string) string {
	return filepath.Join(infraDir, "certbot", "conf", "live", domain)
}

func atomicWrite(path string, payload []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	err := os.MkdirAll(dir, dirModeTraversable) // #nosec G301
	if err != nil {
		return fmt.Errorf("mkdir for %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", path, err)
	}

	tmpName := tmp.Name()

	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	_, err = tmp.Write(payload)
	if err != nil {
		cleanup()

		return fmt.Errorf("write temp %s: %w", tmpName, err)
	}

	err = tmp.Chmod(perm)
	if err != nil {
		cleanup()

		return fmt.Errorf("chmod temp %s: %w", tmpName, err)
	}

	err = tmp.Close()
	if err != nil {
		_ = os.Remove(tmpName)

		return fmt.Errorf("close temp %s: %w", tmpName, err)
	}

	err = os.Rename(tmpName, path)
	if err != nil {
		_ = os.Remove(tmpName)

		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}

	return nil
}

// flatV1Entry is the pre-active_mode certificate shape.
type flatV1Entry struct {
	IssuedAt    time.Time `json:"issued_at"`
	Mode        string    `json:"mode"`
	Provider    string    `json:"provider,omitempty"`
	Certificate string    `json:"certificate,omitempty"`
	PrivateKey  string    `json:"private_key,omitempty"`
}

func parseCertsJSON(data []byte) (*File, error) {
	var raw struct {
		Accounts     map[string]Account         `json:"accounts"`
		Certificates map[string]json.RawMessage `json:"certificates"`
	}

	err := json.Unmarshal(data, &raw)
	if err != nil {
		return nil, fmt.Errorf("unmarshal certs.json: %w", err)
	}

	doc := emptyFile()
	if raw.Accounts != nil {
		doc.Accounts = raw.Accounts
	}

	for id, rawEntry := range raw.Certificates {
		entry, parseErr := parseDomainEntry(rawEntry)
		if parseErr != nil {
			return nil, fmt.Errorf("domain %s: %w", id, parseErr)
		}

		doc.Certificates[id] = entry
	}

	normalizeFile(doc)

	return doc, nil
}

func parseDomainEntry(raw json.RawMessage) (DomainEntry, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return DomainEntry{}, nil //nolint:exhaustruct_v5
	}

	if isV2DomainEntry(trimmed) {
		var entry DomainEntry

		err := json.Unmarshal(trimmed, &entry)
		if err != nil {
			return DomainEntry{}, fmt.Errorf("unmarshal domain entry: %w", err)
		}

		return entry, nil
	}

	var flat flatV1Entry

	err := json.Unmarshal(trimmed, &flat)
	if err != nil {
		return DomainEntry{}, fmt.Errorf("unmarshal flat v1 entry: %w", err)
	}

	return domainFromV1(flat), nil
}

func isV2DomainEntry(raw json.RawMessage) bool {
	var probe map[string]json.RawMessage

	err := json.Unmarshal(raw, &probe)
	if err != nil {
		return false
	}

	_, hasActive := probe["active_mode"]
	_, hasCerts := probe["certs"]

	return hasActive || hasCerts
}

func domainFromV1(flat flatV1Entry) DomainEntry {
	mode := flat.Mode
	if mode == "" && (flat.Certificate != "" || flat.PrivateKey != "") {
		mode = modeProd
	}

	entry := DomainEntry{
		ActiveMode: mode,
		Certs:      map[string]TypeCert{},
	}

	if mode != "" {
		tc := TypeCert{IssuedAt: flat.IssuedAt.UTC()} //nolint:exhaustruct_v5
		entry.Certs[mode] = tc
	}

	if flat.Certificate != "" && flat.PrivateKey != "" {
		prod := entry.Certs[modeProd]
		prod.IssuedAt = flat.IssuedAt.UTC()

		if prod.IssuedAt.IsZero() {
			prod.IssuedAt = time.Now().UTC()
		}

		prod.Provider = flat.Provider
		prod.Certificate = flat.Certificate
		prod.PrivateKey = flat.PrivateKey
		entry.Certs[modeProd] = prod

		if entry.ActiveMode == "" {
			entry.ActiveMode = modeProd
		}
	}

	return entry
}

func needsV1Rewrite(raw []byte, doc *File) bool {
	var probe struct {
		Certificates map[string]json.RawMessage `json:"certificates"`
	}

	err := json.Unmarshal(raw, &probe)
	if err != nil || len(probe.Certificates) == 0 {
		return false
	}

	for _, entryRaw := range probe.Certificates {
		if !isV2DomainEntry(entryRaw) {
			return true
		}
	}

	_ = doc

	return false
}
