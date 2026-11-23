package config_test

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/uservseatb/apinfra/internal/config"
)

const (
	testNamespace = "dev"
	settingsHead  = "version: \"1\"\nprojects:\n"
	errFormat     = "invalid settings.yml format"
)

var errBoom = errors.New("boom")

type fakeRepo struct {
	files   map[string]string
	readErr error
	missing bool
}

func (r *fakeRepo) Exists(_ string) bool { return !r.missing }

func (r *fakeRepo) ReadFile(path string) ([]byte, error) {
	if r.readErr != nil {
		return nil, r.readErr
	}

	content, ok := r.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}

	return []byte(content), nil
}

func readSettings(raw string) ([]config.ServiceConfig, error) {
	repo := &fakeRepo{files: map[string]string{testNamespace + "/settings.yml": raw}}

	got, err := config.ReadSettings(repo, testNamespace)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}

	return got, nil
}

func ln(lines ...string) []string { return lines }

// project renders one projects[] item; parts are indented under the item.
func project(name string, parts ...[]string) string {
	var b strings.Builder

	b.WriteString("  - name: " + name + "\n")

	for _, part := range parts {
		for _, line := range part {
			b.WriteString("    " + line + "\n")
		}
	}

	return b.String()
}

func proxy(inbound ...string) []string {
	out := ln("proxy:", "  inbound:")
	for _, line := range inbound {
		out = append(out, "    "+line)
	}

	return append(out, "  target:", "    service:", "      name: web", "      port: 8080")
}

func proxyURL(targetURL string) []string {
	return ln(
		"proxy:",
		"  inbound:",
		"    domain-name: api.example.com",
		"  target:",
		"    url: "+targetURL,
	)
}

func cfg(lines ...string) []string {
	out := ln("config:")
	for _, line := range lines {
		out = append(out, "  "+line)
	}

	return out
}

func doc(projects ...string) string { return settingsHead + strings.Join(projects, "") }

func TestReadSettings_DocumentErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "empty_document", raw: "", wantErr: "empty document"},
		{name: "invalid_yaml", raw: "projects: [oops", wantErr: "parse YAML"},
		{name: "version_missing", raw: "projects: []\n", wantErr: "version is required"},
		{name: "version_not_string", raw: "version: 1\nprojects: []\n", wantErr: "version must be a non-empty string"},
		{name: "version_blank", raw: "version: \"  \"\nprojects: []\n", wantErr: "version must be a non-empty string"},
		{name: "projects_missing", raw: "version: \"1\"\n", wantErr: "projects is required"},
		{name: "projects_not_sequence", raw: "version: \"1\"\nprojects: {a: b}\n", wantErr: "projects must be a sequence"},
		{name: "project_not_mapping", raw: settingsHead + "  - just-a-string\n", wantErr: "projects[0] must be a mapping"},
		{name: "project_name_missing", raw: settingsHead + "  - enabled: true\n", wantErr: "projects[0].name is required"},
		{
			name: "duplicate_name_case_insensitive",
			raw: doc(
				project("Shop", proxy("domain-name: a.example.com"), cfg("cert-issue-type: none")),
				project("shop", proxy("domain-name: b.example.com"), cfg("cert-issue-type: none")),
			),
			wantErr: "duplicate project name",
		},
		{
			name:    "enabled_not_bool",
			raw:     doc(project("shop", ln("enabled: \"yes\""), proxy("domain-name: a.example.com"))),
			wantErr: "enabled must be a boolean",
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			_, err := readSettings(tcase.raw)
			if err == nil || !strings.Contains(err.Error(), tcase.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tcase.wantErr)
			}

			if !strings.Contains(err.Error(), errFormat) {
				t.Fatalf("err must carry the common settings prefix: %v", err)
			}
		})
	}
}

func TestReadSettings_RepoErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		repo    *fakeRepo
		wantErr string
	}{
		{name: "namespace_folder_missing", repo: &fakeRepo{missing: true}, wantErr: "environment folder"},
		{name: "settings_unreadable", repo: &fakeRepo{readErr: errBoom}, wantErr: "read settings.yml"},
		{name: "settings_file_absent", repo: &fakeRepo{files: map[string]string{}}, wantErr: "read settings.yml"},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.ReadSettings(tcase.repo, testNamespace)
			if err == nil || !strings.Contains(err.Error(), tcase.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tcase.wantErr)
			}
		})
	}

	t.Run("nil_repo", func(t *testing.T) {
		t.Parallel()

		_, err := config.ReadSettings(nil, testNamespace)
		if err == nil || !strings.Contains(err.Error(), "nil repo") {
			t.Fatalf("err = %v, want nil repo", err)
		}
	})
}

func TestReadSettings_Projects(t *testing.T) {
	t.Parallel()

	const (
		secProd = int64(5184000)
		secTest = int64(2592000)
		secMock = int64(2592000)
	)

	tests := []struct {
		name string
		raw  string
		want []config.ServiceConfig
	}{
		{
			name: "domain_defaults_to_mock",
			raw:  doc(project("shop", proxy("domain-name: shop.example.com"))),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "shop",
				Routes: []config.Route{{
					DomainName: "shop.example.com", EntryPoint: "web", EntryPointPort: "8080",
					CertIssueType: "mock", CertUpdateSec: secMock,
				}},
			}},
		},
		{
			name: "domain_target_url_https",
			raw:  doc(project("edge", proxyURL("https://api.example.org"))),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "edge",
				Routes: []config.Route{{
					DomainName: "api.example.com", TargetURL: "https://api.example.org",
					CertIssueType: "mock", CertUpdateSec: secMock,
				}},
			}},
		},
		{
			name: "domain_target_url_http",
			raw:  doc(project("edge", proxyURL("http://backend.internal:8080"))),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "edge",
				Routes: []config.Route{{
					DomainName: "api.example.com", TargetURL: "http://backend.internal:8080",
					CertIssueType: "mock", CertUpdateSec: secMock,
				}},
			}},
		},
		{
			name: "domain_prod_with_email_and_default_ttl",
			raw: doc(project("shop", proxy("domain-name: shop.example.com"),
				cfg("cert-issue-type: prod", "cert-acme-email: ops@example.com"))),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "shop",
				Routes: []config.Route{{
					DomainName: "shop.example.com", EntryPoint: "web", EntryPointPort: "8080",
					CertIssueType: "prod", CertUpdateSec: secProd, CertAcmeEmail: "ops@example.com",
				}},
			}},
		},
		{
			name: "domain_test_with_custom_ttl_int_and_string",
			raw: doc(
				project("a", proxy("domain-name: a.example.com"),
					cfg("cert-issue-type: test", "cert-acme-email: ops@example.com", "cert-update-time-sec: 120")),
				project("b", proxy("domain-name: b.example.com"),
					cfg("cert-issue-type: test", "cert-acme-email: ops@example.com", "cert-update-time-sec: \"300\"")),
			),
			want: []config.ServiceConfig{
				{
					Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "a",
					Routes: []config.Route{{
						DomainName: "a.example.com", EntryPoint: "web", EntryPointPort: "8080",
						CertIssueType: "test", CertUpdateSec: 120, CertAcmeEmail: "ops@example.com",
					}},
				},
				{
					Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "b",
					Routes: []config.Route{{
						DomainName: "b.example.com", EntryPoint: "web", EntryPointPort: "8080",
						CertIssueType: "test", CertUpdateSec: 300, CertAcmeEmail: "ops@example.com",
					}},
				},
			},
		},
		{
			name: "domain_test_default_ttl",
			raw: doc(project("shop", proxy("domain-name: shop.example.com"),
				cfg("cert-issue-type: test", "cert-acme-email: ops@example.com"))),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "shop",
				Routes: []config.Route{{
					DomainName: "shop.example.com", EntryPoint: "web", EntryPointPort: "8080",
					CertIssueType: "test", CertUpdateSec: secTest, CertAcmeEmail: "ops@example.com",
				}},
			}},
		},
		{
			name: "domain_none_has_zero_ttl",
			raw:  doc(project("shop", proxy("domain-name: shop.example.com"), cfg("cert-issue-type: none"))),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "shop",
				Routes: []config.Route{{
					DomainName: "shop.example.com", EntryPoint: "web", EntryPointPort: "8080",
					CertIssueType: "none",
				}},
			}},
		},
		{
			name: "port_routing_defaults_to_none",
			raw:  doc(project("shop", proxy("port: 9000"))),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "shop",
				Routes: []config.Route{{
					InboundPort: "9000", EntryPoint: "web", EntryPointPort: "8080", CertIssueType: "none",
				}},
			}},
		},
		{
			name: "port_routing_allows_mock",
			raw:  doc(project("shop", proxy("port: 9000"), cfg("cert-issue-type: mock"))),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "shop",
				Routes: []config.Route{{
					InboundPort: "9000", EntryPoint: "web", EntryPointPort: "8080",
					CertIssueType: "mock", CertUpdateSec: secMock,
				}},
			}},
		},
		{
			name: "no_proxy_is_compose_only_project",
			raw:  doc(project("worker", cfg("allow-ports-mapping: true"))),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "worker",
				AllowPortsMapping: true,
			}},
		},
		{
			name: "allow_ports_mapping_legacy_top_level",
			raw:  doc(project("worker", ln("allow-ports-mapping: true"))),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "worker",
				AllowPortsMapping: true,
			}},
		},
		{
			name: "allow_ports_mapping_config_wins_over_top_level",
			raw:  doc(project("worker", ln("allow-ports-mapping: true"), cfg("allow-ports-mapping: false"))),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "worker",
			}},
		},
		{
			name: "disabled_projects_are_omitted",
			raw: doc(
				project("off", ln("enabled: false"), proxy("domain-name: off.example.com")),
				project("on", ln("enabled: true"), proxy("port: 9001")),
			),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "on",
				Routes: []config.Route{{
					InboundPort: "9001", EntryPoint: "web", EntryPointPort: "8080", CertIssueType: "none",
				}},
			}},
		},
		{
			name: "all_disabled_yields_empty",
			raw:  doc(project("off", ln("enabled: false"))),
			want: nil,
		},
		{
			name: "proxies_single_route",
			raw: doc(project("shop", ln(
				"proxies:",
				"  - inbound:",
				"      domain-name: shop.example.com",
				"    target:",
				"      service:",
				"        name: web",
				"        port: 8080",
			))),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "shop",
				Routes: []config.Route{{
					DomainName: "shop.example.com", EntryPoint: "web", EntryPointPort: "8080",
					CertIssueType: "mock", CertUpdateSec: secMock,
				}},
			}},
		},
		{
			name: "proxies_multi_route_per_route_cert",
			raw: doc(project("admin", ln(
				"proxies:",
				"  - inbound:",
				"      port: 3333",
				"    target:",
				"      service:",
				"        name: console",
				"        port: 8080",
				"    config:",
				"      cert-issue-type: mock",
				"  - inbound:",
				"      domain-name: admin.example.com",
				"    target:",
				"      service:",
				"        name: console",
				"        port: 8080",
				"    config:",
				"      cert-issue-type: prod",
				"      cert-acme-email: ops@example.com",
			))),
			want: []config.ServiceConfig{{
				Namespace: testNamespace, NamespacePrefix: testNamespace, Name: "admin",
				Routes: []config.Route{
					{
						InboundPort: "3333", EntryPoint: "console", EntryPointPort: "8080",
						CertIssueType: "mock", CertUpdateSec: secMock,
					},
					{
						DomainName: "admin.example.com", EntryPoint: "console", EntryPointPort: "8080",
						CertIssueType: "prod", CertUpdateSec: secProd, CertAcmeEmail: "ops@example.com",
					},
				},
			}},
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			got, err := readSettings(tcase.raw)
			if err != nil {
				t.Fatalf("ReadSettings: %v", err)
			}

			assertServiceConfigsEqual(t, got, tcase.want)
		})
	}
}

func assertServiceConfigsEqual(t *testing.T, got, want []config.ServiceConfig) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("got %d projects (%#v), want %d", len(got), got, len(want))
	}

	for i := range got {
		if got[i].Namespace != want[i].Namespace ||
			got[i].NamespacePrefix != want[i].NamespacePrefix ||
			got[i].Name != want[i].Name ||
			got[i].AllowPortsMapping != want[i].AllowPortsMapping {
			t.Fatalf("project[%d] = %#v, want %#v", i, got[i], want[i])
		}

		if len(got[i].Routes) != len(want[i].Routes) {
			t.Fatalf("project[%d].Routes = %#v, want %#v", i, got[i].Routes, want[i].Routes)
		}

		for j := range got[i].Routes {
			if got[i].Routes[j] != want[i].Routes[j] {
				t.Fatalf("project[%d].Routes[%d] = %#v, want %#v", i, j, got[i].Routes[j], want[i].Routes[j])
			}
		}
	}
}

func TestReadSettings_ProxyAndCertErrors(t *testing.T) {
	t.Parallel()

	domainProxy := proxy("domain-name: shop.example.com")

	tests := []struct {
		name    string
		project string
		wantErr string
	}{
		{name: "proxy_not_mapping", project: project("shop", ln("proxy: nope")), wantErr: "proxy must be a mapping"},
		{name: "inbound_missing", project: project("shop", ln("proxy:", "  target:", "    service:", "      name: web", "      port: 80")), wantErr: "proxy.inbound is required"},
		{name: "inbound_not_mapping", project: project("shop", ln("proxy:", "  inbound: x")), wantErr: "proxy.inbound must be a mapping"},
		{name: "inbound_empty", project: project("shop", ln("proxy:", "  inbound: {}")), wantErr: "requires domain-name or port"},
		{
			name:    "inbound_domain_and_port",
			project: project("shop", proxy("domain-name: shop.example.com", "port: 9000")),
			wantErr: "mutually exclusive",
		},
		{
			name:    "target_missing",
			project: project("shop", ln("proxy:", "  inbound:", "    port: 9000")),
			wantErr: "proxy.target is required",
		},
		{
			name:    "target_not_mapping",
			project: project("shop", ln("proxy:", "  inbound:", "    port: 9000", "  target: x")),
			wantErr: "proxy.target must be a mapping",
		},
		{
			name:    "target_service_not_mapping",
			project: project("shop", ln("proxy:", "  inbound:", "    port: 9000", "  target:", "    service: x")),
			wantErr: "proxy.target.service must be a mapping",
		},
		{
			name:    "target_service_name_missing",
			project: project("shop", ln("proxy:", "  inbound:", "    port: 9000", "  target:", "    service:", "      port: 80")),
			wantErr: "proxy.target.service.name is required",
		},
		{
			name:    "target_service_port_missing",
			project: project("shop", ln("proxy:", "  inbound:", "    port: 9000", "  target:", "    service:", "      name: web")),
			wantErr: "proxy.target.service.port is required",
		},
		{
			name:    "target_neither_service_nor_url",
			project: project("shop", ln("proxy:", "  inbound:", "    domain-name: shop.example.com", "  target: {}")),
			wantErr: "requires service or url",
		},
		{
			name: "target_url_and_service",
			project: project("shop", ln(
				"proxy:",
				"  inbound:",
				"    domain-name: shop.example.com",
				"  target:",
				"    url: https://api.example.org",
				"    service:",
				"      name: web",
				"      port: 80",
			)),
			wantErr: "mutually exclusive",
		},
		{
			name: "target_url_with_inbound_port",
			project: project("shop", ln(
				"proxy:",
				"  inbound:",
				"    port: 9000",
				"  target:",
				"    url: https://api.example.org",
			)),
			wantErr: "only allowed with proxy.inbound.domain-name",
		},
		{
			name: "proxy_and_proxies_mutual_exclusion",
			project: project("shop", proxy("domain-name: shop.example.com"), ln(
				"proxies:",
				"  - inbound:",
				"      domain-name: other.example.com",
				"    target:",
				"      service:",
				"        name: web",
				"        port: 80",
			)),
			wantErr: "proxy and proxies are mutually exclusive",
		},
		{
			name:    "proxies_empty_rejected",
			project: project("shop", ln("proxies: []")),
			wantErr: "proxies must not be empty",
		},
		{
			name: "duplicate_domain_across_routes",
			project: project("shop", ln(
				"proxies:",
				"  - inbound:",
				"      domain-name: shop.example.com",
				"    target:",
				"      service:",
				"        name: web",
				"        port: 80",
				"  - inbound:",
				"      domain-name: shop.example.com",
				"    target:",
				"      service:",
				"        name: api",
				"        port: 8080",
			)),
			wantErr: "already used by project",
		},
		{
			name:    "target_url_ftp",
			project: project("shop", proxyURL("ftp://api.example.org")),
			wantErr: "must use http:// or https://",
		},
		{
			name:    "target_url_userinfo",
			project: project("shop", proxyURL("https://user:pass@api.example.org")),
			wantErr: "must not include user:pass@",
		},
		{
			name:    "target_url_path",
			project: project("shop", proxyURL("https://api.example.org/v1")),
			wantErr: "must not include a path",
		},
		{
			name:    "target_url_query",
			project: project("shop", proxyURL("https://api.example.org?x=1")),
			wantErr: "must not include a query string",
		},
		{
			name:    "target_url_fragment",
			project: project("shop", proxyURL("https://api.example.org#frag")),
			wantErr: "must not include a fragment",
		},
		{
			name:    "target_url_missing_host",
			project: project("shop", proxyURL("https:///path")),
			wantErr: "must include a host",
		},
		{
			name:    "inbound_port_reused",
			project: project("a", proxy("port: 9000")) + project("b", proxy("port: 9000")),
			wantErr: "already used by project",
		},
		{
			name:    "port_routing_rejects_prod",
			project: project("shop", proxy("port: 9000"), cfg("cert-issue-type: prod")),
			wantErr: "must be mock or none",
		},
		{
			name:    "port_routing_rejects_test",
			project: project("shop", proxy("port: 9000"), cfg("cert-issue-type: test")),
			wantErr: "must be mock or none",
		},
		{
			name:    "prod_requires_email",
			project: project("shop", domainProxy, cfg("cert-issue-type: prod")),
			wantErr: "cert-acme-email is required",
		},
		{
			name:    "test_requires_email",
			project: project("shop", domainProxy, cfg("cert-issue-type: test")),
			wantErr: "cert-acme-email is required",
		},
		{
			name:    "unknown_issue_type",
			project: project("shop", domainProxy, cfg("cert-issue-type: staging")),
			wantErr: "must be mock, prod, test, or none",
		},
		{
			name:    "issue_type_not_string",
			project: project("shop", domainProxy, cfg("cert-issue-type: 5")),
			wantErr: "cert-issue-type must be a string",
		},
		{
			name:    "issue_type_blank",
			project: project("shop", domainProxy, cfg("cert-issue-type: \"  \"")),
			wantErr: "non-empty string",
		},
		{name: "config_not_mapping", project: project("shop", domainProxy, ln("config: nope")), wantErr: "config must be a mapping"},
		{
			name:    "update_sec_zero",
			project: project("shop", domainProxy, cfg("cert-update-time-sec: 0")),
			wantErr: "positive number",
		},
		{
			name:    "update_sec_negative",
			project: project("shop", domainProxy, cfg("cert-update-time-sec: -5")),
			wantErr: "positive number",
		},
		{
			name:    "update_sec_not_number",
			project: project("shop", domainProxy, cfg("cert-update-time-sec: abc")),
			wantErr: "must be a number",
		},
		{
			name:    "update_sec_wrong_type",
			project: project("shop", domainProxy, cfg("cert-update-time-sec: [1]")),
			wantErr: "number or string",
		},
		{
			name:    "email_not_string",
			project: project("shop", domainProxy, cfg("cert-acme-email: 5")),
			wantErr: "must be a string",
		},
		{
			name:    "allow_ports_not_bool",
			project: project("shop", domainProxy, cfg("allow-ports-mapping: \"yes\"")),
			wantErr: "allow-ports-mapping must be a boolean",
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			_, err := readSettings(doc(tcase.project))
			if err == nil || !strings.Contains(err.Error(), tcase.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tcase.wantErr)
			}
		})
	}
}
