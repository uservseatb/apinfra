package nginx

import (
	"fmt"
	"strings"

	"github.com/uservseatb/apinfra/internal/assets"
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
		if hasProxyConf(cfg) {
			keep[serviceConfFileName(cfg)] = struct{}{}
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

func hasProxyConf(cfg Project) bool {
	return cfg.DomainName != "" ||
		(cfg.InboundPort != "" && cfg.ProxyHost != "" && cfg.EntryPointPort != "")
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
		content, ok := s.renderServiceConfig(cfg, installedByName, tmpl)
		if !ok {
			continue
		}

		changed, writeErr := s.fs.WriteConfig(serviceConfFileName(cfg), content, validate)
		if writeErr != nil {
			return anyChanged, fmt.Errorf("write service config %s: %w", cfg.Name, writeErr)
		}

		if changed {
			anyChanged = true
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

func (s *RoutingService) renderServiceConfig(
	cfg Project,
	installedByName map[string]CertDescriptor,
	tmpl nginxTemplates,
) (string, bool) {
	switch {
	case cfg.DomainName != "":
		return s.renderDomainProxy(cfg, installedByName, tmpl.server, tmpl.ssl, tmpl.httpDomain), true
	case cfg.InboundPort != "" && cfg.ProxyHost != "" && cfg.EntryPointPort != "":
		return s.renderPortProxy(cfg, installedByName, tmpl.httpPort, tmpl.httpsPort), true
	default:
		return "", false
	}
}

func (s *RoutingService) renderDomainProxy(
	cfg Project,
	installedByName map[string]CertDescriptor,
	serverTemplate, sslTemplate, httpDomainTemplate string,
) string {
	mode := confCertMode(cfg, installedByName)
	if mode == CertModeNone {
		body := httpDomainTemplate
		body = strings.ReplaceAll(body, tplPlaceholder("domain"), cfg.DomainName)
		body = strings.ReplaceAll(body, tplPlaceholder("host"), cfg.ProxyHost)
		body = strings.ReplaceAll(body, tplPlaceholder("port"), cfg.EntryPointPort)

		return body
	}

	var builder strings.Builder

	httpPart := strings.ReplaceAll(serverTemplate, tplPlaceholder("domain"), cfg.DomainName)
	builder.WriteString(httpPart)

	name := cfg.DomainName

	issued, metaOK := installedByName[name]
	if metaOK && issued.Mode == mode && s.fs.CertFilesPresent(name, mode) &&
		cfg.ProxyHost != "" && cfg.EntryPointPort != "" {
		block := sslTemplate
		block = strings.ReplaceAll(block, tplPlaceholder("domain"), cfg.DomainName)
		block = strings.ReplaceAll(block, tplPlaceholder("domainEscaped"), strings.ReplaceAll(cfg.DomainName, ".", "\\."))
		block = strings.ReplaceAll(block, tplPlaceholder("certSSLDir"), certNginxSSLDir(name, mode))
		block = strings.ReplaceAll(block, tplPlaceholder("host"), cfg.ProxyHost)
		block = strings.ReplaceAll(block, tplPlaceholder("port"), cfg.EntryPointPort)

		builder.WriteString("\n\n")
		builder.WriteString(block)
	}

	return builder.String()
}

func (s *RoutingService) renderPortProxy(
	cfg Project,
	installedByName map[string]CertDescriptor,
	httpPortTemplate, httpsPortTemplate string,
) string {
	mode := confCertMode(cfg, installedByName)

	name := certIdentity(cfg)
	if mode == CertModeMock && name != "" {
		issued, metaOK := installedByName[name]
		if metaOK && issued.Mode == CertModeMock && s.fs.CertFilesPresent(name, CertModeMock) {
			body := httpsPortTemplate
			body = strings.ReplaceAll(body, tplPlaceholder("inboundPort"), cfg.InboundPort)
			body = strings.ReplaceAll(body, tplPlaceholder("certSSLDir"), certNginxSSLDir(name, CertModeMock))
			body = strings.ReplaceAll(body, tplPlaceholder("host"), cfg.ProxyHost)
			body = strings.ReplaceAll(body, tplPlaceholder("port"), cfg.EntryPointPort)

			return body
		}
	}

	body := httpPortTemplate
	body = strings.ReplaceAll(body, tplPlaceholder("inboundPort"), cfg.InboundPort)
	body = strings.ReplaceAll(body, tplPlaceholder("host"), cfg.ProxyHost)
	body = strings.ReplaceAll(body, tplPlaceholder("port"), cfg.EntryPointPort)

	return body
}

// confCertMode is the mode nginx confs are rendered for: settings none always wins;
// otherwise the installed cert mode (e.g. an acme downgrade) when known, else settings.
func confCertMode(cfg Project, installedByName map[string]CertDescriptor) string {
	settings := effectiveCertMode(cfg)
	if settings == CertModeNone {
		return settings
	}

	name := certIdentity(cfg)
	if name != "" {
		if c, ok := installedByName[name]; ok && c.Mode != "" {
			return c.Mode
		}
	}

	return settings
}

func effectiveCertMode(cfg Project) string {
	switch {
	case cfg.CertIssueType != "":
		return cfg.CertIssueType
	case cfg.InboundPort != "" && cfg.DomainName == "":
		return CertModeNone
	default:
		return CertModeMock
	}
}

func tplPlaceholder(name string) string {
	return "$${" + name + "}$$"
}

// certIdentity is the ~/.certs.json / PEM directory key for a proxy project.
func certIdentity(cfg Project) string {
	switch {
	case cfg.DomainName != "":
		return cfg.DomainName
	case cfg.InboundPort != "":
		return "port-" + cfg.InboundPort
	default:
		return ""
	}
}

func serviceConfFileName(cfg Project) string {
	ns := cfg.Namespace
	if ns == "" {
		ns = "default"
	}

	return fmt.Sprintf("%s_%s.conf", ns, cfg.Name)
}

func identityToConfFileNames(projects []Project) map[string]string {
	out := make(map[string]string, len(projects))

	for _, cfg := range projects {
		name := certIdentity(cfg)
		if name == "" {
			continue
		}

		out[name] = serviceConfFileName(cfg)
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
