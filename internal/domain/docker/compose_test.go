package docker_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/uservseatb/apinfra/internal/domain/docker"
)

const (
	netProxy = "apinfra_proxy_net"
	netShop  = "shop_net"
	optNoNew = "no-new-privileges:true"
)

func portsOf(svc map[string]any) []string {
	list, _ := svc["ports"].([]any)
	out := make([]string, 0, len(list))

	for _, item := range list {
		port := asMap(item)
		hostIP, _ := port["host_ip"].(string)
		out = append(out, fmt.Sprintf("%s|%v|%v", hostIP, port["published"], port["target"]))
	}

	return out
}

func stringsOf(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))

	for _, item := range list {
		out = append(out, fmt.Sprint(item))
	}

	return out
}

func networkKeys(svc map[string]any) []string {
	nets := asMap(svc["networks"])
	keys := make([]string, 0, len(nets))

	for key := range nets {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}

func appCompose(t *testing.T, env *dockerEnv) map[string]any {
	t.Helper()

	return readYAML(t, env.read(t, env.appFile("docker-compose.yml")))
}

func TestApply_PublishedPortsPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		allow     bool
		ports     []string
		wantPorts []string
		wantErr   string
	}{
		{
			name:      "default_strips_all_ports",
			allow:     false,
			ports:     []string{"5432:5432", "80", "127.0.0.1:9090:90"},
			wantPorts: []string{},
		},
		{
			name:      "allow_binds_unbound_mapping_to_loopback",
			allow:     true,
			ports:     []string{"8080:80"},
			wantPorts: []string{"127.0.0.1|8080|80"},
		},
		{
			name:      "allow_bare_container_port_gets_loopback_and_published",
			allow:     true,
			ports:     []string{"80"},
			wantPorts: []string{"127.0.0.1|80|80"},
		},
		{
			name:      "allow_keeps_explicit_host_ip",
			allow:     true,
			ports:     []string{"10.0.0.1:8080:80", "127.0.0.1:9090:90"},
			wantPorts: []string{"10.0.0.1|8080|80", "127.0.0.1|9090|90"},
		},
		{
			name:    "allow_rejects_ipv4_wildcard",
			allow:   true,
			ports:   []string{"0.0.0.0:8080:80"},
			wantErr: "disallowed published host IP",
		},
		{
			name:    "allow_rejects_ipv6_wildcard",
			allow:   true,
			ports:   []string{"\"[::]:8080:80\""},
			wantErr: "disallowed published host IP",
		},
		{
			name:    "allow_rejects_ipv6_zero_alias",
			allow:   true,
			ports:   []string{"\"[::0]:8080:80\""},
			wantErr: "disallowed published host IP",
		},
		{
			name:    "allow_rejects_ipv6_full_unspecified",
			allow:   true,
			ports:   []string{"\"[0:0:0:0:0:0:0:0]:8080:80\""},
			wantErr: "disallowed published host IP",
		},
		{
			name:    "allow_rejects_ipv4_mapped_unspecified",
			allow:   true,
			ports:   []string{"\"[::ffff:0.0.0.0]:8080:80\""},
			wantErr: "disallowed published host IP",
		},
		{
			name:    "allow_rejects_wildcard_among_valid",
			allow:   true,
			ports:   []string{"8080:80", "0.0.0.0:9090:90"},
			wantErr: "disallowed published host IP",
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newDockerEnv(t)

			var compose strings.Builder

			compose.WriteString("services:\n  web:\n    image: nginx\n    ports:\n")

			for _, port := range tcase.ports {
				if strings.HasPrefix(port, "\"") {
					compose.WriteString("      - " + port + "\n")
				} else {
					compose.WriteString("      - \"" + port + "\"\n")
				}
			}

			project := shopProject(compose.String())
			project.AllowPortsMapping = tcase.allow

			_, err := env.applyAll(t, project)

			if tcase.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tcase.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tcase.wantErr)
				}

				if len(env.cli.calls) != 0 {
					t.Fatalf("rejected cycle must not touch docker: %v", env.cli.calls)
				}

				return
			}

			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			got := portsOf(serviceOf(appCompose(t, env), "web"))
			if !slices.Equal(got, tcase.wantPorts) {
				t.Fatalf("ports = %v, want %v", got, tcase.wantPorts)
			}
		})
	}
}

func TestApply_PublishedPortsPolicy_LongFormHostIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		hostIP  string
		wantErr string
	}{
		{name: "rejects_ipv6_zero_alias", hostIP: "::0", wantErr: "disallowed published host IP"},
		{name: "rejects_ipv4_mapped", hostIP: "::ffff:0.0.0.0", wantErr: "disallowed published host IP"},
		{name: "keeps_loopback", hostIP: "127.0.0.1", wantErr: ""},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newDockerEnv(t)
			compose := fmt.Sprintf(`services:
  web:
    image: nginx
    ports:
      - target: 80
        published: "8080"
        host_ip: %q
`, tcase.hostIP)

			project := shopProject(compose)
			project.AllowPortsMapping = true

			_, err := env.applyAll(t, project)
			if tcase.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tcase.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tcase.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			got := portsOf(serviceOf(appCompose(t, env), "web"))

			want := []string{tcase.hostIP + "|8080|80"}
			if !slices.Equal(got, want) {
				t.Fatalf("ports = %v, want %v", got, want)
			}
		})
	}
}

func TestApply_StripsNetworkModeAndContainerNames(t *testing.T) {
	t.Parallel()

	env := newDockerEnv(t)

	compose := `services:
  host:
    image: nginx
    container_name: shop-host
    network_mode: host
  bridged:
    image: nginx
    container_name: shop-bridged
    network_mode: bridge
  shared:
    image: nginx
    network_mode: service:host
`

	_, err := env.applyAll(t, shopProject(compose))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	doc := appCompose(t, env)

	for _, name := range []string{"host", "bridged", "shared"} {
		svc := serviceOf(doc, name)
		if _, has := svc["network_mode"]; has {
			t.Fatalf("network_mode must be stripped from %s: %v", name, svc)
		}

		if !slices.Contains(networkKeys(svc), netShop) {
			t.Fatalf("service %s must join %s: %v", name, netShop, networkKeys(svc))
		}
	}

	for _, name := range []string{"host", "bridged"} {
		if _, has := serviceOf(doc, name)["container_name"]; has {
			t.Fatalf("container_name must be stripped from %s", name)
		}
	}
}

func TestApply_ContainerHardening(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		compose  string
		wantOpts []string
	}{
		{
			name:     "added_when_missing",
			compose:  baseCompose,
			wantOpts: []string{optNoNew},
		},
		{
			name:     "appended_to_existing",
			compose:  "services:\n  web:\n    image: nginx\n    security_opt:\n      - seccomp:unconfined\n",
			wantOpts: []string{"seccomp:unconfined", optNoNew},
		},
		{
			name:     "not_duplicated",
			compose:  "services:\n  web:\n    image: nginx\n    security_opt:\n      - no-new-privileges:true\n",
			wantOpts: []string{optNoNew},
		},
		{
			name: "replaces_false_variant",
			compose: "services:\n  web:\n    image: nginx\n    security_opt:\n" +
				"      - no-new-privileges:false\n      - seccomp:unconfined\n",
			wantOpts: []string{"seccomp:unconfined", optNoNew},
		},
		{
			name:     "clears_privileged",
			compose:  "services:\n  web:\n    image: nginx\n    privileged: true\n",
			wantOpts: []string{optNoNew},
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newDockerEnv(t)

			_, err := env.applyAll(t, shopProject(tcase.compose))
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			web := serviceOf(appCompose(t, env), "web")

			got := stringsOf(web["security_opt"])
			if !slices.Equal(got, tcase.wantOpts) {
				t.Fatalf("security_opt = %v, want %v", got, tcase.wantOpts)
			}

			if privileged, has := web["privileged"]; has && privileged == true {
				t.Fatalf("privileged must not stay true: %v", web)
			}

			proxy := readYAML(t, env.read(t, env.infra+"/docker-compose.yml"))
			if !slices.Contains(stringsOf(serviceOf(proxy, "webserver")["security_opt"]), optNoNew) {
				t.Fatal("proxy must be hardened too")
			}
		})
	}
}

func TestApply_SkipsIncludeAndExtendsFromHostFS(t *testing.T) {
	t.Parallel()

	extraPath := filepath.Join(t.TempDir(), "extra.yml")

	err := os.WriteFile(extraPath, []byte("services:\n  leaked:\n    image: alpine\n"), 0o600)
	if err != nil {
		t.Fatalf("write include target: %v", err)
	}

	env := newDockerEnv(t)
	compose := fmt.Sprintf(`include:
  - path: %s
services:
  web:
    image: nginx
    extends:
      file: %s
      service: leaked
`, extraPath, extraPath)

	_, err = env.applyAll(t, shopProject(compose))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	doc := appCompose(t, env)

	services := asMap(doc["services"])
	if _, has := services["leaked"]; has {
		t.Fatalf("include/extends must not pull host services: %v", services)
	}

	if _, has := services["web"]; !has {
		t.Fatalf("web service missing: %v", services)
	}
}

func TestApply_ClearsIncludeAndExtendsFromWrittenCompose(t *testing.T) {
	t.Parallel()

	extraPath := filepath.Join(t.TempDir(), "leaked.yml")

	err := os.WriteFile(extraPath, []byte("services:\n  leaked:\n    image: alpine\n"), 0o600)
	if err != nil {
		t.Fatalf("write include target: %v", err)
	}

	env := newDockerEnv(t)
	compose := fmt.Sprintf(`include:
  - path: %s
services:
  web:
    image: nginx
    extends:
      file: %s
      service: leaked
`, extraPath, extraPath)

	_, err = env.applyAll(t, shopProject(compose))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	raw := env.read(t, env.appFile("docker-compose.yml"))
	for _, banned := range []string{"include:", "extends:", "leaked"} {
		if strings.Contains(raw, banned) {
			t.Fatalf("written compose must not contain %q:\n%s", banned, raw)
		}
	}

	doc := readYAML(t, raw)
	if _, has := doc["include"]; has {
		t.Fatalf("top-level include must be cleared: %v", doc["include"])
	}

	services := asMap(doc["services"])
	if _, has := services["leaked"]; has {
		t.Fatalf("leaked service must not appear: %v", services)
	}

	web := asMap(services["web"])
	if web == nil {
		t.Fatalf("web service missing: %v", services)
	}

	if _, has := web["extends"]; has {
		t.Fatalf("web.extends must be cleared: %v", web)
	}
}

func TestApply_NetworkIsolation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		compose      string
		entryPoint   string
		wantAliasFor string // service key that must carry the proxy alias; "" = none
		wantNets     map[string][]string
	}{
		{
			name: "only_proxy_target_joins_proxy_net",
			compose: `services:
  web:
    image: nginx
  db:
    image: postgres
`,
			entryPoint:   "web",
			wantAliasFor: "web",
			wantNets: map[string][]string{
				"web": {netProxy, netShop},
				"db":  {netShop},
			},
		},
		{
			name: "target_resolved_by_container_name",
			compose: `services:
  web:
    image: nginx
    container_name: shop-web
`,
			entryPoint:   "shop-web",
			wantAliasFor: "web",
			wantNets:     map[string][]string{"web": {netProxy, netShop}},
		},
		{
			name: "custom_networks_preserved",
			compose: `services:
  web:
    image: nginx
    networks:
      - backend
networks:
  backend: {}
`,
			entryPoint:   "web",
			wantAliasFor: "web",
			wantNets:     map[string][]string{"web": {netProxy, "backend", netShop}},
		},
		{
			name:         "no_entry_point_no_proxy_net",
			compose:      baseCompose,
			entryPoint:   "",
			wantAliasFor: "",
			wantNets:     map[string][]string{"web": {netShop}},
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newDockerEnv(t)
			project := shopProject(tcase.compose)
			project.EntryPoint = tcase.entryPoint

			res, err := env.applyAll(t, project)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			doc := appCompose(t, env)

			for svcName, want := range tcase.wantNets {
				got := networkKeys(serviceOf(doc, svcName))

				slices.Sort(want)

				if !slices.Equal(got, want) {
					t.Fatalf("%s networks = %v, want %v", svcName, got, want)
				}
			}

			topNets := asMap(doc["networks"])
			if _, ok := topNets[netShop]; !ok {
				t.Fatalf("%s must be declared: %v", netShop, topNets)
			}

			if tcase.wantAliasFor == "" {
				if _, ok := topNets[netProxy]; ok {
					t.Fatalf("proxy net must not be declared without a target: %v", topNets)
				}

				if len(res.ProxyHosts) != 0 {
					t.Fatalf("ProxyHosts = %v, want empty", res.ProxyHosts)
				}

				return
			}

			if asMap(topNets[netProxy])["external"] != true {
				t.Fatalf("%s must be external: %v", netProxy, topNets[netProxy])
			}

			wantAlias := tcase.wantAliasFor + ".apinfra_shop"

			aliases := stringsOf(asMap(asMap(serviceOf(doc, tcase.wantAliasFor)["networks"])[netProxy])["aliases"])
			if !slices.Contains(aliases, wantAlias) {
				t.Fatalf("aliases = %v, want %s", aliases, wantAlias)
			}

			if res.ProxyHosts[projShop] != wantAlias {
				t.Fatalf("ProxyHosts = %v, want %s", res.ProxyHosts, wantAlias)
			}
		})
	}
}

func TestApply_ProxyStackPorts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		inbound []string
		want    []string
	}{
		{name: "defaults_only", inbound: nil, want: []string{"|80|80", "|443|443"}},
		{name: "adds_inbound_ports", inbound: []string{"9999", "8443"}, want: []string{"|80|80", "|443|443", "|9999|9999", "|8443|8443"}},
		{name: "dedupes_and_skips_defaults", inbound: []string{"9999", "9999", "80"}, want: []string{"|80|80", "|443|443", "|9999|9999"}},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			env := newDockerEnv(t)

			var projects []docker.Project

			for i, port := range tcase.inbound {
				p := namedProject(fmt.Sprintf("app%d", i))
				p.InboundPort = port
				projects = append(projects, p)
			}

			if len(projects) == 0 {
				projects = append(projects, namedProject("app0"))
			}

			_, err := env.applyAll(t, projects...)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			proxy := serviceOf(readYAML(t, env.read(t, env.infra+"/docker-compose.yml")), "webserver")

			if got := portsOf(proxy); !slices.Equal(got, tcase.want) {
				t.Fatalf("proxy ports = %v, want %v", got, tcase.want)
			}

			if got := networkKeys(proxy); !slices.Equal(got, []string{netProxy}) {
				t.Fatalf("proxy networks = %v, want only %s", got, netProxy)
			}
		})
	}
}
