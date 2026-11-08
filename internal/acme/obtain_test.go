package acme_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	legoacme "github.com/go-acme/lego/v4/acme"
	"github.com/uservseatb/apinfra/internal/acme"
)

var (
	errConnRefused     = errors.New("connection refused")
	errRateLimitedMsg  = errors.New("acme: error: urn:ietf:params:acme:error:rateLimited")
	errRateLimitedText = errors.New("rate LIMITED by CA")
)

func TestRateLimitCooldown(t *testing.T) {
	t.Parallel()

	const defaultCooldown = 24 * time.Hour

	tests := []struct {
		name         string
		err          error
		ttl          time.Duration
		wantLimited  bool
		wantCooldown time.Duration
	}{
		{name: "nil", err: nil, ttl: time.Hour},
		{name: "unrelated", err: errConnRefused, ttl: time.Hour},
		{name: "sentinel", err: acme.ErrRateLimited, ttl: time.Hour, wantLimited: true, wantCooldown: time.Hour},
		{
			name: "sentinel_wrapped", err: fmt.Errorf("provider letsencrypt: %w", acme.ErrRateLimited),
			ttl: 2 * time.Hour, wantLimited: true, wantCooldown: 2 * time.Hour,
		},
		{name: "default_ttl_when_zero", err: acme.ErrRateLimited, ttl: 0, wantLimited: true, wantCooldown: defaultCooldown},
		{name: "default_ttl_when_negative", err: acme.ErrRateLimited, ttl: -time.Minute, wantLimited: true, wantCooldown: defaultCooldown},
		{
			name: "lego_rate_limited_error", err: &legoacme.RateLimitedError{RetryAfter: "1h"},
			ttl: time.Hour, wantLimited: true, wantCooldown: time.Hour,
		},
		{
			name: "problem_type_rate_limited",
			err:  &legoacme.ProblemDetails{Type: legoacme.RateLimitedErr},
			ttl:  time.Hour, wantLimited: true, wantCooldown: time.Hour,
		},
		{
			name: "problem_http_429",
			err:  &legoacme.ProblemDetails{HTTPStatus: http.StatusTooManyRequests},
			ttl:  time.Hour, wantLimited: true, wantCooldown: time.Hour,
		},
		{
			name: "problem_other_status",
			err:  &legoacme.ProblemDetails{HTTPStatus: http.StatusBadRequest, Type: "urn:ietf:params:acme:error:malformed"},
			ttl:  time.Hour,
		},
		{name: "message_ratelimited", err: errRateLimitedMsg, ttl: time.Hour, wantLimited: true, wantCooldown: time.Hour},
		{name: "message_rate_limited_text", err: errRateLimitedText, ttl: time.Hour, wantLimited: true, wantCooldown: time.Hour},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			cooldown, limited := acme.RateLimitCooldown(tcase.err, tcase.ttl)
			if limited != tcase.wantLimited || cooldown != tcase.wantCooldown {
				t.Fatalf("RateLimitCooldown = (%v, %v), want (%v, %v)", cooldown, limited, tcase.wantCooldown, tcase.wantLimited)
			}

			if got := acme.IsRateLimited(tcase.err); got != tcase.wantLimited {
				t.Fatalf("IsRateLimited = %v, want %v", got, tcase.wantLimited)
			}
		})
	}
}
