package adapters_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uservseatb/apinfra/internal/domain/certmanagement"
	"github.com/uservseatb/apinfra/internal/infrastructure/adapters"
	"github.com/uservseatb/apinfra/internal/sys"
)

const (
	downgradeFileName = ".downgradeCertMode"
	retriesName       = ".retries"
)

const (
	repoTestDomain = "app.example.com"
	repoTestOther  = "other.example.com"
)

func newTestRepo(t *testing.T) (*adapters.YamlStateRepository, string) {
	t.Helper()

	home := t.TempDir()

	return adapters.NewYamlStateRepository(filepath.Join(home, "infra"), sys.FileWriter{}), home
}

func writeHomeFile(t *testing.T, home, name, content string) {
	t.Helper()

	err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o600)
	if err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestYamlStateRepository_LoadDowngrades(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		file        string // empty = file absent
		now         time.Time
		want        map[string]certmanagement.DowngradeEntry
		wantRewrite bool
	}{
		{
			name: "absent_file",
			now:  now,
			want: map[string]certmanagement.DowngradeEntry{},
		},
		{
			name: "mapping_with_cert_layout_time",
			file: repoTestDomain + ":\n  mode: test\n  until: \"2026-11-06T09:15:50.578\"\n",
			now:  now,
			want: map[string]certmanagement.DowngradeEntry{
				repoTestDomain: {Mode: certmanagement.ModeTest, Until: time.Date(2026, 11, 6, 9, 15, 50, 578_000_000, time.UTC)},
			},
		},
		{
			name: "mapping_with_rfc3339_time",
			file: repoTestDomain + ":\n  mode: mock\n  until: \"2026-11-06T09:15:50Z\"\n",
			now:  now,
			want: map[string]certmanagement.DowngradeEntry{
				repoTestDomain: {Mode: certmanagement.ModeMock, Until: time.Date(2026, 11, 6, 9, 15, 50, 0, time.UTC)},
			},
		},
		{
			name: "legacy_scalar_has_no_expiry",
			file: repoTestDomain + ": test\n",
			now:  now,
			want: map[string]certmanagement.DowngradeEntry{
				repoTestDomain: {Mode: certmanagement.ModeTest},
			},
		},
		{
			name: "mapping_without_until",
			file: repoTestDomain + ":\n  mode: test\n",
			now:  now,
			want: map[string]certmanagement.DowngradeEntry{
				repoTestDomain: {Mode: certmanagement.ModeTest},
			},
		},
		{
			name: "expired_entry_pruned_and_file_rewritten",
			file: repoTestDomain + ":\n  mode: test\n  until: \"2026-11-05T11:00:00.000\"\n" +
				repoTestOther + ":\n  mode: mock\n  until: \"2026-11-06T09:00:00.000\"\n",
			now: now,
			want: map[string]certmanagement.DowngradeEntry{
				repoTestOther: {Mode: certmanagement.ModeMock, Until: time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)},
			},
			wantRewrite: true,
		},
		{
			name: "zero_now_keeps_expired",
			file: repoTestDomain + ":\n  mode: test\n  until: \"2020-01-01T00:00:00.000\"\n",
			now:  time.Time{},
			want: map[string]certmanagement.DowngradeEntry{
				repoTestDomain: {Mode: certmanagement.ModeTest, Until: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
			},
		},
		{
			name: "empty_mode_skipped",
			file: repoTestDomain + ":\n  mode: \"\"\n",
			now:  now,
			want: map[string]certmanagement.DowngradeEntry{},
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			repo, home := newTestRepo(t)
			if tcase.file != "" {
				writeHomeFile(t, home, downgradeFileName, tcase.file)
			}

			got, err := repo.LoadDowngrades(tcase.now)
			if err != nil {
				t.Fatalf("LoadDowngrades: %v", err)
			}

			if len(got) != len(tcase.want) {
				t.Fatalf("got %+v, want %+v", got, tcase.want)
			}

			for domain, want := range tcase.want {
				if g := got[domain]; g.Mode != want.Mode || !g.Until.Equal(want.Until) {
					t.Fatalf("%s = %+v, want %+v", domain, g, want)
				}
			}

			if !tcase.wantRewrite {
				return
			}

			reloaded, err := repo.LoadDowngrades(time.Time{})
			if err != nil {
				t.Fatalf("reload: %v", err)
			}

			if _, stale := reloaded[repoTestDomain]; stale || len(reloaded) != len(tcase.want) {
				t.Fatalf("expired entry must be gone from disk: %+v", reloaded)
			}
		})
	}
}

func TestYamlStateRepository_LoadDowngradesRejectsGarbage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		file string
	}{
		{name: "sequence_entry", file: repoTestDomain + ":\n  - test\n"},
		{name: "bad_until", file: repoTestDomain + ":\n  mode: test\n  until: tomorrow\n"},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			repo, home := newTestRepo(t)
			writeHomeFile(t, home, downgradeFileName, tcase.file)

			_, err := repo.LoadDowngrades(time.Now())
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestYamlStateRepository_DowngradesRoundTrip(t *testing.T) {
	t.Parallel()

	repo, _ := newTestRepo(t)
	until := time.Date(2026, 11, 6, 9, 15, 50, 578_000_000, time.UTC)

	input := map[string]certmanagement.DowngradeEntry{
		repoTestDomain: {Mode: certmanagement.ModeTest, Until: until},
		repoTestOther:  {Mode: certmanagement.ModeMock},
	}

	err := repo.SaveDowngrades(input)
	if err != nil {
		t.Fatalf("SaveDowngrades: %v", err)
	}

	got, err := repo.LoadDowngrades(time.Time{})
	if err != nil {
		t.Fatalf("LoadDowngrades: %v", err)
	}

	for domain, want := range input {
		if g := got[domain]; g.Mode != want.Mode || !g.Until.Equal(want.Until) {
			t.Fatalf("%s = %+v, want %+v", domain, g, want)
		}
	}
}

func TestYamlStateRepository_LoadRetries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		file    string
		want    map[string]certmanagement.RetryEntry
		wantErr bool
	}{
		{name: "absent_file", want: map[string]certmanagement.RetryEntry{}},
		{name: "empty_certs", file: "certs: {}\n", want: map[string]certmanagement.RetryEntry{}},
		{
			name: "attempts_only",
			file: "certs:\n  " + repoTestDomain + ":\n    attempts: 2\n    blocked: false\n",
			want: map[string]certmanagement.RetryEntry{repoTestDomain: {Attempts: 2}},
		},
		{
			name: "blocked_with_until",
			file: "certs:\n  " + repoTestDomain + ":\n    attempts: 3\n    blocked: true\n    blocked_until: \"2026-11-06T09:15:50.578\"\n",
			want: map[string]certmanagement.RetryEntry{
				repoTestDomain: {
					Attempts:     3,
					Blocked:      true,
					BlockedUntil: time.Date(2026, 11, 6, 9, 15, 50, 578_000_000, time.UTC),
				},
			},
		},
		{
			name:    "bad_blocked_until",
			file:    "certs:\n  " + repoTestDomain + ":\n    attempts: 3\n    blocked: true\n    blocked_until: soon\n",
			wantErr: true,
		},
		{name: "invalid_yaml", file: "certs: [unterminated", wantErr: true},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			repo, home := newTestRepo(t)
			if tcase.file != "" {
				writeHomeFile(t, home, retriesName, tcase.file)
			}

			got, err := repo.LoadRetries()
			if tcase.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}

				return
			}

			if err != nil {
				t.Fatalf("LoadRetries: %v", err)
			}

			if len(got) != len(tcase.want) {
				t.Fatalf("got %+v, want %+v", got, tcase.want)
			}

			for domain, want := range tcase.want {
				g := got[domain]
				if g.Attempts != want.Attempts || g.Blocked != want.Blocked || !g.BlockedUntil.Equal(want.BlockedUntil) {
					t.Fatalf("%s = %+v, want %+v", domain, g, want)
				}
			}
		})
	}
}

func TestYamlStateRepository_RetriesRoundTripAndEmptySave(t *testing.T) {
	t.Parallel()

	repo, _ := newTestRepo(t)
	until := time.Date(2026, 11, 6, 9, 15, 50, 578_000_000, time.UTC)

	err := repo.SaveRetries(map[string]certmanagement.RetryEntry{
		repoTestDomain: {Attempts: 3, Blocked: true, BlockedUntil: until},
	})
	if err != nil {
		t.Fatalf("SaveRetries: %v", err)
	}

	got, err := repo.LoadRetries()
	if err != nil {
		t.Fatalf("LoadRetries: %v", err)
	}

	if g := got[repoTestDomain]; g.Attempts != 3 || !g.Blocked || !g.BlockedUntil.Equal(until) {
		t.Fatalf("round trip = %+v", g)
	}

	err = repo.SaveRetries(nil)
	if err != nil {
		t.Fatalf("SaveRetries(nil): %v", err)
	}

	got, err = repo.LoadRetries()
	if err != nil || len(got) != 0 {
		t.Fatalf("after clearing = (%+v, %v)", got, err)
	}
}
