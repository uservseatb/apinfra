package sys_test

import (
	"strings"
	"testing"

	"github.com/uservseatb/apinfra/internal/sys"
)

func TestRedactURLUserinfo(t *testing.T) {
	t.Parallel()

	const secret = "s3cr3t"

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "user_and_password",
			in:   "https://bob:" + secret + "@github.com/o/r.git",
			want: "https://***@github.com/o/r.git",
		},
		{
			name: "token_only",
			in:   "https://" + secret + "@github.com/o/r.git",
			want: "https://***@github.com/o/r.git",
		},
		{
			name: "at_sign_inside_password_uses_last_at",
			in:   "https://bob:" + secret + "@x@github.com/o/r.git",
			want: "https://***@github.com/o/r.git",
		},
		{
			name: "password_with_percent_fallback",
			in:   "https://bob:100%pure@" + "github.com/o/r.git",
			want: "https://***@github.com/o/r.git",
		},
		{
			name: "url_inside_parentheses",
			in:   "see (https://bob:" + secret + "@github.com/o/r.git)",
			want: "see (https://***@github.com/o/r.git)",
		},
		{
			name: "url_inside_quotes",
			in:   `"https://bob:` + secret + `@github.com/o/r.git"`,
			want: `"https://***@github.com/o/r.git"`,
		},
		{
			name: "query_email_at_sign_untouched",
			in:   "https://host.example/path?email=a@b.com",
			want: "https://host.example/path?email=a@b.com",
		},
		{
			name: "github_pat_as_host_no_at",
			in:   "http://ghp_" + secret + "/127.0.0.1:18081/repo",
			want: "http://***",
		},
		{
			name: "docker_pat_as_host",
			in:   "http://dckr_pat_" + secret + "/registry.example/v2/",
			want: "http://***",
		},
		{
			name: "bare_pat_in_dns_error",
			in:   `lookup ghp_` + secret + `: no such host`,
			want: "lookup ***: no such host",
		},
		{
			name: "bare_akia_access_key",
			in:   "creds " + "AKIA" + "IOSFODNN7EXAMPLE leaked",
			want: "creds *** leaked",
		},
		{
			name: "bearer_token",
			in:   "Authorization: Bearer " + secret + "abc",
			want: "Authorization: ***",
		},
		{
			name: "cli_password_assign",
			in:   "tool --password=" + secret + " up",
			want: "tool --password=*** up",
		},
		{
			name: "cli_password_space",
			in:   "tool --password " + secret + " up",
			want: "tool --password *** up",
		},
		{
			name: "cli_password_stdin_untouched",
			in:   "docker login -u bot --password-stdin",
			want: "docker login -u bot --password-stdin",
		},
		{
			name: "cli_short_p",
			in:   "tool -p " + secret + " run",
			want: "tool -p *** run",
		},
		{
			name: "cli_token_assign",
			in:   "tool --token=" + secret,
			want: "tool --token=***",
		},
		{
			name: "env_style_token",
			in:   "TOKEN=" + secret + " ./run",
			want: "TOKEN=*** ./run",
		},
		{
			name: "url_without_userinfo_untouched",
			in:   "https://github.com/o/r.git",
			want: "https://github.com/o/r.git",
		},
		{
			name: "at_sign_in_path_is_not_credentials",
			in:   "https://github.com/o/r@v1",
			want: "https://github.com/o/r@v1",
		},
		{
			name: "already_redacted_is_idempotent",
			in:   "https://***@github.com/o/r.git",
			want: "https://***@github.com/o/r.git",
		},
		{
			name: "several_urls_in_one_string",
			in:   "clone https://a:" + secret + "@one.test/x then ssh://b:" + secret + "@two.test/y",
			want: "clone https://***@one.test/x then ssh://***@two.test/y",
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			got := sys.RedactURLUserinfo(tcase.in)
			if got != tcase.want {
				t.Fatalf("RedactURLUserinfo(%q) = %q, want %q", tcase.in, got, tcase.want)
			}

			if strings.Contains(got, secret) {
				t.Fatalf("secret leaked into redacted output: %q", got)
			}
		})
	}
}
