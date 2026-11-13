package acme

import (
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	legoacme "github.com/go-acme/lego/v4/acme"
	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/http/webroot"
	"github.com/go-acme/lego/v4/registration"
	"github.com/uservseatb/apinfra/internal/certstore"
)

const (
	accountKeyFile     = "private.key"
	accountRegFile     = "registration.json"
	defaultObtainLimit = time.Minute

	defaultDowngradeCertModeHours = 24
)

// acmeUser implements registration.User for the lego ACME client.
type acmeUser struct {
	email        string
	registration *registration.Resource
	key          crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string {
	return u.email
}

func (u *acmeUser) GetRegistration() *registration.Resource {
	return u.registration
}

func (u *acmeUser) GetPrivateKey() crypto.PrivateKey {
	return u.key
}

// obtainCert issues a certificate via lego (HTTP-01 webroot) using prov
// and writes fullchain.pem / privkey.pem under infra/certbot/conf/live/<domain>/.
func obtainCert(
	ctx context.Context,
	files fileWriter,
	accounts *certstore.Store,
	infraDirPath, domainName, email, mode, providerID string,
	acmeTimeout time.Duration,
	prov provider,
) error {
	if domainName == "" {
		return ErrEmptyDomain
	}

	if email == "" {
		return fmt.Errorf("%w for domain %s", ErrEmailRequired, domainName)
	}

	if acmeTimeout <= 0 {
		acmeTimeout = defaultObtainLimit
	}

	caURL := prov.directoryURL(mode)
	webrootPath := filepath.Join(infraDirPath, "certbot", "www")

	err := os.MkdirAll(webrootPath, dirModeTraversable)
	if err != nil { // #nosec G301 -- nginx serves ACME tokens from this tree
		return fmt.Errorf("mkdir webroot: %w", err)
	}

	accountKey := acmeAccountKey(providerID, mode, email)

	user, err := loadOrCreateUser(files, accounts, infraDirPath, email, caURL, accountKey)
	if err != nil {
		return err
	}

	var certs *certificate.Resource

	err = runWithTimeout(ctx, acmeTimeout, func() error {
		obtained, obtainErr := obtainResource(
			files, accounts, user, caURL, webrootPath, infraDirPath, domainName, accountKey, acmeTimeout, prov,
		)
		if obtainErr != nil {
			return obtainErr
		}

		certs = obtained

		return nil
	})
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return fmt.Errorf("obtain certificate for %s: %w", domainName, err)
		}

		return err
	}

	return writeACMECertPEMs(files, infraDirPath, domainName, mode, certs)
}

func obtainResource(
	files fileWriter,
	accounts *certstore.Store,
	user *acmeUser,
	caURL, webrootPath, infraDirPath, domainName, accountKey string,
	acmeTimeout time.Duration,
	prov provider,
) (*certificate.Resource, error) {
	client, err := newLegoClient(user, caURL, webrootPath, acmeTimeout)
	if err != nil {
		return nil, err
	}

	err = ensureRegistration(files, accounts, client, user, infraDirPath, caURL, accountKey, prov)
	if err != nil {
		return nil, err
	}

	//nolint:exhaustruct_v5 // only Domains/Bundle required for Obtain
	obtained, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: []string{domainName},
		Bundle:  true,
	})
	if err != nil {
		if IsRateLimited(err) {
			return nil, fmt.Errorf("%w: obtain certificate for %s: %w", ErrRateLimited, domainName, err)
		}

		return nil, fmt.Errorf("obtain certificate for %s: %w", domainName, err)
	}

	return obtained, nil
}

// obtainMu serializes in-flight ACME Obtain calls on the shared HTTP-01 webroot.
// On timeout the mutex stays held until the leaked Obtain finishes so the next
// attempt (fallback CA / another domain) cannot race HTTP-01 on the same webroot.
//
//nolint:gochecknoglobals // package-level lock for shared ACME HTTP-01 webroot
var obtainMu sync.Mutex

// runWithTimeout bounds wall-clock ACME work. Lego Obtain has no Context API and may
// hang on HTTP-01 polling, so issue runs in a goroutine while the deadline is context.WithTimeout.
func runWithTimeout(parent context.Context, timeout time.Duration, issue func() error) error {
	if timeout <= 0 {
		timeout = defaultObtainLimit
	}

	if parent == nil {
		return errNilContext
	}

	err := parent.Err()
	if err != nil {
		return fmt.Errorf("acme issue context: %w", err)
	}

	obtainMu.Lock()

	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- issue()
	}()

	select {
	case err := <-done:
		obtainMu.Unlock()

		return err
	case <-ctx.Done():
		// Keep the mutex until Obtain finishes so a timed-out call cannot race the
		// next attempt on the shared HTTP-01 webroot.
		go func() {
			<-done //nolint:errcheck // abandoned Obtain result is discarded after timeout
			obtainMu.Unlock()
		}()

		ctxErr := ctx.Err()
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return fmt.Errorf("%w after %v", ErrTimeout, timeout)
		}

		return fmt.Errorf("acme issue canceled: %w", ctxErr)
	}
}

// IsRateLimited reports whether err is an ACME rate limit / 429.
func IsRateLimited(err error) bool {
	_, ok := RateLimitCooldown(err, 0)

	return ok
}

// RateLimitCooldown reports whether err is an ACME rate limit / 429.
// Cooldown is configuredTTL, or defaultDowngradeCertModeHours when configuredTTL <= 0;
// no Retry-After parsing.
func RateLimitCooldown(err error, configuredTTL time.Duration) (time.Duration, bool) {
	if err == nil || !isRateLimitError(err) {
		return 0, false
	}

	if configuredTTL <= 0 {
		configuredTTL = time.Duration(defaultDowngradeCertModeHours) * time.Hour
	}

	return configuredTTL, true
}

func isRateLimitError(err error) bool {
	if _, ok := errors.AsType[*legoacme.RateLimitedError](err); ok {
		return true
	}

	if errors.Is(err, ErrRateLimited) {
		return true
	}

	problem, ok := errors.AsType[*legoacme.ProblemDetails](err)
	if ok && (problem.Type == legoacme.RateLimitedErr || problem.HTTPStatus == http.StatusTooManyRequests) {
		return true
	}

	msg := strings.ToLower(err.Error())

	return strings.Contains(msg, "ratelimited") ||
		strings.Contains(msg, "rate limited") ||
		strings.Contains(msg, "urn:ietf:params:acme:error:ratelimited")
}

func newLegoClient(user *acmeUser, caURL, webrootPath string, acmeTimeout time.Duration) (*lego.Client, error) {
	cfg := lego.NewConfig(user)
	cfg.CADirURL = caURL
	cfg.Certificate.KeyType = certcrypto.RSA2048
	applyObtainHTTPTimeout(cfg, acmeTimeout)

	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create ACME client: %w", err)
	}

	httpProvider, err := webroot.NewHTTPProvider(webrootPath)
	if err != nil {
		return nil, fmt.Errorf("webroot provider: %w", err)
	}

	err = client.Challenge.SetHTTP01Provider(httpProvider)
	if err != nil {
		return nil, fmt.Errorf("set HTTP-01 provider: %w", err)
	}

	return client, nil
}

// applyObtainHTTPTimeout sets certificate and HTTP client timeouts without replacing
// lego's default Transport (so LEGO_CA_CERTIFICATES / RootCAs still apply).
func applyObtainHTTPTimeout(cfg *lego.Config, acmeTimeout time.Duration) {
	if cfg == nil {
		return
	}

	cfg.Certificate.Timeout = acmeTimeout
	if cfg.HTTPClient != nil {
		cfg.HTTPClient.Timeout = acmeTimeout
	}
}

func ensureRegistration(
	files fileWriter,
	accounts *certstore.Store,
	client *lego.Client,
	user *acmeUser,
	infraDirPath, caURL, accountKey string,
	prov provider,
) error {
	if user.registration != nil {
		return nil
	}

	reg, err := client.Registration.ResolveAccountByKey()
	if err != nil {
		reg, err = prov.register(client.Registration)
		if err != nil {
			return fmt.Errorf("register ACME account: %w", err)
		}
	}

	user.registration = reg

	return saveUser(files, accounts, infraDirPath, user, caURL, accountKey)
}

func loadOrCreateUser(
	files fileWriter,
	accounts *certstore.Store,
	infraDirPath, email, caURL, accountKey string,
) (*acmeUser, error) {
	if accounts != nil {
		user, ok, err := loadUserFromStore(accounts, email, accountKey)
		if err != nil {
			return nil, err
		}

		if ok {
			return user, nil
		}
	}

	dir := accountDir(infraDirPath, email, caURL)
	keyPath := filepath.Join(dir, accountKeyFile)
	regPath := filepath.Join(dir, accountRegFile)

	keyPEM, err := os.ReadFile(keyPath) // #nosec G304 -- path under infraDirPath
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read ACME account key: %w", err)
		}

		return createUser(files, accounts, infraDirPath, email, caURL, accountKey)
	}

	key, err := certcrypto.ParsePEMPrivateKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse ACME account key: %w", err)
	}

	user := &acmeUser{email: email, key: key, registration: nil}

	regJSON, err := os.ReadFile(regPath) // #nosec G304 -- path under infraDirPath
	if err != nil {
		if os.IsNotExist(err) {
			_ = saveUser(files, accounts, infraDirPath, user, caURL, accountKey)

			return user, nil
		}

		return nil, fmt.Errorf("read ACME registration: %w", err)
	}

	var reg registration.Resource

	err = json.Unmarshal(regJSON, &reg)
	if err != nil {
		return nil, fmt.Errorf("parse ACME registration: %w", err)
	}

	user.registration = &reg

	// One-shot migrate legacy disk account into ~/.certs.json.
	_ = saveUser(files, accounts, infraDirPath, user, caURL, accountKey)

	return user, nil
}

func loadUserFromStore(accounts *certstore.Store, email, accountKey string) (*acmeUser, bool, error) {
	acc, ok, err := accounts.GetAccount(accountKey)
	if err != nil {
		return nil, false, fmt.Errorf("load ACME account %s: %w", accountKey, err)
	}

	if !ok {
		return nil, false, nil
	}

	keyPEM, err := base64.StdEncoding.DecodeString(acc.PrivateKey)
	if err != nil {
		return nil, false, fmt.Errorf("decode ACME account key: %w", err)
	}

	key, err := certcrypto.ParsePEMPrivateKey(keyPEM)
	if err != nil {
		return nil, false, fmt.Errorf("parse ACME account key: %w", err)
	}

	// Leave registration nil so ensureRegistration can ResolveAccountByKey.
	return &acmeUser{email: email, key: key, registration: nil}, true, nil
}

func createUser(
	files fileWriter,
	accounts *certstore.Store,
	infraDirPath, email, caURL, accountKey string,
) (*acmeUser, error) {
	key, err := certcrypto.GeneratePrivateKey(certcrypto.EC256)
	if err != nil {
		return nil, fmt.Errorf("generate ACME account key: %w", err)
	}

	user := &acmeUser{email: email, key: key, registration: nil}

	err = saveUser(files, accounts, infraDirPath, user, caURL, accountKey)
	if err != nil {
		return nil, err
	}

	return user, nil
}

func saveUser(
	files fileWriter,
	accounts *certstore.Store,
	infraDirPath string,
	user *acmeUser,
	caURL, accountKey string,
) error {
	keyPEM := certcrypto.PEMEncode(user.key)

	if accounts != nil && accountKey != "" {
		uri := ""
		if user.registration != nil {
			uri = user.registration.URI
		}

		err := accounts.PutAccount(accountKey, certstore.Account{
			Email:      user.email,
			URI:        uri,
			PrivateKey: base64.StdEncoding.EncodeToString(keyPEM),
		})
		if err != nil {
			return fmt.Errorf("persist ACME account: %w", err)
		}

		return nil
	}

	dir := accountDir(infraDirPath, user.email, caURL)

	err := os.MkdirAll(dir, dirModeTraversable)
	if err != nil { // #nosec G301
		return fmt.Errorf("mkdir ACME account dir: %w", err)
	}

	_, err = files.WriteFile(filepath.Join(dir, accountKeyFile), string(keyPEM), fileModePrivate)
	if err != nil {
		return fmt.Errorf("write ACME account key: %w", err)
	}

	if user.registration == nil {
		return nil
	}

	regJSON, err := json.MarshalIndent(user.registration, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ACME registration: %w", err)
	}

	_, err = files.WriteFile(filepath.Join(dir, accountRegFile), string(regJSON), fileModePrivate)
	if err != nil {
		return fmt.Errorf("write ACME registration: %w", err)
	}

	return nil
}

func acmeAccountKey(providerID, mode, email string) string {
	kind := ModeProd
	if providerID == providerLetsEncrypt && mode == ModeTest {
		kind = ModeTest
	}

	if providerID == "" {
		providerID = providerLetsEncrypt
	}

	return certstore.AccountKey(providerID, kind, email)
}

func accountDir(infraDirPath, email, caURL string) string {
	server := strings.TrimPrefix(caURL, "https://")
	server = strings.TrimPrefix(server, "http://")
	server = strings.NewReplacer("/", "-", ":", "-").Replace(server)

	return filepath.Join(infraDirPath, "certbot", "conf", "accounts", server, email)
}

func writeACMECertPEMs(
	files fileWriter,
	infraDirPath, domainName, mode string,
	certs *certificate.Resource,
) error {
	if certs == nil || len(certs.Certificate) == 0 || len(certs.PrivateKey) == 0 {
		return fmt.Errorf("%w for %s", errEmptyCertificate, domainName)
	}

	// Prod and test share the live path; mode switch overwrites PEMs in place.
	paths := NewPaths(infraDirPath, domainName, ModeProd)

	err := os.MkdirAll(filepath.Dir(paths.Fullchain), dirModeTraversable)
	if err != nil { // #nosec G301 -- nginx must traverse live/<domain>
		return fmt.Errorf("mkdir cert dir: %w", err)
	}

	_, err = files.Write(paths.Fullchain, string(certs.Certificate))
	if err != nil {
		return fmt.Errorf("write %s: %w", paths.Fullchain, err)
	}

	_, err = files.WriteFile(paths.Privkey, string(certs.PrivateKey), fileModePrivate)
	if err != nil {
		return fmt.Errorf("write %s: %w", paths.Privkey, err)
	}

	err = writeIssuedMode(files, filepath.Dir(paths.Fullchain), mode)
	if err != nil {
		return err
	}

	return nil
}

func writeIssuedMode(files fileWriter, certDir, mode string) error {
	path := filepath.Join(certDir, issuedModeFile)

	_, err := files.WriteFile(path, mode+"\n", fileModePrivate)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}
