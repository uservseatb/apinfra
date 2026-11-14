package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/config"
)

const (
	minimalHost  = "namespace=dev\nrepo=https://example.com/cfg.git\n"
	ownerRWMode  = os.FileMode(0o600)
	defaultTries = 3
	defaultHours = 24
)

// loadHost runs LoadHostConfig against a temporary $HOME holding the given
// .apinfra content. Not parallel-safe (t.Setenv).
func loadHost(t *testing.T, apinfra string) (*config.HostConfig, string, error) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	if apinfra != "" {
		err := os.WriteFile(filepath.Join(home, ".apinfra"), []byte(apinfra), 0o600)
		if err != nil {
			t.Fatalf("write .apinfra: %v", err)
		}
	}

	cfg, err := config.LoadHostConfig()
	if err != nil {
		return nil, home, fmt.Errorf("load host config: %w", err)
	}

	return cfg, home, nil
}

func TestLoadHostConfig_Defaults(t *testing.T) {
	cfg, home, err := loadHost(t, minimalHost)
	if err != nil {
		t.Fatalf("LoadHostConfig: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"InfraDirPath", cfg.InfraDirPath, filepath.Join(home, "infra")},
		{"ConfigPath", cfg.ConfigPath, filepath.Join(home, ".apinfra")},
		{"EnvPath", cfg.EnvPath, filepath.Join(home, ".env")},
		{"Namespace", cfg.Namespace, "dev"},
		{"RepoURL", cfg.RepoURL, "https://example.com/cfg.git"},
		{"RepoBranch", cfg.RepoBranch, "main"},
		{"RepoToken", cfg.RepoToken, ""},
		{"UseExecLogs", cfg.UseExecLogs, false},
		{"GitTimeout", cfg.GitTimeout, time.Minute},
		{"DockerLoginTimeout", cfg.DockerLoginTimeout, time.Minute},
		{"DockerComposePullTimeout", cfg.DockerComposePullTimeout, 10 * time.Minute},
		{"DockerComposeUpTimeout", cfg.DockerComposeUpTimeout, 5 * time.Minute},
		{"DockerComposeDownTimeout", cfg.DockerComposeDownTimeout, 2 * time.Minute},
		{"DockerPruneTimeout", cfg.DockerPruneTimeout, 2 * time.Minute},
		{"DockerAPITimeout", cfg.DockerAPITimeout, 30 * time.Second},
		{"NginxCLITimeout", cfg.NginxCLITimeout, 30 * time.Second},
		{"DomainPrecheckTimeout", cfg.DomainPrecheckTimeout, 15 * time.Second},
		{"ProxyReadyAfter", cfg.ProxyReadyAfter, 30 * time.Second},
		{"CertIssueTimeout", cfg.CertIssueTimeout, time.Minute},
		{"CertsProvider", cfg.CertsProvider, config.CertsProviderLetsEncrypt},
		{"CertsTriesCount", cfg.CertsTriesCount, defaultTries},
		{"CertsBlockHours", cfg.CertsBlockHours, defaultHours},
		{"CertsDowngradeCertModeHours", cfg.CertsDowngradeCertModeHours, defaultHours},
		{"LetsEncryptDirectory", cfg.LetsEncryptDirectory, "https://acme-v02.api.letsencrypt.org/directory"},
		{"LetsEncryptStagingDirectory", cfg.LetsEncryptStagingDirectory, "https://acme-staging-v02.api.letsencrypt.org/directory"},
		{"ZeroSSLDirectory", cfg.ZeroSSLDirectory, "https://acme.zerossl.com/v2/DV90"},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoadHostConfig_Overrides(t *testing.T) {
	apinfra := minimalHost + strings.Join([]string{
		"repoBranch=release",
		"repoToken=\"tok\"",
		"useExecLogs=TRUE",
		"gitTimeout=5s",
		"dockerComposeUpTimeout=90s",
		"proxyReadyAfter=1s",
		"certIssueTimeout=2m",
		"certs.provider=ZeroSSL",
		"certs.zerossl.AcmeKid=kid",
		"certs.zerossl.AcmeHmac=hmac",
		"certs.publicIP=203.0.113.7",
		"certs.triesCount=5",
		"certs.blockHours=2",
		"certs.downgradeCertModeHours=48",
		"certs.letsencrypt.directory=https://le.test/dir",
		"certs.zerossl.directory=https://zs.test/dir",
	}, "\n") + "\n"

	cfg, _, err := loadHost(t, apinfra)
	if err != nil {
		t.Fatalf("LoadHostConfig: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"RepoBranch", cfg.RepoBranch, "release"},
		{"RepoToken", cfg.RepoToken, "tok"},
		{"UseExecLogs", cfg.UseExecLogs, true},
		{"GitTimeout", cfg.GitTimeout, 5 * time.Second},
		{"DockerComposeUpTimeout", cfg.DockerComposeUpTimeout, 90 * time.Second},
		{"ProxyReadyAfter", cfg.ProxyReadyAfter, time.Second},
		{"CertIssueTimeout", cfg.CertIssueTimeout, 2 * time.Minute},
		{"CertsProvider", cfg.CertsProvider, config.CertsProviderZeroSSL},
		{"AcmeKid", cfg.AcmeKid, "kid"},
		{"AcmeHmac", cfg.AcmeHmac, "hmac"},
		{"CertsPublicIP", cfg.CertsPublicIP, "203.0.113.7"},
		{"CertsTriesCount", cfg.CertsTriesCount, 5},
		{"CertsBlockHours", cfg.CertsBlockHours, 2},
		{"CertsDowngradeCertModeHours", cfg.CertsDowngradeCertModeHours, 48},
		{"LetsEncryptDirectory", cfg.LetsEncryptDirectory, "https://le.test/dir"},
		{"ZeroSSLDirectory", cfg.ZeroSSLDirectory, "https://zs.test/dir"},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoadHostConfig_Errors(t *testing.T) {
	tests := []struct {
		name    string
		apinfra string
		wantErr string
	}{
		{name: "file_missing", apinfra: "", wantErr: "open properties"},
		{name: "namespace_missing", apinfra: "repo=https://example.com/cfg.git\n", wantErr: `missing required key "namespace"`},
		{name: "namespace_blank", apinfra: "namespace= \nrepo=x\n", wantErr: `missing required key "namespace"`},
		{name: "repo_missing", apinfra: "namespace=dev\n", wantErr: `missing required key "repo"`},
		{name: "timeout_unparsable", apinfra: minimalHost + "gitTimeout=soon\n", wantErr: "gitTimeout"},
		{name: "timeout_zero", apinfra: minimalHost + "dockerAPITimeout=0s\n", wantErr: "must be > 0"},
		{name: "timeout_negative", apinfra: minimalHost + "nginxCLITimeout=-1s\n", wantErr: "must be > 0"},
		{name: "tries_not_int", apinfra: minimalHost + "certs.triesCount=many\n", wantErr: "certs.triesCount"},
		{name: "tries_zero", apinfra: minimalHost + "certs.triesCount=0\n", wantErr: "must be > 0"},
		{name: "block_hours_negative", apinfra: minimalHost + "certs.blockHours=-1\n", wantErr: "must be > 0"},
		{name: "downgrade_cert_mode_hours_zero", apinfra: minimalHost + "certs.downgradeCertModeHours=0\n", wantErr: "must be > 0"},
		{name: "provider_unknown", apinfra: minimalHost + "certs.provider=digicert\n", wantErr: "invalid certs.provider"},
		{name: "zerossl_without_eab", apinfra: minimalHost + "certs.provider=zerossl\n", wantErr: "AcmeKid and certs.zerossl.AcmeHmac are required"},
		{
			name:    "zerossl_with_only_kid",
			apinfra: minimalHost + "certs.provider=zerossl\ncerts.zerossl.AcmeKid=kid\n",
			wantErr: "required when certs.provider=zerossl",
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			_, _, err := loadHost(t, tcase.apinfra)
			if err == nil || !strings.Contains(err.Error(), tcase.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tcase.wantErr)
			}
		})
	}
}

func TestLoadHostConfig_EmptyValuesFallBackToDefaults(t *testing.T) {
	cfg, _, err := loadHost(t, minimalHost+"gitTimeout=\ncerts.triesCount=\ncerts.provider=\n")
	if err != nil {
		t.Fatalf("LoadHostConfig: %v", err)
	}

	if cfg.GitTimeout != time.Minute || cfg.CertsTriesCount != defaultTries ||
		cfg.CertsProvider != config.CertsProviderLetsEncrypt {
		t.Fatalf("empty values must use defaults: %+v", cfg)
	}
}

func TestLoadHostConfig_RestrictsSecretFilePermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	apinfraPath := filepath.Join(home, ".apinfra")
	envPath := filepath.Join(home, ".env")

	for path, content := range map[string]string{apinfraPath: minimalHost, envPath: "SHOP__TOKEN=abc\n"} {
		err := os.WriteFile(path, []byte(content), 0o600)
		if err != nil {
			t.Fatalf("write %s: %v", path, err)
		}

		err = os.Chmod(path, 0o644)
		if err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}
	}

	_, err := config.LoadHostConfig()
	if err != nil {
		t.Fatalf("LoadHostConfig: %v", err)
	}

	for _, path := range []string{apinfraPath, envPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}

		if info.Mode().Perm() != ownerRWMode {
			t.Fatalf("%s mode = %v, want %v", path, info.Mode().Perm(), ownerRWMode)
		}
	}
}

func TestLoadHostConfig_MissingEnvFileIsFine(t *testing.T) {
	cfg, home, err := loadHost(t, minimalHost)
	if err != nil {
		t.Fatalf("LoadHostConfig: %v", err)
	}

	_, statErr := os.Stat(filepath.Join(home, ".env"))
	if !os.IsNotExist(statErr) {
		t.Fatalf(".env must not be created by loading config: %v", statErr)
	}

	if cfg.EnvPath == "" {
		t.Fatal("EnvPath must be set even when file is absent")
	}
}
