package sys

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// ParseProperties reads key=value lines from r.
// Empty lines and whole-line # comments are skipped. Inline # in values is kept
// (so tokens like abc#def are not truncated).
func ParseProperties(reader io.Reader) (map[string]string, error) {
	out := make(map[string]string)
	scanner := bufio.NewScanner(reader)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, val, found := strings.Cut(line, "=")
		if !found {
			continue
		}

		key = strings.TrimSpace(key)
		if key == "" {
			// Bare "=" / " = value" would yield an empty key and break compose .env lines.
			continue
		}

		val = strings.TrimSpace(val)
		val = strings.Trim(val, "\"")
		out[key] = val
	}

	err := scanner.Err()
	if err != nil {
		return nil, fmt.Errorf("scan properties: %w", err)
	}

	return out, nil
}

// ParsePropertiesString parses key=value content from a string.
func ParsePropertiesString(content string) (map[string]string, error) {
	return ParseProperties(strings.NewReader(content))
}
