package config

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var errNilRepo = errors.New("nil repo")

// ReadSettings reads and validates settings.yml from the config repo for the given namespace.
// Path is <namespace>/settings.yml (no "namespace/" prefix). Disabled projects are omitted.
func ReadSettings(repo fileReader, namespace string) ([]ServiceConfig, error) {
	if repo == nil {
		return nil, errNilRepo
	}

	nsPrefix := namespace
	if !repo.Exists(nsPrefix) {
		return nil, formatSettingsError(fmt.Sprintf("environment folder %q not found", namespace))
	}

	settingsPath := path.Join(nsPrefix, "settings.yml")

	data, err := repo.ReadFile(settingsPath)
	if err != nil {
		return nil, formatSettingsError(fmt.Sprintf("read settings.yml for %s: %s", namespace, err))
	}

	var raw map[string]any

	err = yaml.Unmarshal(data, &raw)
	if err != nil {
		return nil, formatSettingsError(fmt.Sprintf("parse YAML: %s", err))
	}

	return toProjects(raw, namespace, nsPrefix)
}

func toProjects(raw map[string]any, namespace, nsPrefix string) ([]ServiceConfig, error) {
	if raw == nil {
		return nil, formatSettingsError("empty document")
	}

	err := requireSettingsVersion(raw)
	if err != nil {
		return nil, err
	}

	projList, err := requireProjectsList(raw)
	if err != nil {
		return nil, err
	}

	seenNames := make(map[string]string)
	inboundPorts := make(map[string]string) // inboundPort -> project name
	domainNames := make(map[string]string)  // domain-name -> project name

	var result []ServiceConfig

	for idx, item := range projList {
		cfg, skip, parseErr := parseProjectEntry(
			item, idx, namespace, nsPrefix, seenNames, inboundPorts, domainNames,
		)
		if parseErr != nil {
			return nil, parseErr
		}

		if skip {
			continue
		}

		result = append(result, cfg)
	}

	return result, nil
}

func requireSettingsVersion(raw map[string]any) error {
	verRaw, exists := raw["version"]
	if !exists || verRaw == nil {
		return formatSettingsError("version is required")
	}

	ver, isString := verRaw.(string)
	if !isString || strings.TrimSpace(ver) == "" {
		return formatSettingsError("version must be a non-empty string")
	}

	return nil
}

func requireProjectsList(raw map[string]any) ([]any, error) {
	projRaw, exists := raw["projects"]
	if !exists {
		return nil, formatSettingsError("projects is required")
	}

	projList, isList := projRaw.([]any)
	if !isList {
		return nil, formatSettingsError("projects must be a sequence")
	}

	return projList, nil
}

func parseProjectEntry(
	item any,
	idx int,
	namespace, nsPrefix string,
	seenNames, inboundPorts, domainNames map[string]string,
) (ServiceConfig, bool, error) {
	mapping, name, err := projectMappingAndName(item, idx, seenNames)
	if err != nil {
		return emptyServiceConfig(), false, err
	}

	enabled, err := projectEnabled(mapping, name)
	if err != nil {
		return emptyServiceConfig(), false, err
	}

	if !enabled {
		return emptyServiceConfig(), true, nil
	}

	return buildEnabledProject(mapping, name, namespace, nsPrefix, inboundPorts, domainNames)
}

func projectMappingAndName(
	item any,
	idx int,
	seenNames map[string]string,
) (map[string]any, string, error) {
	mapping, ok := item.(map[string]any)
	if !ok || mapping == nil {
		return nil, "", formatSettingsError(fmt.Sprintf("projects[%d] must be a mapping", idx))
	}

	name, _ := mapping["name"].(string)
	name = strings.TrimSpace(name)

	if name == "" {
		return nil, "", formatSettingsError(fmt.Sprintf("projects[%d].name is required", idx))
	}

	err := validateProjectName(name)
	if err != nil {
		return nil, "", err
	}

	upper := strings.ToUpper(name)
	if prev, dup := seenNames[upper]; dup {
		return nil, "", formatSettingsError(
			fmt.Sprintf("duplicate project name %q (case-insensitive, conflicts with %q)", name, prev),
		)
	}

	seenNames[upper] = name

	return mapping, name, nil
}

func buildEnabledProject(
	mapping map[string]any,
	name, namespace, nsPrefix string,
	inboundPorts, domainNames map[string]string,
) (ServiceConfig, bool, error) {
	projectCertDefaults, err := parseProjectCertDefaults(mapping, name)
	if err != nil {
		return emptyServiceConfig(), false, err
	}

	routes, err := parseProjectRoutes(mapping, name, projectCertDefaults)
	if err != nil {
		return emptyServiceConfig(), false, err
	}

	for i := range routes {
		err = registerInboundPort(inboundPorts, name, routes[i].InboundPort)
		if err != nil {
			return emptyServiceConfig(), false, err
		}

		err = registerDomainName(domainNames, name, routes[i].DomainName)
		if err != nil {
			return emptyServiceConfig(), false, err
		}
	}

	allowPorts, err := parsePortsMapping(mapping, name)
	if err != nil {
		return emptyServiceConfig(), false, err
	}

	return ServiceConfig{
		Namespace:         namespace,
		NamespacePrefix:   nsPrefix,
		Name:              name,
		Routes:            routes,
		AllowPortsMapping: allowPorts,
	}, false, nil
}

func projectEnabled(mapping map[string]any, name string) (bool, error) {
	ev, has := mapping["enabled"]
	if !has {
		return true, nil
	}

	enabled, isBool := ev.(bool)
	if !isBool {
		return false, formatSettingsError(fmt.Sprintf("project %s: enabled must be a boolean", name))
	}

	return enabled, nil
}

func registerInboundPort(inboundPorts map[string]string, name, inboundPort string) error {
	if inboundPort == "" {
		return nil
	}

	if prev, dup := inboundPorts[inboundPort]; dup {
		return formatSettingsError(
			fmt.Sprintf("project %s: proxy.inbound.port %s already used by project %q", name, inboundPort, prev),
		)
	}

	inboundPorts[inboundPort] = name

	return nil
}

func registerDomainName(domainNames map[string]string, name, domainName string) error {
	if domainName == "" {
		return nil
	}

	key := strings.ToLower(domainName)
	if prev, dup := domainNames[key]; dup {
		return formatSettingsError(
			fmt.Sprintf("project %s: proxy.inbound.domain-name %s already used by project %q", name, domainName, prev),
		)
	}

	domainNames[key] = name

	return nil
}

type certFieldDefaults struct {
	issueTypeSet bool
	issueType    string
	updateSecSet bool
	updateSec    int64
	acmeEmailSet bool
	acmeEmail    string
}

// parseProjectCertDefaults reads optional cert fields from projects[].config (route defaults).
func parseProjectCertDefaults(mapping map[string]any, projectName string) (certFieldDefaults, error) {
	cfg, err := projectConfigMap(mapping, projectName)
	if err != nil {
		return certFieldDefaults{}, err
	}

	return parseCertFields(cfg, projectName, "config")
}

func parseCertFields(cfg map[string]any, projectName, scope string) (certFieldDefaults, error) {
	var out certFieldDefaults

	if raw, has := cfg["cert-issue-type"]; has && raw != nil {
		issueType, err := parseCertIssueTypeValue(raw, projectName, scope)
		if err != nil {
			return certFieldDefaults{}, err
		}

		out.issueTypeSet = true
		out.issueType = issueType
	}

	if raw, has := cfg["cert-update-time-sec"]; has && raw != nil {
		sec, err := positiveInt64(raw, fmt.Sprintf("project %s: %s.cert-update-time-sec", projectName, scope))
		if err != nil {
			return certFieldDefaults{}, err
		}

		out.updateSecSet = true
		out.updateSec = sec
	}

	if raw, has := cfg["cert-acme-email"]; has && raw != nil {
		email, isString := raw.(string)
		if !isString {
			return certFieldDefaults{}, formatSettingsError(
				fmt.Sprintf("project %s: %s.cert-acme-email must be a string", projectName, scope),
			)
		}

		email = strings.TrimSpace(email)

		err := validateAcmeEmail(email, projectName)
		if err != nil {
			return certFieldDefaults{}, err
		}

		out.acmeEmailSet = true
		out.acmeEmail = email
	}

	return out, nil
}

func parseCertIssueTypeValue(raw any, projectName, scope string) (string, error) {
	issueType, isString := raw.(string)
	if !isString {
		return "", formatSettingsError(
			fmt.Sprintf("project %s: %s.cert-issue-type must be a string", projectName, scope),
		)
	}

	issueType = strings.TrimSpace(issueType)
	if issueType == "" {
		return "", formatSettingsError(
			fmt.Sprintf("project %s: %s.cert-issue-type must be a non-empty string", projectName, scope),
		)
	}

	switch issueType {
	case certIssueMock, certIssueProd, certIssueTest, certIssueNone:
		return issueType, nil
	default:
		return "", formatSettingsError(
			fmt.Sprintf("project %s: %s.cert-issue-type must be mock, prod, test, or none", projectName, scope),
		)
	}
}

func applyRouteCertConfig(
	route *Route,
	routeCfg map[string]any,
	defaults certFieldDefaults,
	projectName, label string,
) error {
	portOnly := route.InboundPort != "" && route.DomainName == ""
	issueType := defaultCertIssueType(portOnly)

	if defaults.issueTypeSet {
		issueType = defaults.issueType
	}

	routeFields, err := parseCertFields(routeCfg, projectName, label+".config")
	if err != nil {
		return err
	}

	if routeFields.issueTypeSet {
		issueType = routeFields.issueType
	}

	if portOnly && issueType != certIssueMock && issueType != certIssueNone {
		return formatSettingsError(
			fmt.Sprintf(
				"project %s: %s config.cert-issue-type for proxy.inbound.port must be mock or none (got %s)",
				projectName, label, issueType,
			),
		)
	}

	updateSec := defaultCertUpdateSec(issueType)
	if defaults.updateSecSet {
		updateSec = defaults.updateSec
	}

	if routeFields.updateSecSet {
		updateSec = routeFields.updateSec
	}

	acmeEmail := ""
	if defaults.acmeEmailSet {
		acmeEmail = defaults.acmeEmail
	}

	if routeFields.acmeEmailSet {
		acmeEmail = routeFields.acmeEmail
	}

	err = requireCertAcmeEmail(route.DomainName, issueType, acmeEmail, projectName)
	if err != nil {
		return err
	}

	route.CertIssueType = issueType
	route.CertUpdateSec = updateSec
	route.CertAcmeEmail = acmeEmail

	return nil
}

func projectConfigMap(mapping map[string]any, projectName string) (map[string]any, error) {
	cv, has := mapping["config"]
	if !has || cv == nil {
		return map[string]any{}, nil
	}

	cfg, isMap := cv.(map[string]any)
	if !isMap {
		return nil, formatSettingsError(fmt.Sprintf("project %s: config must be a mapping", projectName))
	}

	return cfg, nil
}

func defaultCertIssueType(portOnly bool) string {
	if portOnly {
		return certIssueNone
	}

	return certIssueMock
}

func requireCertAcmeEmail(domainName, issueType, acmeEmail, projectName string) error {
	needsEmail := domainName != "" && (issueType == certIssueProd || issueType == certIssueTest)
	if needsEmail && acmeEmail == "" {
		return formatSettingsError(
			fmt.Sprintf(
				"project %s: config.cert-acme-email is required when cert-issue-type is %s and domain-name is set",
				projectName, issueType,
			),
		)
	}

	return nil
}

func defaultCertUpdateSec(issueType string) int64 {
	switch issueType {
	case certIssueProd:
		return defaultCertUpdateSecProd
	case certIssueTest:
		return defaultCertUpdateSecTest
	case certIssueNone:
		return defaultCertUpdateSecNone
	default:
		return defaultCertUpdateSecMock
	}
}

func positiveInt64(val any, label string) (int64, error) {
	var num int64

	switch typed := val.(type) {
	case int:
		num = int64(typed)
	case int64:
		num = typed
	case float64:
		num = int64(typed)
	case string:
		str := strings.TrimSpace(typed)
		if str == "" {
			return 0, formatSettingsError(label + " must be a positive number")
		}

		parsed, err := strconv.ParseInt(str, 10, 64)
		if err != nil {
			return 0, formatSettingsError(label + " must be a number")
		}

		num = parsed
	default:
		return 0, formatSettingsError(label + " must be a number or string")
	}

	if num <= 0 {
		return 0, formatSettingsError(label + " must be a positive number")
	}

	return num, nil
}

func parseProjectRoutes(
	mapping map[string]any,
	projectName string,
	defaults certFieldDefaults,
) ([]Route, error) {
	_, hasProxy := mapping["proxy"]
	proxyPresent := hasProxy && mapping["proxy"] != nil

	_, hasProxies := mapping["proxies"]
	proxiesPresent := hasProxies && mapping["proxies"] != nil

	if proxyPresent && proxiesPresent {
		return nil, formatSettingsError(
			fmt.Sprintf("project %s: proxy and proxies are mutually exclusive; prefer proxies", projectName),
		)
	}

	switch {
	case proxiesPresent:
		return parseProxiesList(mapping["proxies"], projectName, defaults)
	case proxyPresent:
		route, err := parseOneRoute(mapping["proxy"], projectName, "proxy", defaults)
		if err != nil {
			return nil, err
		}

		return []Route{route}, nil
	default:
		return nil, nil
	}
}

func parseProxiesList(raw any, projectName string, defaults certFieldDefaults) ([]Route, error) {
	list, isList := raw.([]any)
	if !isList {
		return nil, formatSettingsError(
			fmt.Sprintf("project %s: proxies must be a sequence", projectName),
		)
	}

	if len(list) == 0 {
		return nil, formatSettingsError(
			fmt.Sprintf("project %s: proxies must not be empty", projectName),
		)
	}

	routes := make([]Route, 0, len(list))
	for i, item := range list {
		label := fmt.Sprintf("proxies[%d]", i)

		route, err := parseOneRoute(item, projectName, label, defaults)
		if err != nil {
			return nil, err
		}

		routes = append(routes, route)
	}

	return routes, nil
}

func parseOneRoute(
	raw any,
	projectName, label string,
	defaults certFieldDefaults,
) (Route, error) {
	proxy, isMap := raw.(map[string]any)
	if !isMap || proxy == nil {
		return Route{}, formatSettingsError(
			fmt.Sprintf("project %s: %s must be a mapping", projectName, label),
		)
	}

	domainName, inboundPort, err := parseProxyInbound(proxy, projectName, label)
	if err != nil {
		return Route{}, err
	}

	entryPoint, entryPointPort, targetURL, err := parseProxyTarget(proxy, projectName, label)
	if err != nil {
		return Route{}, err
	}

	if targetURL != "" && inboundPort != "" {
		return Route{}, formatSettingsError(
			fmt.Sprintf(
				"project %s: %s.target.url is only allowed with %s.inbound.domain-name",
				projectName, label, label,
			),
		)
	}

	routeCfg, err := optionalRouteConfigMap(proxy, projectName, label)
	if err != nil {
		return Route{}, err
	}

	route := Route{
		DomainName:     domainName,
		InboundPort:    inboundPort,
		EntryPoint:     entryPoint,
		EntryPointPort: entryPointPort,
		TargetURL:      targetURL,
		ProxyHost:      "",
		CertIssueType:  "",
		CertUpdateSec:  0,
		CertAcmeEmail:  "",
	}

	err = applyRouteCertConfig(&route, routeCfg, defaults, projectName, label)
	if err != nil {
		return Route{}, err
	}

	return route, nil
}

func optionalRouteConfigMap(proxy map[string]any, projectName, label string) (map[string]any, error) {
	cv, has := proxy["config"]
	if !has || cv == nil {
		return map[string]any{}, nil
	}

	cfg, isMap := cv.(map[string]any)
	if !isMap {
		return nil, formatSettingsError(
			fmt.Sprintf("project %s: %s.config must be a mapping", projectName, label),
		)
	}

	return cfg, nil
}

func parseProxyInbound(proxy map[string]any, projectName, label string) (string, string, error) {
	inboundRaw, exists := proxy["inbound"]
	if !exists || inboundRaw == nil {
		return "", "", formatSettingsError(
			fmt.Sprintf("project %s: %s.inbound is required", projectName, label),
		)
	}

	inbound, isMap := inboundRaw.(map[string]any)
	if !isMap {
		return "", "", formatSettingsError(
			fmt.Sprintf("project %s: %s.inbound must be a mapping", projectName, label),
		)
	}

	domainName, _ := inbound["domain-name"].(string)
	domainName = strings.TrimSpace(domainName)

	err := validateDomainName(domainName, projectName)
	if err != nil {
		return "", "", err
	}

	inboundPort, err := optionalInboundPort(
		inbound["port"],
		fmt.Sprintf("project %s: %s.inbound.port", projectName, label),
	)
	if err != nil {
		return "", "", err
	}

	err = validateInboundExclusive(domainName, inboundPort, projectName, label)
	if err != nil {
		return "", "", err
	}

	return domainName, inboundPort, nil
}

func validateInboundExclusive(domainName, inboundPort, projectName, label string) error {
	hasDomain := domainName != ""
	hasPort := inboundPort != ""

	switch {
	case hasDomain && hasPort:
		return formatSettingsError(
			fmt.Sprintf(
				"project %s: %s.inbound.domain-name and %s.inbound.port are mutually exclusive",
				projectName, label, label,
			),
		)
	case !hasDomain && !hasPort:
		return formatSettingsError(
			fmt.Sprintf("project %s: %s.inbound requires domain-name or port", projectName, label),
		)
	default:
		return nil
	}
}

func parseProxyTarget(proxy map[string]any, projectName, label string) (string, string, string, error) {
	targetRaw, exists := proxy["target"]
	if !exists || targetRaw == nil {
		return "", "", "", formatSettingsError(
			fmt.Sprintf("project %s: %s.target is required", projectName, label),
		)
	}

	target, isMap := targetRaw.(map[string]any)
	if !isMap {
		return "", "", "", formatSettingsError(
			fmt.Sprintf("project %s: %s.target must be a mapping", projectName, label),
		)
	}

	urlRaw, _ := target["url"].(string)
	urlRaw = strings.TrimSpace(urlRaw)

	serviceRaw, hasServiceKey := target["service"]
	hasURL := urlRaw != ""
	hasService := hasServiceKey && serviceRaw != nil

	switch {
	case hasURL && hasService:
		return "", "", "", formatSettingsError(
			fmt.Sprintf(
				"project %s: %s.target.url and %s.target.service are mutually exclusive",
				projectName, label, label,
			),
		)
	case hasURL:
		normalized, err := validateTargetURL(urlRaw, projectName)
		if err != nil {
			return "", "", "", err
		}

		return "", "", normalized, nil
	case hasService:
		entryPoint, entryPointPort, err := parseProxyTargetService(serviceRaw, projectName, label)
		if err != nil {
			return "", "", "", err
		}

		return entryPoint, entryPointPort, "", nil
	default:
		return "", "", "", formatSettingsError(
			fmt.Sprintf(
				"project %s: %s.target requires service or url",
				projectName, label,
			),
		)
	}
}

func parseProxyTargetService(serviceRaw any, projectName, label string) (string, string, error) {
	service, isMap := serviceRaw.(map[string]any)
	if !isMap {
		return "", "", formatSettingsError(
			fmt.Sprintf("project %s: %s.target.service must be a mapping", projectName, label),
		)
	}

	entryPoint, _ := service["name"].(string)
	entryPoint = strings.TrimSpace(entryPoint)

	err := validateServiceName(entryPoint, projectName)
	if err != nil {
		return "", "", err
	}

	entryPointPort, err := parsePortInRange(
		service["port"],
		fmt.Sprintf("project %s: %s.target.service.port", projectName, label),
		minTargetPort,
		maxTargetPort,
	)
	if err != nil {
		return "", "", err
	}

	return entryPoint, entryPointPort, nil
}

func optionalInboundPort(val any, label string) (string, error) {
	if val == nil {
		return "", nil
	}

	return parsePortInRange(val, label, minInboundPort, maxInboundPort)
}

func emptyServiceConfig() ServiceConfig {
	return ServiceConfig{
		Namespace:         "",
		NamespacePrefix:   "",
		Name:              "",
		Routes:            nil,
		AllowPortsMapping: false,
	}
}

const keyAllowPortsMapping = "allow-ports-mapping"

// parsePortsMapping reads projects[].config.allow-ports-mapping (default false).
// Top-level projects[].allow-ports-mapping is still accepted for compatibility.
func parsePortsMapping(mapping map[string]any, projectName string) (bool, error) {
	cfg, err := projectConfigMap(mapping, projectName)
	if err != nil {
		return false, err
	}

	if _, has := cfg[keyAllowPortsMapping]; has {
		return optionalProjectBool(cfg, keyAllowPortsMapping, projectName)
	}

	return optionalProjectBool(mapping, keyAllowPortsMapping, projectName)
}

func optionalProjectBool(mapping map[string]any, key, projectName string) (bool, error) {
	raw, has := mapping[key]
	if !has || raw == nil {
		return false, nil
	}

	value, isBool := raw.(bool)
	if !isBool {
		return false, formatSettingsError(
			fmt.Sprintf("project %s: %s must be a boolean", projectName, key),
		)
	}

	return value, nil
}
