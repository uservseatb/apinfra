package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	InfraDirName = "infra"

	hostConfigFileName = ".apinfra"
	hostEnvFileName    = ".env"
	ownerRWFileMode    = 0o600
	defaultRepoBranch  = "main"

	// Short ops (git/login/ACME).
	defaultShortTimeout = time.Minute

	defaultDockerAPITimeout      = 30 * time.Second
	defaultNginxCLITimeout       = 30 * time.Second
	defaultDomainPrecheckTimeout = 15 * time.Second
	defaultProxyReadyAfter       = 30 * time.Second

	// Compose / prune can legitimately run longer than ACME/git.
	defaultComposePullTimeout = 10 * time.Minute
	defaultComposeUpTimeout   = 5 * time.Minute
	defaultComposeDownTimeout = 2 * time.Minute
	defaultPruneTimeout       = 2 * time.Minute

	keyGitTimeout                  = "gitTimeout"
	keyDockerLoginTimeout          = "dockerLoginTimeout"
	keyDockerComposePullTimeout    = "dockerComposePullTimeout"
	keyDockerComposeUpTimeout      = "dockerComposeUpTimeout"
	keyDockerComposeDownTimeout    = "dockerComposeDownTimeout"
	keyDockerPruneTimeout          = "dockerPruneTimeout"
	keyDockerAPITimeout            = "dockerAPITimeout"
	keyNginxCLITimeout             = "nginxCLITimeout"
	keyDomainPrecheckTimeout       = "domainPrecheckTimeout"
	keyProxyReadyAfter             = "proxyReadyAfter"
	keyCertIssueTimeout            = "certIssueTimeout"
	keyCertsProvider               = "certs.provider"
	keyCertsPublicIP               = "certs.publicIP"
	keyCertsTriesCount             = "certs.triesCount"
	keyCertsBlockHours             = "certs.blockHours"
	keyCertsDowngradeCertModeHours = "certs.downgradeCertModeHours"
	keyZeroSSLAcmeKid              = "certs.zerossl.AcmeKid"
	keyZeroSSLAcmeHmac             = "certs.zerossl.AcmeHmac"
	keyLetsEncryptDirectory        = "certs.letsencrypt.directory"
	keyLetsEncryptStagingDir       = "certs.letsencrypt.stagingDirectory"
	keyZeroSSLDirectory            = "certs.zerossl.directory"

	defaultCertsTriesCount             = 3
	defaultCertsBlockHours             = 24
	defaultCertsDowngradeCertModeHours = 24

	defaultLetsEncryptDirectory        = "https://acme-v02.api.letsencrypt.org/directory"
	defaultLetsEncryptStagingDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"
	defaultZeroSSLDirectory            = "https://acme.zerossl.com/v2/DV90"

	// CertsProviderLetsEncrypt is the default ACME CA (Let's Encrypt).
	CertsProviderLetsEncrypt = "letsencrypt"
	// CertsProviderZeroSSL selects ZeroSSL (requires EAB credentials).
	CertsProviderZeroSSL = "zerossl"
)

var (
	errMissingRequiredKey   = errors.New("missing required key")
	errInvalidHostTimeout   = errors.New("invalid host timeout")
	errInvalidHostInt       = errors.New("invalid host integer")
	errInvalidCertsProvider = errors.New("invalid certs.provider")
	errMissingZeroSSLEAB    = errors.New(
		"certs.zerossl.AcmeKid and certs.zerossl.AcmeHmac are required when certs.provider=zerossl",
	)
)

// HostConfig holds values parsed from the host .apinfra next to the infra directory.
// Application compose secrets live in the host .env and each service's sidecar .env
// in the config repo. Per-project cert mode/TTL/email live in settings.yml; the ACME
// CA (Let's Encrypt vs ZeroSSL) is selected here via CertsProvider.
type HostConfig struct {
	InfraDirPath                string
	ConfigPath                  string
	EnvPath                     string
	Namespace                   string
	RepoURL                     string
	RepoToken                   string
	RepoBranch                  string
	UseExecLogs                 bool
	GitTimeout                  time.Duration
	DockerLoginTimeout          time.Duration
	DockerComposePullTimeout    time.Duration
	DockerComposeUpTimeout      time.Duration
	DockerComposeDownTimeout    time.Duration
	DockerPruneTimeout          time.Duration
	DockerAPITimeout            time.Duration
	NginxCLITimeout             time.Duration
	DomainPrecheckTimeout       time.Duration
	ProxyReadyAfter             time.Duration
	CertIssueTimeout            time.Duration
	CertsProvider               string
	CertsPublicIP               string
	CertsTriesCount             int
	CertsBlockHours             int
	CertsDowngradeCertModeHours int
	AcmeKid                     string
	AcmeHmac                    string
	// ACME directory URLs (lego CADirURL). Empty .apinfra keys → public CA defaults.
	LetsEncryptDirectory        string
	LetsEncryptStagingDirectory string
	ZeroSSLDirectory            string
}

// LoadHostConfig resolves $HOME/infra and reads the sibling .apinfra file.
func LoadHostConfig() (*HostConfig, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}

	infraDirPath := filepath.Join(homeDir, InfraDirName)
	configPath := hostConfigPath(infraDirPath)

	envs, err := ReadProperties(configPath)
	if err != nil {
		return nil, fmt.Errorf("read %s at %s: %w", hostConfigFileName, configPath, err)
	}

	cfg, err := buildHostConfig(infraDirPath, configPath, envs)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

// hostConfigPath returns the host settings file (.apinfra) next to the infra directory.
func hostConfigPath(infraDirPath string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(infraDirPath)), hostConfigFileName)
}

func buildHostConfig(infraDirPath, configPath string, envs map[string]string) (*HostConfig, error) {
	namespace, err := requireHostKey(envs, "namespace", configPath)
	if err != nil {
		return nil, err
	}

	repoURL, err := requireHostKey(envs, "repo", configPath)
	if err != nil {
		return nil, err
	}

	repoBranch := optionalHostKey(envs, "repoBranch")
	if repoBranch == "" {
		repoBranch = defaultRepoBranch
	}

	envPath := hostEnvPath(infraDirPath)
	// Host secrets: owner read/write only (operators edit this file).
	err = ensureOwnerRWFiles(envPath, configPath)
	if err != nil {
		return nil, err
	}

	timeouts, err := parseHostTimeouts(envs)
	if err != nil {
		return nil, err
	}

	certs, err := parseCertsProvider(envs)
	if err != nil {
		return nil, err
	}

	retry, err := parseCertsRetryPolicy(envs)
	if err != nil {
		return nil, err
	}

	return &HostConfig{
		InfraDirPath:                infraDirPath,
		ConfigPath:                  configPath,
		EnvPath:                     envPath,
		Namespace:                   namespace,
		RepoURL:                     repoURL,
		RepoToken:                   optionalHostKey(envs, "repoToken"),
		RepoBranch:                  repoBranch,
		UseExecLogs:                 optionalHostBool(envs, "useExecLogs", false),
		GitTimeout:                  timeouts.git,
		DockerLoginTimeout:          timeouts.dockerLogin,
		DockerComposePullTimeout:    timeouts.composePull,
		DockerComposeUpTimeout:      timeouts.composeUp,
		DockerComposeDownTimeout:    timeouts.composeDown,
		DockerPruneTimeout:          timeouts.prune,
		DockerAPITimeout:            timeouts.dockerAPI,
		NginxCLITimeout:             timeouts.nginxCLI,
		DomainPrecheckTimeout:       timeouts.domainPrecheck,
		ProxyReadyAfter:             timeouts.proxyReadyAfter,
		CertIssueTimeout:            timeouts.certIssue,
		CertsProvider:               certs.provider,
		CertsPublicIP:               certs.publicIP,
		CertsTriesCount:             retry.triesCount,
		CertsBlockHours:             retry.blockHours,
		CertsDowngradeCertModeHours: retry.downgradeCertModeHours,
		AcmeKid:                     certs.kid,
		AcmeHmac:                    certs.hmac,
		LetsEncryptDirectory:        certs.letsEncryptDirectory,
		LetsEncryptStagingDirectory: certs.letsEncryptStagingDirectory,
		ZeroSSLDirectory:            certs.zeroSSLDirectory,
	}, nil
}

type hostTimeouts struct {
	git             time.Duration
	dockerLogin     time.Duration
	composePull     time.Duration
	composeUp       time.Duration
	composeDown     time.Duration
	prune           time.Duration
	dockerAPI       time.Duration
	nginxCLI        time.Duration
	domainPrecheck  time.Duration
	proxyReadyAfter time.Duration
	certIssue       time.Duration
}

func parseHostTimeouts(envs map[string]string) (hostTimeouts, error) {
	var out hostTimeouts

	var err error

	out.git, err = optionalHostDuration(envs, keyGitTimeout, defaultShortTimeout)
	if err != nil {
		return hostTimeouts{}, fmt.Errorf("%s: %w", keyGitTimeout, err)
	}

	out.dockerLogin, err = optionalHostDuration(envs, keyDockerLoginTimeout, defaultShortTimeout)
	if err != nil {
		return hostTimeouts{}, fmt.Errorf("%s: %w", keyDockerLoginTimeout, err)
	}

	out.composePull, err = optionalHostDuration(envs, keyDockerComposePullTimeout, defaultComposePullTimeout)
	if err != nil {
		return hostTimeouts{}, fmt.Errorf("%s: %w", keyDockerComposePullTimeout, err)
	}

	out.composeUp, err = optionalHostDuration(envs, keyDockerComposeUpTimeout, defaultComposeUpTimeout)
	if err != nil {
		return hostTimeouts{}, fmt.Errorf("%s: %w", keyDockerComposeUpTimeout, err)
	}

	out.composeDown, err = optionalHostDuration(envs, keyDockerComposeDownTimeout, defaultComposeDownTimeout)
	if err != nil {
		return hostTimeouts{}, fmt.Errorf("%s: %w", keyDockerComposeDownTimeout, err)
	}

	out.prune, err = optionalHostDuration(envs, keyDockerPruneTimeout, defaultPruneTimeout)
	if err != nil {
		return hostTimeouts{}, fmt.Errorf("%s: %w", keyDockerPruneTimeout, err)
	}

	out.dockerAPI, err = optionalHostDuration(envs, keyDockerAPITimeout, defaultDockerAPITimeout)
	if err != nil {
		return hostTimeouts{}, fmt.Errorf("%s: %w", keyDockerAPITimeout, err)
	}

	out.nginxCLI, err = optionalHostDuration(envs, keyNginxCLITimeout, defaultNginxCLITimeout)
	if err != nil {
		return hostTimeouts{}, fmt.Errorf("%s: %w", keyNginxCLITimeout, err)
	}

	out.domainPrecheck, err = optionalHostDuration(envs, keyDomainPrecheckTimeout, defaultDomainPrecheckTimeout)
	if err != nil {
		return hostTimeouts{}, fmt.Errorf("%s: %w", keyDomainPrecheckTimeout, err)
	}

	out.proxyReadyAfter, err = optionalHostDuration(envs, keyProxyReadyAfter, defaultProxyReadyAfter)
	if err != nil {
		return hostTimeouts{}, fmt.Errorf("%s: %w", keyProxyReadyAfter, err)
	}

	out.certIssue, err = optionalHostDuration(envs, keyCertIssueTimeout, defaultShortTimeout)
	if err != nil {
		return hostTimeouts{}, fmt.Errorf("%s: %w", keyCertIssueTimeout, err)
	}

	return out, nil
}

type hostCertsSettings struct {
	provider                    string
	publicIP                    string
	kid                         string
	hmac                        string
	letsEncryptDirectory        string
	letsEncryptStagingDirectory string
	zeroSSLDirectory            string
}

// parseCertsProvider reads certs.provider, optional public IP for DNS preflight,
// optional ACME directory URLs, and optional ZeroSSL EAB credentials.
// Default provider is letsencrypt. ZeroSSL as primary requires AcmeKid and AcmeHmac.
// When EAB credentials are set with letsencrypt primary, both CAs are available
// for rate-limit fallback. Omitted directory URLs use public CA defaults.
func parseCertsProvider(envs map[string]string) (hostCertsSettings, error) {
	provider := strings.ToLower(optionalHostKey(envs, keyCertsProvider))
	if provider == "" {
		provider = CertsProviderLetsEncrypt
	}

	switch provider {
	case CertsProviderLetsEncrypt, CertsProviderZeroSSL:
	default:
		return hostCertsSettings{}, fmt.Errorf("%w %q (want %s or %s)",
			errInvalidCertsProvider, provider, CertsProviderLetsEncrypt, CertsProviderZeroSSL)
	}

	kid := optionalHostKey(envs, keyZeroSSLAcmeKid)
	hmac := optionalHostKey(envs, keyZeroSSLAcmeHmac)
	publicIP := optionalHostKey(envs, keyCertsPublicIP)

	if provider == CertsProviderZeroSSL && (kid == "" || hmac == "") {
		return hostCertsSettings{}, errMissingZeroSSLEAB
	}

	return hostCertsSettings{
		provider:                    provider,
		publicIP:                    publicIP,
		kid:                         kid,
		hmac:                        hmac,
		letsEncryptDirectory:        optionalHostKeyOr(envs, keyLetsEncryptDirectory, defaultLetsEncryptDirectory),
		letsEncryptStagingDirectory: optionalHostKeyOr(envs, keyLetsEncryptStagingDir, defaultLetsEncryptStagingDirectory),
		zeroSSLDirectory:            optionalHostKeyOr(envs, keyZeroSSLDirectory, defaultZeroSSLDirectory),
	}, nil
}

func optionalHostKeyOr(envs map[string]string, key, fallback string) string {
	if v := optionalHostKey(envs, key); v != "" {
		return v
	}

	return fallback
}

type hostCertsRetrySettings struct {
	triesCount             int
	blockHours             int
	downgradeCertModeHours int
}

// parseCertsRetryPolicy reads tries/block hours used after ACME failures.
func parseCertsRetryPolicy(envs map[string]string) (hostCertsRetrySettings, error) {
	triesCount, err := optionalHostPositiveInt(envs, keyCertsTriesCount, defaultCertsTriesCount)
	if err != nil {
		return hostCertsRetrySettings{}, fmt.Errorf("%s: %w", keyCertsTriesCount, err)
	}

	blockHours, err := optionalHostPositiveInt(envs, keyCertsBlockHours, defaultCertsBlockHours)
	if err != nil {
		return hostCertsRetrySettings{}, fmt.Errorf("%s: %w", keyCertsBlockHours, err)
	}

	downgradeCertModeHours, err := optionalHostPositiveInt(
		envs, keyCertsDowngradeCertModeHours, defaultCertsDowngradeCertModeHours,
	)
	if err != nil {
		return hostCertsRetrySettings{}, fmt.Errorf("%s: %w", keyCertsDowngradeCertModeHours, err)
	}

	return hostCertsRetrySettings{
		triesCount:             triesCount,
		blockHours:             blockHours,
		downgradeCertModeHours: downgradeCertModeHours,
	}, nil
}

// hostEnvPath returns the host application env file (.env) next to the infra directory.
func hostEnvPath(infraDirPath string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(infraDirPath)), hostEnvFileName)
}

func requireHostKey(envs map[string]string, name, configPath string) (string, error) {
	val, exists := envs[name]
	if !exists || strings.TrimSpace(val) == "" {
		return "", fmt.Errorf("%w %q in %s", errMissingRequiredKey, name, configPath)
	}

	return strings.Trim(val, "\""), nil
}

func optionalHostKey(envs map[string]string, name string) string {
	val, exists := envs[name]
	if !exists {
		return ""
	}

	return strings.Trim(strings.TrimSpace(val), "\"")
}

func optionalHostBool(envs map[string]string, name string, def bool) bool {
	val, exists := envs[name]
	if !exists {
		return def
	}

	trimmed := strings.Trim(strings.TrimSpace(val), "\"")

	switch {
	case strings.EqualFold(trimmed, "true"):
		return true
	case strings.EqualFold(trimmed, "false"):
		return false
	default:
		return def
	}
}

// optionalHostPositiveInt parses a positive integer from .apinfra.
// Missing/empty → def. Zero/negative or non-integer → error.
func optionalHostPositiveInt(envs map[string]string, name string, def int) (int, error) {
	val, exists := envs[name]
	if !exists {
		return def, nil
	}

	trimmed := strings.Trim(strings.TrimSpace(val), "\"")
	if trimmed == "" {
		return def, nil
	}

	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("%w %q: %w", errInvalidHostInt, trimmed, err)
	}

	if parsed <= 0 {
		return 0, fmt.Errorf("%w %q: must be > 0", errInvalidHostInt, trimmed)
	}

	return parsed, nil
}

// optionalHostDuration parses a Go duration from .apinfra.
// Missing/empty → def. Zero/negative → error. No upper bound.
func optionalHostDuration(envs map[string]string, name string, def time.Duration) (time.Duration, error) {
	val, exists := envs[name]
	if !exists {
		return def, nil
	}

	trimmed := strings.Trim(strings.TrimSpace(val), "\"")
	if trimmed == "" {
		return def, nil
	}

	parsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, fmt.Errorf("%w %q: %w", errInvalidHostTimeout, trimmed, err)
	}

	if parsed <= 0 {
		return 0, fmt.Errorf("%w %q: must be > 0", errInvalidHostTimeout, trimmed)
	}

	return parsed, nil
}

func ensureOwnerRWFiles(paths ...string) error {
	for _, path := range paths {
		err := ensureOwnerRWFile(path)
		if err != nil {
			return err
		}
	}

	return nil
}

// ensureOwnerRWFile sets mode 0600 when path exists (no-op if missing).
func ensureOwnerRWFile(path string) error {
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return fmt.Errorf("stat %s: %w", path, err)
	}

	err = os.Chmod(path, ownerRWFileMode)
	if err != nil {
		return fmt.Errorf("chmod 0600 %s: %w", path, err)
	}

	return nil
}
