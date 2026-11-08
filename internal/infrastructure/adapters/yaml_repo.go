package adapters

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/uservseatb/apinfra/internal/domain/certmanagement"
	"github.com/uservseatb/apinfra/internal/sys"
	"gopkg.in/yaml.v3"
)

const (
	downgradeCertModeFile = ".downgradeCertMode"
	retriesFileName       = ".retries"
)

// YamlStateRepository persists .retries and .downgradeCertMode next to the infra directory.
type YamlStateRepository struct {
	infraDirPath string
	files        sys.FileWriter
}

// NewYamlStateRepository builds a YamlStateRepository for infraDirPath.
func NewYamlStateRepository(infraDirPath string, files sys.FileWriter) *YamlStateRepository {
	return &YamlStateRepository{infraDirPath: infraDirPath, files: files}
}

type retryEntryYAML struct {
	Attempts     int    `yaml:"attempts"`
	Blocked      bool   `yaml:"blocked"`
	BlockedUntil string `yaml:"blocked_until,omitempty"`
}

type retriesFileYAML struct {
	Certs map[string]retryEntryYAML `yaml:"certs"`
}

type downgradeYAML struct {
	Mode  string `yaml:"mode"`
	Until string `yaml:"until,omitempty"`
}

// LoadRetries reads .retries into domain models.
func (r *YamlStateRepository) LoadRetries() (map[string]certmanagement.RetryEntry, error) {
	path := retriesFilePath(r.infraDirPath)

	data, err := os.ReadFile(path) // #nosec G304 -- host state next to infra
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]certmanagement.RetryEntry{}, nil
		}

		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var file retriesFileYAML

	err = yaml.Unmarshal(data, &file)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if file.Certs == nil {
		return map[string]certmanagement.RetryEntry{}, nil
	}

	out := make(map[string]certmanagement.RetryEntry, len(file.Certs))
	for domain, entry := range file.Certs {
		until, parseErr := parseOptionalTime(entry.BlockedUntil)
		if parseErr != nil {
			return nil, fmt.Errorf("parse %s domain %s blocked_until: %w", path, domain, parseErr)
		}

		out[domain] = certmanagement.RetryEntry{
			Attempts:     entry.Attempts,
			Blocked:      entry.Blocked,
			BlockedUntil: until,
		}
	}

	return out, nil
}

// SaveRetries writes .retries from domain models.
func (r *YamlStateRepository) SaveRetries(retries map[string]certmanagement.RetryEntry) error {
	path := retriesFilePath(r.infraDirPath)

	if retries == nil {
		retries = map[string]certmanagement.RetryEntry{}
	}

	file := retriesFileYAML{Certs: make(map[string]retryEntryYAML, len(retries))}
	for domain, entry := range retries {
		item := retryEntryYAML{
			Attempts:     entry.Attempts,
			Blocked:      entry.Blocked,
			BlockedUntil: "",
		}
		if !entry.BlockedUntil.IsZero() {
			item.BlockedUntil = entry.BlockedUntil.UTC().Format(certmanagement.CertTimeLayout)
		}

		file.Certs[domain] = item
	}

	data, err := yaml.Marshal(file)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}

	_, err = r.files.WriteFile(path, string(data), sys.FileModePrivate)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

// LoadDowngrades reads .downgradeCertMode. When now is non-zero, expired entries
// are dropped and the file is rewritten.
func (r *YamlStateRepository) LoadDowngrades(now time.Time) (map[string]certmanagement.DowngradeEntry, error) {
	path := downgradeCertModePath(r.infraDirPath)

	data, err := os.ReadFile(path) // #nosec G304 -- host state next to infra
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]certmanagement.DowngradeEntry{}, nil
		}

		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	raw := map[string]yaml.Node{}

	err = yaml.Unmarshal(data, &raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	out, expired, err := collectDowngradeEntries(path, raw, now)
	if err != nil {
		return nil, err
	}

	if expired {
		saveErr := r.SaveDowngrades(out)
		if saveErr != nil {
			return out, fmt.Errorf("prune expired %s: %w", path, saveErr)
		}
	}

	return out, nil
}

// SaveDowngrades writes .downgradeCertMode from domain models.
func (r *YamlStateRepository) SaveDowngrades(modes map[string]certmanagement.DowngradeEntry) error {
	path := downgradeCertModePath(r.infraDirPath)

	if modes == nil {
		modes = map[string]certmanagement.DowngradeEntry{}
	}

	encoded := make(map[string]downgradeYAML, len(modes))
	for domain, entry := range modes {
		item := downgradeYAML{Mode: entry.Mode, Until: ""}
		if !entry.Until.IsZero() {
			item.Until = entry.Until.UTC().Format(certmanagement.CertTimeLayout)
		}

		encoded[domain] = item
	}

	data, err := yaml.Marshal(encoded)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}

	_, err = r.files.WriteFile(path, string(data), sys.FileModePrivate)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

func collectDowngradeEntries(
	path string,
	raw map[string]yaml.Node,
	now time.Time,
) (map[string]certmanagement.DowngradeEntry, bool, error) {
	out := make(map[string]certmanagement.DowngradeEntry, len(raw))
	expired := false

	for domain, node := range raw {
		entry, decodeErr := decodeDowngradeNode(node)
		if decodeErr != nil {
			return nil, false, fmt.Errorf("parse %s domain %s: %w", path, domain, decodeErr)
		}

		if entry.Mode == "" {
			continue
		}

		if !now.IsZero() && certmanagement.DowngradeExpired(entry, now) {
			expired = true

			continue
		}

		out[domain] = entry
	}

	return out, expired, nil
}

var errUnexpectedDowngradeYAML = errors.New("unexpected .downgradeCertMode yaml kind")

func decodeDowngradeNode(node yaml.Node) (certmanagement.DowngradeEntry, error) {
	switch node.Kind { //nolint:exhaustive // only scalar/mapping are valid entry shapes
	case yaml.ScalarNode:
		return certmanagement.DowngradeEntry{Mode: node.Value, Until: time.Time{}}, nil
	case yaml.MappingNode:
		var aux downgradeYAML

		err := node.Decode(&aux)
		if err != nil {
			return certmanagement.DowngradeEntry{}, fmt.Errorf("decode downgrade entry: %w", err)
		}

		until, err := parseOptionalTime(aux.Until)
		if err != nil {
			return certmanagement.DowngradeEntry{}, err
		}

		return certmanagement.DowngradeEntry{Mode: aux.Mode, Until: until}, nil
	default:
		return certmanagement.DowngradeEntry{}, fmt.Errorf("%w: %v", errUnexpectedDowngradeYAML, node.Kind)
	}
}

func parseOptionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}

	parsed, err := time.Parse(certmanagement.CertTimeLayout, value)
	if err == nil {
		return parsed.UTC(), nil
	}

	parsed, err = time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse until %q: %w", value, err)
	}

	return parsed.UTC(), nil
}

func downgradeCertModePath(infraDirPath string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(infraDirPath)), downgradeCertModeFile)
}

func retriesFilePath(infraDirPath string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(infraDirPath)), retriesFileName)
}
