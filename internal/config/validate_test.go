package config_test

import (
	"strings"
	"testing"
)

// Field validators are exercised through ReadSettings so tests survive
// refactors of the unexported helpers.

func domainProject(domain string) string {
	return doc(project("shop", proxy("domain-name: "+domain), cfg("cert-issue-type: none")))
}

func TestReadSettings_DomainName(t *testing.T) {
	t.Parallel()

	label63 := strings.Repeat("a", 63)
	label64 := strings.Repeat("a", 64)
	tooLong := strings.Repeat("a.", 127) + "com"

	tests := []struct {
		name    string
		domain  string
		wantErr string
	}{
		{name: "simple", domain: "app.example.com"},
		{name: "two_labels", domain: "example.com"},
		{name: "hyphen_inside", domain: "my-app.example.com"},
		{name: "digits_and_uppercase", domain: "App1.Example.COM"},
		{name: "max_label_length", domain: label63 + ".com"},
		{name: "single_label", domain: "localhost", wantErr: "fully-qualified"},
		{name: "leading_dot", domain: ".example.com", wantErr: "start or end with a dot"},
		{name: "trailing_dot", domain: "example.com.", wantErr: "start or end with a dot"},
		{name: "empty_label", domain: "a..com", wantErr: "empty DNS label"},
		{name: "label_too_long", domain: label64 + ".com", wantErr: "longer than 63"},
		{name: "domain_too_long", domain: tooLong, wantErr: "too long"},
		{name: "label_starts_with_hyphen", domain: "-a.example.com", wantErr: "hyphen"},
		{name: "label_ends_with_hyphen", domain: "a-.example.com", wantErr: "hyphen"},
		{name: "underscore", domain: "a_b.example.com", wantErr: "ASCII letters"},
		{name: "unicode", domain: "приве.example.com", wantErr: "ASCII letters"},
		{name: "semicolon_directive_breakout", domain: "a;b.example.com", wantErr: "invalid characters"},
		{name: "braces", domain: "a{b}.example.com", wantErr: "invalid characters"},
		{name: "path_traversal", domain: "../etc.example.com", wantErr: "invalid characters"},
		{name: "shell_pipe", domain: "a|b.example.com", wantErr: "invalid characters"},
		{name: "quoted_space", domain: "\"a b.example.com\"", wantErr: "whitespace"},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			_, err := readSettings(domainProject(quoteIfNeeded(tcase.domain)))
			assertErr(t, err, tcase.wantErr)
		})
	}
}

func TestReadSettings_ProjectName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		project string
		wantErr string
	}{
		{name: "simple", project: "shop"},
		{name: "with_underscore_and_hyphen", project: "my-app_1"},
		{name: "digit_first", project: "1app"},
		{name: "space", project: "\"my app\"", wantErr: "must match"},
		{name: "dot", project: "a.b", wantErr: "must match"},
		{name: "path_separator", project: "\"a/b\"", wantErr: "must match"},
		{name: "traversal", project: "\"../x\"", wantErr: "must match"},
		{name: "leading_hyphen", project: "\"-x\"", wantErr: "must match"},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			_, err := readSettings(doc(project(tcase.project, proxy("port: 9000"))))
			assertErr(t, err, tcase.wantErr)
		})
	}
}

func TestReadSettings_ServiceName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		service string
		wantErr string
	}{
		{name: "simple", service: "web"},
		{name: "dots_and_underscores", service: "my.svc_1"},
		{name: "leading_hyphen", service: "\"-web\"", wantErr: "must match"},
		{name: "space", service: "\"a b\"", wantErr: "must match"},
		{name: "shell_chars", service: "\"a;b\"", wantErr: "must match"},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			raw := doc(project("shop", ln(
				"proxy:", "  inbound:", "    port: 9000", "  target:", "    service-name: "+tcase.service, "    port: 80",
			)))

			_, err := readSettings(raw)
			assertErr(t, err, tcase.wantErr)
		})
	}
}

func TestReadSettings_AcmeEmail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		email   string
		wantErr string
	}{
		{name: "plain", email: "ops@example.com"},
		{name: "plus_tag", email: "ops+acme@example.com"},
		{name: "subdomain", email: "a@mail.example.co.uk"},
		{name: "no_at", email: "ops.example.com", wantErr: "not a valid email"},
		{name: "no_dotted_host", email: "ops@localhost", wantErr: "not a valid email"},
		{name: "display_name_form", email: "\"Ops <ops@example.com>\"", wantErr: "bare email"},
		{name: "contains_space", email: "\"o ps@example.com\"", wantErr: "bare email"},
		{name: "host_label_hyphen_edge", email: "ops@-example.com", wantErr: "not a valid email"},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			raw := doc(project("shop", proxy("domain-name: shop.example.com"),
				cfg("cert-issue-type: prod", "cert-acme-email: "+tcase.email)))

			_, err := readSettings(raw)
			assertErr(t, err, tcase.wantErr)
		})
	}
}

func TestReadSettings_Ports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		inbound string // value for proxy.inbound.port
		target  string // value for proxy.target.port
		wantErr string
	}{
		{name: "inbound_min", inbound: "1001", target: "80"},
		{name: "inbound_max", inbound: "50000", target: "80"},
		{name: "inbound_as_string", inbound: "\"9000\"", target: "80"},
		{name: "inbound_below_min", inbound: "1000", target: "80", wantErr: "between 1001 and 50000"},
		{name: "inbound_privileged", inbound: "80", target: "80", wantErr: "between 1001 and 50000"},
		{name: "inbound_above_max", inbound: "50001", target: "80", wantErr: "between 1001 and 50000"},
		{name: "inbound_not_number", inbound: "abc", target: "80", wantErr: "must be a number"},
		{name: "inbound_fraction", inbound: "9000.5", target: "80", wantErr: "whole number"},
		{name: "inbound_empty_string", inbound: "\"\"", target: "80", wantErr: "non-empty number"},
		{name: "target_min", inbound: "9000", target: "1"},
		{name: "target_max", inbound: "9000", target: "65535"},
		{name: "target_zero", inbound: "9000", target: "0", wantErr: "between 1 and 65535"},
		{name: "target_above_max", inbound: "9000", target: "65536", wantErr: "between 1 and 65535"},
		{name: "target_bool", inbound: "9000", target: "true", wantErr: "number or string"},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			raw := doc(project("shop", ln(
				"proxy:", "  inbound:", "    port: "+tcase.inbound, "  target:", "    service-name: web", "    port: "+tcase.target,
			)))

			_, err := readSettings(raw)
			assertErr(t, err, tcase.wantErr)
		})
	}
}

func quoteIfNeeded(s string) string {
	if strings.HasPrefix(s, "\"") {
		return s
	}

	return "\"" + s + "\""
}

func assertErr(t *testing.T, err error, wantErr string) {
	t.Helper()

	if wantErr == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		return
	}

	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("err = %v, want containing %q", err, wantErr)
	}
}
