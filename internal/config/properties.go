package config

import (
	"fmt"
	"os"

	"github.com/uservseatb/apinfra/internal/sys"
)

// ReadProperties reads key=value lines from a file (skips empty and # comments).
func ReadProperties(path string) (map[string]string, error) {
	file, err := os.Open(path) // #nosec G304 -- path comes from host config locations controlled by apinfra
	if err != nil {
		return nil, fmt.Errorf("open properties %s: %w", path, err)
	}

	defer func() { _ = file.Close() }()

	out, err := sys.ParseProperties(file)
	if err != nil {
		return nil, fmt.Errorf("parse properties %s: %w", path, err)
	}

	return out, nil
}

// ParsePropertiesString parses key=value content from a string.
func ParsePropertiesString(content string) (map[string]string, error) {
	out, err := sys.ParsePropertiesString(content)
	if err != nil {
		return nil, fmt.Errorf("parse properties string: %w", err)
	}

	return out, nil
}
