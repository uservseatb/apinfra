package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/config"
	"github.com/uservseatb/apinfra/internal/events"
)

func TestLoadHostConfig_NotificationsEnabled(t *testing.T) {
	apinfra := minimalHost + strings.Join([]string{
		"notifications.enabled=true",
		"notifications.targets=generic+http://127.0.0.1:18100/hook generic+http://127.0.0.1:18101/hook",
		"notifications.events=security_violation,git_login_failed",
		"notifications.timeout=3s",
		"notifications.maxPerCycle=2",
		"notifications.cutEvery=5",
	}, "\n") + "\n"

	cfg, _, err := loadHost(t, apinfra)
	if err != nil {
		t.Fatalf("LoadHostConfig: %v", err)
	}

	if !cfg.NotificationsEnabled {
		t.Fatal("NotificationsEnabled = false")
	}

	if len(cfg.NotificationTargets) != 2 {
		t.Fatalf("targets = %#v", cfg.NotificationTargets)
	}

	if got := strings.Join(cfg.NotificationEvents, ","); got != "security_violation,git_login_failed" {
		t.Fatalf("events = %q", got)
	}

	if cfg.NotificationTimeout != 3*time.Second ||
		cfg.NotificationMaxPerCycle != 2 || cfg.NotificationCutEvery != 5 {
		t.Fatalf("notification knobs: %+v", cfg)
	}
}

func TestLoadHostConfig_NotificationsDefaultEvents(t *testing.T) {
	cases := []struct {
		name    string
		apinfra string
	}{
		{
			name: "omitted",
			apinfra: minimalHost +
				"notifications.enabled=true\n" +
				"notifications.targets=generic+http://127.0.0.1:18100/hook\n",
		},
		{
			name: "all",
			apinfra: minimalHost +
				"notifications.enabled=true\n" +
				"notifications.targets=generic+http://127.0.0.1:18100/hook\n" +
				"notifications.events=all\n",
		},
	}

	want := strings.Join(config.DefaultNotificationEvents(), ",")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _, err := loadHost(t, tc.apinfra)
			if err != nil {
				t.Fatalf("LoadHostConfig: %v", err)
			}

			got := strings.Join(cfg.NotificationEvents, ",")
			if got != want {
				t.Fatalf("events = %q, want %q", got, want)
			}

			if len(cfg.NotificationEvents) != len(events.AllTypes()) {
				t.Fatalf("events len = %d, want %d", len(cfg.NotificationEvents), len(events.AllTypes()))
			}
		})
	}
}

func TestLoadHostConfig_NotificationsAllMixedInvalid(t *testing.T) {
	apinfra := minimalHost +
		"notifications.enabled=true\n" +
		"notifications.targets=generic+http://127.0.0.1:18100/hook\n" +
		"notifications.events=all,security_violation\n"

	_, _, err := loadHost(t, apinfra)
	if err == nil || !strings.Contains(err.Error(), "invalid notifications.events") {
		t.Fatalf("err = %v", err)
	}
}
