package certmanagement_test

import (
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/domain/certmanagement"
)

const (
	wantDefaultTries                  = 3
	wantDefaultBlockHours             = 24
	wantDefaultDowngradeCertModeHours = 24
)

func TestNextLowerMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mode   string
		want   string
		wantOK bool
	}{
		{name: "prod_to_test", mode: certmanagement.ModeProd, want: certmanagement.ModeTest, wantOK: true},
		{name: "test_to_mock", mode: certmanagement.ModeTest, want: certmanagement.ModeMock, wantOK: true},
		{name: "mock_to_none", mode: certmanagement.ModeMock, want: certmanagement.ModeNone, wantOK: true},
		{name: "none_no_lower", mode: certmanagement.ModeNone, want: "", wantOK: false},
		{name: "empty", mode: "", want: "", wantOK: false},
		{name: "unknown", mode: "staging", want: "", wantOK: false},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			got, ok := certmanagement.NextLowerMode(tcase.mode)
			if ok != tcase.wantOK || got != tcase.want {
				t.Fatalf("certmanagement.NextLowerMode(%q) = (%q, %v), want (%q, %v)", tcase.mode, got, ok, tcase.want, tcase.wantOK)
			}
		})
	}
}

func TestDowngradeExpired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		until time.Time
		want  bool
	}{
		{name: "legacy_zero_never_expires", until: time.Time{}, want: false},
		{name: "future", until: now.Add(time.Hour), want: false},
		{name: "equal_boundary", until: now, want: true},
		{name: "past", until: now.Add(-time.Minute), want: true},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			got := certmanagement.DowngradeExpired(certmanagement.DowngradeEntry{Mode: certmanagement.ModeTest, Until: tcase.until}, now)
			if got != tcase.want {
				t.Fatalf("certmanagement.DowngradeExpired(...) = %v, want %v", got, tcase.want)
			}
		})
	}
}

func TestEffectiveMode(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	domain := "app.example.com"

	tests := []struct {
		name      string
		requested string
		domain    string
		downgrade map[string]certmanagement.DowngradeEntry
		want      string
	}{
		{
			name:      "no_downgrade_map",
			requested: certmanagement.ModeProd,
			domain:    domain,
			downgrade: nil,
			want:      certmanagement.ModeProd,
		},
		{
			name:      "empty_domain",
			requested: certmanagement.ModeProd,
			domain:    "",
			downgrade: map[string]certmanagement.DowngradeEntry{domain: {Mode: certmanagement.ModeTest, Until: now.Add(time.Hour)}},
			want:      certmanagement.ModeProd,
		},
		{
			name:      "active_prod_to_test",
			requested: certmanagement.ModeProd,
			domain:    domain,
			downgrade: map[string]certmanagement.DowngradeEntry{domain: {Mode: certmanagement.ModeTest, Until: now.Add(time.Hour)}},
			want:      certmanagement.ModeTest,
		},
		{
			name:      "active_prod_to_mock",
			requested: certmanagement.ModeProd,
			domain:    domain,
			downgrade: map[string]certmanagement.DowngradeEntry{domain: {Mode: certmanagement.ModeMock, Until: now.Add(time.Hour)}},
			want:      certmanagement.ModeMock,
		},
		{
			name:      "active_test_to_mock",
			requested: certmanagement.ModeTest,
			domain:    domain,
			downgrade: map[string]certmanagement.DowngradeEntry{domain: {Mode: certmanagement.ModeMock, Until: now.Add(time.Hour)}},
			want:      certmanagement.ModeMock,
		},
		{
			name:      "active_mock_to_none",
			requested: certmanagement.ModeMock,
			domain:    domain,
			downgrade: map[string]certmanagement.DowngradeEntry{domain: {Mode: certmanagement.ModeNone, Until: now.Add(time.Hour)}},
			want:      certmanagement.ModeNone,
		},
		{
			name:      "active_prod_to_none",
			requested: certmanagement.ModeProd,
			domain:    domain,
			downgrade: map[string]certmanagement.DowngradeEntry{domain: {Mode: certmanagement.ModeNone, Until: now.Add(time.Hour)}},
			want:      certmanagement.ModeNone,
		},
		{
			name:      "expired_ignored",
			requested: certmanagement.ModeProd,
			domain:    domain,
			downgrade: map[string]certmanagement.DowngradeEntry{domain: {Mode: certmanagement.ModeTest, Until: now.Add(-time.Second)}},
			want:      certmanagement.ModeProd,
		},
		{
			name:      "no_raise_mock_over_test_downgrade",
			requested: certmanagement.ModeMock,
			domain:    domain,
			downgrade: map[string]certmanagement.DowngradeEntry{domain: {Mode: certmanagement.ModeTest, Until: now.Add(time.Hour)}},
			want:      certmanagement.ModeMock,
		},
		{
			name:      "same_mode_keeps_requested",
			requested: certmanagement.ModeTest,
			domain:    domain,
			downgrade: map[string]certmanagement.DowngradeEntry{domain: {Mode: certmanagement.ModeTest, Until: now.Add(time.Hour)}},
			want:      certmanagement.ModeTest,
		},
		{
			name:      "other_domain_ignored",
			requested: certmanagement.ModeProd,
			domain:    domain,
			downgrade: map[string]certmanagement.DowngradeEntry{"other.example.com": {Mode: certmanagement.ModeTest, Until: now.Add(time.Hour)}},
			want:      certmanagement.ModeProd,
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			got := certmanagement.EffectiveMode(tcase.requested, tcase.domain, tcase.downgrade, now)
			if got != tcase.want {
				t.Fatalf("certmanagement.EffectiveMode(%q, %q, ...) = %q, want %q", tcase.requested, tcase.domain, got, tcase.want)
			}
		})
	}
}

func TestEffectiveRetryPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   certmanagement.CertRetryPolicy
		want certmanagement.CertRetryPolicy
	}{
		{
			name: "zeros_use_defaults",
			in:   certmanagement.CertRetryPolicy{},
			want: certmanagement.CertRetryPolicy{
				TriesCount:           wantDefaultTries,
				BlockDuration:        time.Duration(wantDefaultBlockHours) * time.Hour,
				DowngradeCertModeTTL: time.Duration(wantDefaultDowngradeCertModeHours) * time.Hour,
			},
		},
		{
			name: "negatives_use_defaults",
			in: certmanagement.CertRetryPolicy{
				TriesCount:           -1,
				BlockDuration:        -time.Hour,
				DowngradeCertModeTTL: -time.Minute,
			},
			want: certmanagement.CertRetryPolicy{
				TriesCount:           wantDefaultTries,
				BlockDuration:        time.Duration(wantDefaultBlockHours) * time.Hour,
				DowngradeCertModeTTL: time.Duration(wantDefaultDowngradeCertModeHours) * time.Hour,
			},
		},
		{
			name: "custom_preserved",
			in: certmanagement.CertRetryPolicy{
				TriesCount:           5,
				BlockDuration:        2 * time.Hour,
				DowngradeCertModeTTL: 6 * time.Hour,
			},
			want: certmanagement.CertRetryPolicy{
				TriesCount:           5,
				BlockDuration:        2 * time.Hour,
				DowngradeCertModeTTL: 6 * time.Hour,
			},
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			got := certmanagement.EffectiveRetryPolicy(tcase.in)
			if got != tcase.want {
				t.Fatalf("certmanagement.EffectiveRetryPolicy() = %+v, want %+v", got, tcase.want)
			}
		})
	}
}
