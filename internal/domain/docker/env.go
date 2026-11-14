package docker

import (
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/uservseatb/apinfra/internal/sys"
	"gopkg.in/yaml.v3"
)

var (
	errEnvKeyCollision     = errors.New("env key collision")
	errConfigDirValidation = errors.New("config dir validation failed")
)

// ValidateConfigDirEnv checks that each project's compose ${VAR} are covered by
// the normalized sidecar .env and/or host application .env keys with the project prefix.
func (s *Service) ValidateConfigDirEnv(projects []Project, hostEnv map[string]string) error {
	host := nonemptyStringMap(hostEnv)

	var msgs []string

	for _, project := range projects {
		if msg := validateProjectConfigDirEnv(project, host); msg != "" {
			msgs = append(msgs, msg)
		}
	}

	if len(msgs) == 0 {
		return nil
	}

	return fmt.Errorf("%w:\n  %s", errConfigDirValidation, strings.Join(msgs, "\n  "))
}

func validateProjectConfigDirEnv(project Project, host map[string]string) string {
	raw := strings.TrimSpace(project.ComposeYAML)
	if raw == "" {
		return ""
	}

	sidecar, err := projectSidecarEnv(project)
	if err != nil {
		return fmt.Sprintf("project=%s %v (in sidecar .env)", project.Name, err)
	}

	remaining, err := missingEnvVars(project.ComposeYAML, sidecar)
	if err != nil {
		return fmt.Sprintf("project=%s invalid compose YAML: %v", project.Name, err)
	}

	if len(remaining) == 0 {
		return ""
	}

	missing := missingAfterHost(remaining, project.envPrefix(), host)
	if len(missing) == 0 {
		return ""
	}

	return fmt.Sprintf(
		"project=%s missing env vars: %s (need in sidecar .env after normalizing keys, or as %sVAR in host .env)",
		project.Name, strings.Join(missing, ", "), project.envPrefix(),
	)
}

func projectSidecarEnv(project Project) (map[string]string, error) {
	if strings.TrimSpace(project.SidecarEnvRaw) == "" {
		return map[string]string{}, nil
	}

	props, err := sys.ParsePropertiesString(project.SidecarEnvRaw)
	if err != nil {
		return nil, fmt.Errorf("parse sidecar env: %w", err)
	}

	return normalizeSidecarEnv(props)
}

func normalizeSidecarEnv(raw map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(raw))
	from := make(map[string]string, len(raw))

	for key, val := range raw {
		normKey := normalizeEnvKey(key)
		if prev, exists := from[normKey]; exists && prev != key {
			return nil, fmt.Errorf("%w: %q and %q both normalize to %q", errEnvKeyCollision, prev, key, normKey)
		}

		if existing, exists := out[normKey]; exists && existing != val {
			return nil, fmt.Errorf("%w: %q normalizes to %q with conflicting values", errEnvKeyCollision, key, normKey)
		}

		from[normKey] = key
		out[normKey] = val
	}

	return out, nil
}

func normalizeEnvKey(key string) string {
	return strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// projectEnvFileLines builds unprefixed KEY=value lines for apps/<name>/.env.
// Host keys use TO_UPPER(project)__ prefix and are stripped; host wins on collision.
func projectEnvFileLines(project Project, sidecar, host map[string]string) []string {
	merged := map[string]string{}
	maps.Copy(merged, sidecar)
	maps.Copy(merged, hostProjectEnv(project.envPrefix(), host))

	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+merged[key])
	}

	return lines
}

func hostProjectEnv(prefix string, host map[string]string) map[string]string {
	out := map[string]string{}
	if prefix == "" {
		return out
	}

	for key, val := range host {
		if !strings.HasPrefix(key, prefix) {
			continue
		}

		stripped := key[len(prefix):]
		if stripped == "" {
			continue
		}

		out[stripped] = val
	}

	return out
}

// missingEnvVars reports bare ${VAR} in compose that are absent from sidecarEnv
// (keys must already be in normalized compose form).
func missingEnvVars(composeContent string, sidecarEnv map[string]string) ([]string, error) {
	vars, err := extractBareComposeVars(composeContent)
	if err != nil {
		return nil, err
	}

	var missing []string

	for _, varname := range vars {
		if _, exists := sidecarEnv[varname]; !exists {
			missing = append(missing, varname)
		}
	}

	return missing, nil
}

// extractBareComposeVars returns unique bare ${VAR} names from YAML string scalars
// (no :- or ? modifiers).
func extractBareComposeVars(content string) ([]string, error) {
	var root any

	err := yaml.Unmarshal([]byte(content), &root)
	if err != nil {
		return nil, fmt.Errorf("parse compose YAML: %w", err)
	}

	seen := map[string]struct{}{}

	var out []string

	walkYAMLStrings(root, func(str string) string {
		collectBareComposeVars(str, seen, &out)

		return str
	})
	sort.Strings(out)

	return out, nil
}

func missingAfterHost(remaining []string, prefix string, host map[string]string) []string {
	hostStripped := hostProjectEnv(prefix, host)

	var missing []string

	for _, varname := range remaining {
		if _, exists := hostStripped[varname]; !exists {
			missing = append(missing, varname)
		}
	}

	return missing
}

// walkYAMLStrings visits every string scalar and replaces it with replaceFn's result.
func walkYAMLStrings(node any, replaceFn func(string) string) any {
	switch typed := node.(type) {
	case map[string]any:
		for key, child := range typed {
			typed[key] = walkYAMLStrings(child, replaceFn)
		}

		return typed
	case map[any]any:
		for key, child := range typed {
			typed[key] = walkYAMLStrings(child, replaceFn)
		}

		return typed
	case []any:
		for idx, child := range typed {
			typed[idx] = walkYAMLStrings(child, replaceFn)
		}

		return typed
	case string:
		return replaceFn(typed)
	default:
		return node
	}
}

func collectBareComposeVars(content string, seen map[string]struct{}, out *[]string) {
	start := 0

	for {
		openIdx := strings.Index(content[start:], "${")
		if openIdx < 0 {
			return
		}

		openIdx += start

		closeIdx := strings.Index(content[openIdx:], "}")
		if closeIdx < 0 {
			return
		}

		closeIdx += openIdx
		inner := content[openIdx+2 : closeIdx]
		start = closeIdx + 1

		if inner == "" || strings.ContainsAny(inner, ":?") {
			continue
		}

		if _, exists := seen[inner]; exists {
			continue
		}

		seen[inner] = struct{}{}

		*out = append(*out, inner)
	}
}
