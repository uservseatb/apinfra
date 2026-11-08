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

	var result []ServiceConfig

	for idx, item := range projList {
		cfg, skip, parseErr := parseProjectEntry(item, idx, namespace, nsPrefix, seenNames, inboundPorts)
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
	seenNames, inboundPorts map[string]string,
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

	return buildEnabledProject(mapping, name, namespace, nsPrefix, inboundPorts)
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
	inboundPorts map[string]string,
) (ServiceConfig, bool, error) {
	domainName, inboundPort, entryPoint, entryPointPort, err := parseProxy(mapping, name)
	if err != nil {
		return emptyServiceConfig(), false, err
	}

	err = registerInboundPort(inboundPorts, name, inboundPort)
	if err != nil {
		return emptyServiceConfig(), false, err
	}

	certIssueType, certUpdateSec, certAcmeEmail, err := parseProjectCertConfig(mapping, name, domainName, inboundPort)
	if err != nil {
		return emptyServiceConfig(), false, err
	}

	allowPorts, err := parsePortsMapping(mapping, name)
	if err != nil {
		return emptyServiceConfig(), false, err
	}

	return ServiceConfig{
		Namespace:         namespace,
		NamespacePrefix:   nsPrefix,
		Name:              name,
		DomainName:        domainName,
		InboundPort:       inboundPort,
		EntryPoint:        entryPoint,
		EntryPointPort:    entryPointPort,
		ProxyHost:         "",
		CertIssueType:     certIssueType,
		CertUpdateSec:     certUpdateSec,
		CertAcmeEmail:     certAcmeEmail,
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

// parseProjectCertConfig reads projects[].config cert fields.
// Missing cert-issue-type defaults to mock for domain-name routing and none for inbound.port routing.
// Domain routing allows mock|prod|test|none; port routing allows only mock|none.
func parseProjectCertConfig(
	mapping map[string]any,
	projectName, domainName, inboundPort string,
) (string, int64, string, error) {
	cfg, err := projectConfigMap(mapping, projectName)
	if err != nil {
		return "", 0, "", err
	}

	portOnly := inboundPort != "" && domainName == ""
	issueType := defaultCertIssueType(portOnly)

	issueType, err = resolveCertIssueType(cfg, projectName, issueType)
	if err != nil {
		return "", 0, "", err
	}

	if portOnly && issueType != certIssueMock && issueType != certIssueNone {
		return "", 0, "", formatSettingsError(
			fmt.Sprintf(
				"project %s: config.cert-issue-type for proxy.inbound.port must be mock or none (got %s)",
				projectName, issueType,
			),
		)
	}

	updateSec, acmeEmail, err := resolveCertExtras(cfg, projectName, issueType)
	if err != nil {
		return "", 0, "", err
	}

	err = requireCertAcmeEmail(domainName, issueType, acmeEmail, projectName)
	if err != nil {
		return "", 0, "", err
	}

	return issueType, updateSec, acmeEmail, nil
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

func resolveCertIssueType(cfg map[string]any, projectName, fallback string) (string, error) {
	raw, has := cfg["cert-issue-type"]
	if !has || raw == nil {
		return fallback, nil
	}

	issueType, isString := raw.(string)
	if !isString {
		return "", formatSettingsError(
			fmt.Sprintf("project %s: config.cert-issue-type must be a string", projectName),
		)
	}

	issueType = strings.TrimSpace(issueType)
	if issueType == "" {
		return "", formatSettingsError(
			fmt.Sprintf("project %s: config.cert-issue-type must be a non-empty string", projectName),
		)
	}

	switch issueType {
	case certIssueMock, certIssueProd, certIssueTest, certIssueNone:
		return issueType, nil
	default:
		return "", formatSettingsError(
			fmt.Sprintf("project %s: config.cert-issue-type must be mock, prod, test, or none", projectName),
		)
	}
}

func resolveCertExtras(cfg map[string]any, projectName, issueType string) (int64, string, error) {
	updateSec := defaultCertUpdateSec(issueType)
	acmeEmail := ""

	if raw, has := cfg["cert-update-time-sec"]; has && raw != nil {
		sec, parseErr := positiveInt64(raw, fmt.Sprintf("project %s: config.cert-update-time-sec", projectName))
		if parseErr != nil {
			return 0, "", parseErr
		}

		updateSec = sec
	}

	if raw, has := cfg["cert-acme-email"]; has && raw != nil {
		email, isString := raw.(string)
		if !isString {
			return 0, "", formatSettingsError(
				fmt.Sprintf("project %s: config.cert-acme-email must be a string", projectName),
			)
		}

		acmeEmail = strings.TrimSpace(email)
	}

	err := validateAcmeEmail(acmeEmail, projectName)
	if err != nil {
		return 0, "", err
	}

	return updateSec, acmeEmail, nil
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

func parseProxy(mapping map[string]any, projectName string) (string, string, string, string, error) {
	pv, hasProxy := mapping["proxy"]
	if !hasProxy || pv == nil {
		return "", "", "", "", nil
	}

	proxy, isMap := pv.(map[string]any)
	if !isMap {
		return "", "", "", "", formatSettingsError(
			fmt.Sprintf("project %s: proxy must be a mapping", projectName),
		)
	}

	domainName, inboundPort, err := parseProxyInbound(proxy, projectName)
	if err != nil {
		return "", "", "", "", err
	}

	entryPoint, entryPointPort, err := parseProxyTarget(proxy, projectName)
	if err != nil {
		return "", "", "", "", err
	}

	return domainName, inboundPort, entryPoint, entryPointPort, nil
}

func parseProxyInbound(proxy map[string]any, projectName string) (string, string, error) {
	inboundRaw, exists := proxy["inbound"]
	if !exists || inboundRaw == nil {
		return "", "", formatSettingsError(
			fmt.Sprintf("project %s: proxy.inbound is required", projectName),
		)
	}

	inbound, isMap := inboundRaw.(map[string]any)
	if !isMap {
		return "", "", formatSettingsError(
			fmt.Sprintf("project %s: proxy.inbound must be a mapping", projectName),
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
		fmt.Sprintf("project %s: proxy.inbound.port", projectName),
	)
	if err != nil {
		return "", "", err
	}

	err = validateInboundExclusive(domainName, inboundPort, projectName)
	if err != nil {
		return "", "", err
	}

	return domainName, inboundPort, nil
}

func validateInboundExclusive(domainName, inboundPort, projectName string) error {
	hasDomain := domainName != ""
	hasPort := inboundPort != ""

	switch {
	case hasDomain && hasPort:
		return formatSettingsError(
			fmt.Sprintf(
				"project %s: proxy.inbound.domain-name and proxy.inbound.port are mutually exclusive",
				projectName,
			),
		)
	case !hasDomain && !hasPort:
		return formatSettingsError(
			fmt.Sprintf("project %s: proxy.inbound requires domain-name or port", projectName),
		)
	default:
		return nil
	}
}

func parseProxyTarget(proxy map[string]any, projectName string) (string, string, error) {
	targetRaw, exists := proxy["target"]
	if !exists || targetRaw == nil {
		return "", "", formatSettingsError(
			fmt.Sprintf("project %s: proxy.target is required", projectName),
		)
	}

	target, isMap := targetRaw.(map[string]any)
	if !isMap {
		return "", "", formatSettingsError(
			fmt.Sprintf("project %s: proxy.target must be a mapping", projectName),
		)
	}

	entryPoint, _ := target["service-name"].(string)
	entryPoint = strings.TrimSpace(entryPoint)

	err := validateServiceName(entryPoint, projectName)
	if err != nil {
		return "", "", err
	}

	entryPointPort, err := parsePortInRange(
		target["port"],
		fmt.Sprintf("project %s: proxy.target.port", projectName),
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
		DomainName:        "",
		InboundPort:       "",
		EntryPoint:        "",
		EntryPointPort:    "",
		ProxyHost:         "",
		CertIssueType:     "",
		CertUpdateSec:     0,
		CertAcmeEmail:     "",
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
