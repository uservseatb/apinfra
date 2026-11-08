package assets

import (
	"embed"
	"fmt"
	"path/filepath"
)

// Static files embedded into the binary. Paths are relative to this package.
//
//go:embed templates configs ssl-dhparams.pem
var embeddedFS embed.FS

// LoadText returns resource content as a string strictly from memory.
func LoadText(relPath string) (string, error) {
	data, err := embeddedFS.ReadFile(filepath.ToSlash(relPath))
	if err != nil {
		return "", fmt.Errorf("read embedded %s: %w", relPath, err)
	}

	return string(data), nil
}
