package config_test

import (
	"errors"
	"testing"

	"github.com/uservseatb/apinfra/internal/config"
)

var errHostnameBoom = errors.New("hostname boom")

func TestResolveHostDisplayName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		serverName   string
		hostPublicIP string
		hostname     func() (string, error)
		want         string
	}{
		{
			name:         "serverName_wins",
			serverName:   "prod-1",
			hostPublicIP: "203.0.113.10",
			hostname:     func() (string, error) { return "os-host", nil },
			want:         "prod-1",
		},
		{
			name:         "hostPublicIP_when_no_serverName",
			hostPublicIP: "203.0.113.10",
			hostname:     func() (string, error) { return "os-host", nil },
			want:         "203.0.113.10",
		},
		{
			name:     "hostname_fallback",
			hostname: func() (string, error) { return "cv4816261.novalocal", nil },
			want:     "cv4816261.novalocal",
		},
		{
			name:     "hostname_error",
			hostname: func() (string, error) { return "", errHostnameBoom },
			want:     "unknown-host",
		},
		{
			name:     "hostname_blank",
			hostname: func() (string, error) { return "  ", nil },
			want:     "unknown-host",
		},
		{
			name:     "nil_hostname_func",
			hostname: nil,
			want:     "unknown-host",
		},
		{
			name:       "trims_serverName",
			serverName: "  edge  ",
			want:       "edge",
		},
	}

	for _, tcase := range cases {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			got := config.ResolveHostDisplayName(tcase.serverName, tcase.hostPublicIP, tcase.hostname)
			if got != tcase.want {
				t.Fatalf("got %q, want %q", got, tcase.want)
			}
		})
	}
}

func TestHostConfig_HostDisplayName(t *testing.T) {
	apinfra := minimalHost + "serverName=from-config\nhostPublicIP=1.2.3.4\n"

	cfg, _, err := loadHost(t, apinfra)
	if err != nil {
		t.Fatalf("LoadHostConfig: %v", err)
	}

	if cfg.ServerName != "from-config" || cfg.HostPublicIP != "1.2.3.4" {
		t.Fatalf("ServerName=%q HostPublicIP=%q", cfg.ServerName, cfg.HostPublicIP)
	}

	if got := cfg.HostDisplayName(); got != "from-config" {
		t.Fatalf("HostDisplayName = %q", got)
	}
}
