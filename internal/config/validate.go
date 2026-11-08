package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Port ranges for settings.yml proxy fields.
const (
	minInboundPort = 1001
	maxInboundPort = 50000
	minTargetPort  = 1
	maxTargetPort  = 65535
)

const (
	maxDomainLen      = 253
	maxLabelLen       = 63
	minDomainLabels   = 2
	invalidDomainMeta = `/\$\{};'"|&<>()` + "`"
)

const (
	acmeEmailLocal = `[a-zA-Z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+`
	acmeEmailLabel = `[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?`
)

var (
	// Compose / Docker-safe service name: letter/digit start, then [A-Za-z0-9_.-].
	serviceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

	// Project name: letter/digit start, then [A-Za-z0-9_-] only (no dots/slashes/spaces).
	// Matches docker.appNameFromLabelRE so names are safe in -p flags and apps/<name> paths.
	projectNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

	// Simple email shape: local@domain with a dotted host (no display-name form).
	acmeEmailPattern = regexp.MustCompile(
		`^` + acmeEmailLocal + `@` + acmeEmailLabel + `(?:\.` + acmeEmailLabel + `)+$`,
	)
)

func validateDomainName(domain, projectName string) error {
	label := fmt.Sprintf("project %s: proxy.inbound.domain-name", projectName)

	if domain == "" {
		return nil
	}

	if len(domain) > maxDomainLen {
		return formatSettingsError(label + " is too long (max 253 characters)")
	}

	if strings.ContainsAny(domain, " \t\r\n") {
		return formatSettingsError(label + " must not contain whitespace")
	}

	// Block path traversal, nginx directive breakout, and shell metacharacters.
	if strings.ContainsAny(domain, invalidDomainMeta) {
		return formatSettingsError(label + " contains invalid characters")
	}

	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return formatSettingsError(label + " must not start or end with a dot")
	}

	labels := strings.Split(domain, ".")
	if len(labels) < minDomainLabels {
		return formatSettingsError(label + " must be a fully-qualified hostname (e.g. app.example.com)")
	}

	for _, part := range labels {
		err := validateDomainLabel(part, label)
		if err != nil {
			return err
		}
	}

	return nil
}

func validateDomainLabel(label, fieldLabel string) error {
	if label == "" {
		return formatSettingsError(fieldLabel + " has an empty DNS label")
	}

	if len(label) > maxLabelLen {
		return formatSettingsError(fieldLabel + " has a DNS label longer than 63 characters")
	}

	if label[0] == '-' || label[len(label)-1] == '-' {
		return formatSettingsError(fieldLabel + " DNS labels must not start or end with a hyphen")
	}

	for i := range len(label) {
		ch := label[i]
		if isASCIILetterOrDigit(ch) || ch == '-' {
			continue
		}

		return formatSettingsError(fieldLabel + " must use ASCII letters, digits, hyphens, and dots only")
	}

	return nil
}

func isASCIILetterOrDigit(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') ||
		(ch >= 'A' && ch <= 'Z') ||
		(ch >= '0' && ch <= '9')
}

func validateProjectName(name string) error {
	if name == "" {
		return formatSettingsError("projects[].name is required")
	}

	if !projectNamePattern.MatchString(name) {
		return formatSettingsError(
			fmt.Sprintf(
				"project name %q must match [A-Za-z0-9][A-Za-z0-9_-]* (no spaces, dots, or path separators)",
				name,
			),
		)
	}

	return nil
}

func validateServiceName(name, projectName string) error {
	label := fmt.Sprintf("project %s: proxy.target.service-name", projectName)

	if name == "" {
		return formatSettingsError(label + " is required")
	}

	if !serviceNamePattern.MatchString(name) {
		return formatSettingsError(
			label + " must match [A-Za-z0-9][A-Za-z0-9_.-]* (compose service key or container_name)",
		)
	}

	return nil
}

func validateAcmeEmail(email, projectName string) error {
	if email == "" {
		return nil
	}

	label := fmt.Sprintf("project %s: config.cert-acme-email", projectName)

	if strings.ContainsAny(email, " \t\r\n<>") {
		return formatSettingsError(label + " must be a bare email address (user@example.com)")
	}

	if !acmeEmailPattern.MatchString(email) {
		return formatSettingsError(label + " is not a valid email address")
	}

	return nil
}

func parsePortInRange(val any, label string, minPort, maxPort int) (string, error) {
	if val == nil {
		return "", formatSettingsError(label + " is required")
	}

	num, err := coercePortInt(val, label)
	if err != nil {
		return "", err
	}

	if num < minPort || num > maxPort {
		return "", formatSettingsError(
			fmt.Sprintf("%s must be between %d and %d (got %d)", label, minPort, maxPort, num),
		)
	}

	return strconv.Itoa(num), nil
}

func coercePortInt(val any, label string) (int, error) {
	switch typed := val.(type) {
	case int:
		return typed, nil
	case int64:
		return int(typed), nil
	case float64:
		asInt := int(typed)
		if float64(asInt) != typed {
			return 0, formatSettingsError(label + " must be a whole number")
		}

		return asInt, nil
	case string:
		str := strings.TrimSpace(typed)
		if str == "" {
			return 0, formatSettingsError(label + " must be a non-empty number")
		}

		parsed, err := strconv.Atoi(str)
		if err != nil {
			return 0, formatSettingsError(label + " must be a number")
		}

		return parsed, nil
	default:
		return 0, formatSettingsError(label + " must be a number or string")
	}
}
