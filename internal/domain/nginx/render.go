package nginx

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/uservseatb/apinfra/internal/assets"
)

const (
	proxyHostHeaderClient = "$host"
	proxySSLServerNameOn  = "        proxy_ssl_server_name on;\n"
	targetURLSchemeHTTPS  = "https"
)

type nginxTemplates struct {
	server     string
	ssl        string
	httpDomain string
	httpPort   string
	httpsPort  string
}

func (s *RoutingService) syncNginxServiceConfigs(
	projects []Project,
	validate func() error,
) (bool, error) {
	keep := map[string]struct{}{
		defaultConfName: {},
	}

	for _, cfg := range projects {
		for _, route := range cfg.Routes {
			if hasProxyConf(route) {
				keep[routeConfFileName(cfg, route)] = struct{}{}
			}
		}
	}

	removed, err := s.fs.RemoveStaleConfigs(keep, validate)
	if err != nil {
		return false, fmt.Errorf("remove stale configs: %w", err)
	}

	anyConfChanged, err := s.writeServiceConfigs(projects, validate)
	if err != nil {
		return removed, err
	}

	defaultChanged, err := s.writeDefaultConfig(validate)
	if err != nil {
		return removed || anyConfChanged, err
	}

	return removed || anyConfChanged || defaultChanged, nil
}

func hasProxyConf(route Route) bool {
	return route.DomainName != "" ||
		(route.InboundPort != "" && hasComposeUpstream(route))
}

func hasComposeUpstream(route Route) bool {
	return route.ProxyHost != "" && route.EntryPointPort != ""
}

func hasUpstream(route Route) bool {
	return route.TargetURL != "" || hasComposeUpstream(route)
}

func proxyUpstream(route Route) (string, string, string) {
	if route.TargetURL != "" {
		hostHeader := proxyURLHost(route.TargetURL)
		if strings.HasPrefix(route.TargetURL, targetURLSchemeHTTPS+"://") {
			return route.TargetURL, hostHeader, proxySSLServerNameOn
		}

		return route.TargetURL, hostHeader, ""
	}

	return "http://" + net.JoinHostPort(route.ProxyHost, route.EntryPointPort), proxyHostHeaderClient, ""
}

func proxyURLHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return raw
	}

	return parsed.Hostname()
}

func applyProxyUpstream(body string, route Route) string {
	pass, hostHeader, sslDirectives := proxyUpstream(route)
	body = strings.ReplaceAll(body, tplPlaceholder("proxyPass"), pass)
	body = strings.ReplaceAll(body, tplPlaceholder("proxyHostHeader"), hostHeader)
	body = strings.ReplaceAll(body, tplPlaceholder("proxySSLDirectives"), sslDirectives)

	return body
}

func (s *RoutingService) rewriteConfigsIfCertsChanged(
	projects []Project,
	validate func() error,
	anyConfChanged, certsChanged bool,
) (bool, error) {
	if !certsChanged {
		return anyConfChanged, nil
	}

	changed, err := s.writeServiceConfigs(projects, validate)
	if err != nil {
		return anyConfChanged, err
	}

	return anyConfChanged || changed, nil
}

func (s *RoutingService) writeDefaultConfig(validate func() error) (bool, error) {
	defaultTemplate, err := assets.LoadText("templates/appdef.conf.tpl")
	if err != nil {
		return false, fmt.Errorf("load templates/appdef.conf.tpl: %w", err)
	}

	changed, err := s.fs.WriteConfig(defaultConfName, defaultTemplate, validate)
	if err != nil {
		return false, fmt.Errorf("write default config: %w", err)
	}

	return changed, nil
}

func (s *RoutingService) writeServiceConfigs(
	projects []Project,
	validate func() error,
) (bool, error) {
	tmpl, err := loadNginxTemplates()
	if err != nil {
		return false, err
	}

	installedByName, err := s.installedCertMap()
	if err != nil {
		return false, fmt.Errorf("load .certs.json: %w", err)
	}

	anyChanged := false

	for _, cfg := range projects {
		for _, route := range cfg.Routes {
			content, ok := s.renderRouteConfig(route, installedByName, tmpl)
			if !ok {
				continue
			}

			fileName := routeConfFileName(cfg, route)

			changed, writeErr := s.fs.WriteConfig(fileName, content, validate)
			if writeErr != nil {
				return anyChanged, fmt.Errorf("write service config %s: %w", fileName, writeErr)
			}

			if changed {
				anyChanged = true
			}
		}
	}

	return anyChanged, nil
}

func loadNginxTemplates() (nginxTemplates, error) {
	var tmpl nginxTemplates

	server, err := assets.LoadText("templates/app.conf.tpl")
	if err != nil {
		return tmpl, fmt.Errorf("load templates/app.conf.tpl: %w", err)
	}

	ssl, err := assets.LoadText("templates/ssl.conf.tpl")
	if err != nil {
		return tmpl, fmt.Errorf("load templates/ssl.conf.tpl: %w", err)
	}

	httpDomain, err := assets.LoadText("templates/http-domain.conf.tpl")
	if err != nil {
		return tmpl, fmt.Errorf("load templates/http-domain.conf.tpl: %w", err)
	}

	httpPort, err := assets.LoadText("templates/http-port.conf.tpl")
	if err != nil {
		return tmpl, fmt.Errorf("load templates/http-port.conf.tpl: %w", err)
	}

	httpsPort, err := assets.LoadText("templates/https-port.conf.tpl")
	if err != nil {
		return tmpl, fmt.Errorf("load templates/https-port.conf.tpl: %w", err)
	}

	tmpl.server = server
	tmpl.ssl = ssl
	tmpl.httpDomain = httpDomain
	tmpl.httpPort = httpPort
	tmpl.httpsPort = httpsPort

	return tmpl, nil
}

func (s *RoutingService) renderRouteConfig(
	route Route,
	installedByName map[string]CertDescriptor,
	tmpl nginxTemplates,
) (string, bool) {
	switch {
	case route.DomainName != "":
		return s.renderDomainProxy(route, installedByName, tmpl.server, tmpl.ssl, tmpl.httpDomain), true
	case route.InboundPort != "" && hasComposeUpstream(route):
		return s.renderPortProxy(route, installedByName, tmpl.httpPort, tmpl.httpsPort), true
	default:
		return "", false
	}
}

func (s *RoutingService) renderDomainProxy(
	route Route,
	installedByName map[string]CertDescriptor,
	serverTemplate, sslTemplate, httpDomainTemplate string,
) string {
	mode := confCertMode(route, installedByName)
	if mode == CertModeNone {
		body := httpDomainTemplate
		body = strings.ReplaceAll(body, tplPlaceholder("domain"), route.DomainName)

		return applyProxyUpstream(body, route)
	}

	var builder strings.Builder

	httpPart := strings.ReplaceAll(serverTemplate, tplPlaceholder("domain"), route.DomainName)
	builder.WriteString(httpPart)

	name := route.DomainName

	issued, metaOK := installedByName[name]
	if metaOK && issued.Mode == mode && s.fs.CertFilesPresent(name, mode) && hasUpstream(route) {
		block := sslTemplate
		block = strings.ReplaceAll(block, tplPlaceholder("domain"), route.DomainName)
		block = strings.ReplaceAll(block, tplPlaceholder("domainEscaped"), strings.ReplaceAll(route.DomainName, ".", "\\."))
		block = strings.ReplaceAll(block, tplPlaceholder("certSSLDir"), certNginxSSLDir(name, mode))
		block = applyProxyUpstream(block, route)

		builder.WriteString("\n\n")
		builder.WriteString(block)
	}

	return builder.String()
}

func (s *RoutingService) renderPortProxy(
	route Route,
	installedByName map[string]CertDescriptor,
	httpPortTemplate, httpsPortTemplate string,
) string {
	mode := confCertMode(route, installedByName)

	name := certIdentity(route)
	if mode == CertModeMock && name != "" {
		issued, metaOK := installedByName[name]
		if metaOK && issued.Mode == CertModeMock && s.fs.CertFilesPresent(name, CertModeMock) {
			body := httpsPortTemplate
			body = strings.ReplaceAll(body, tplPlaceholder("inboundPort"), route.InboundPort)
			body = strings.ReplaceAll(body, tplPlaceholder("certSSLDir"), certNginxSSLDir(name, CertModeMock))

			return applyProxyUpstream(body, route)
		}
	}

	body := httpPortTemplate
	body = strings.ReplaceAll(body, tplPlaceholder("inboundPort"), route.InboundPort)

	return applyProxyUpstream(body, route)
}

// confCertMode is the mode nginx confs are rendered for: settings none always wins;
// otherwise the installed cert mode (e.g. an acme downgrade) when known, else settings.
func confCertMode(route Route, installedByName map[string]CertDescriptor) string {
	settings := effectiveCertMode(route)
	if settings == CertModeNone {
		return settings
	}

	name := certIdentity(route)
	if name != "" {
		if c, ok := installedByName[name]; ok && c.Mode != "" {
			return c.Mode
		}
	}

	return settings
}

func effectiveCertMode(route Route) string {
	switch {
	case route.CertIssueType != "":
		return route.CertIssueType
	case route.InboundPort != "" && route.DomainName == "":
		return CertModeNone
	default:
		return CertModeMock
	}
}

func tplPlaceholder(name string) string {
	return "$${" + name + "}$$"
}

// certIdentity is the ~/.certs.json / PEM directory key for a proxy route.
func certIdentity(route Route) string {
	switch {
	case route.DomainName != "":
		return route.DomainName
	case route.InboundPort != "":
		return "port-" + route.InboundPort
	default:
		return ""
	}
}

func routeConfFileName(project Project, route Route) string {
	ns := project.Namespace
	if ns == "" {
		ns = "default"
	}

	return fmt.Sprintf("%s_%s__%s.conf", ns, project.Name, certIdentity(route))
}

func identityToConfFileNames(projects []Project) map[string]string {
	out := make(map[string]string)

	for _, cfg := range projects {
		for _, route := range cfg.Routes {
			name := certIdentity(route)
			if name == "" {
				continue
			}

			out[name] = routeConfFileName(cfg, route)
		}
	}

	return out
}

// certNginxSSLDir returns the in-container directory for nginx ssl_certificate paths.
func certNginxSSLDir(domainName, mode string) string {
	if mode == CertModeMock {
		return "/etc/nginx/mockcerts/" + domainName
	}

	return "/etc/nginx/ssl/live/" + domainName
}
