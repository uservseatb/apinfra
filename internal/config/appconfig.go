package config

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/uservseatb/apinfra/internal/assets"
	"gopkg.in/yaml.v3"
)

const defaultSettingsDocsURL = "https://github.com/uservseatb/apinfra#config-repository"

var errInvalidSettingsFormat = errors.New("invalid settings.yml format")

//nolint:gochecknoglobals // sync.Once lazy-load of embedded application.yml
var (
	appCfgOnce sync.Once
	appCfg     applicationConfig
	errAppCfg  error
)

type applicationConfig struct {
	SettingsDocsURL string `yaml:"settings_docs_url"`
}

// formatSettingsError wraps a settings/config-dir validation detail with the docs URL.
func formatSettingsError(detail string) error {
	return fmt.Errorf("%w: %s; see %s", errInvalidSettingsFormat, detail, settingsDocsURL())
}

func settingsDocsURL() string {
	cfg := loadAppConfig()
	if cfg.SettingsDocsURL == "" {
		return defaultSettingsDocsURL
	}

	return cfg.SettingsDocsURL
}

func loadAppConfig() applicationConfig {
	appCfgOnce.Do(func() {
		appCfg, errAppCfg = loadApplicationConfig()
	})

	if errAppCfg != nil {
		return applicationConfig{SettingsDocsURL: defaultSettingsDocsURL}
	}

	return appCfg
}

func loadApplicationConfig() (applicationConfig, error) {
	data, err := assets.LoadText("configs/application.yml")
	if err != nil {
		return applicationConfig{}, fmt.Errorf("load configs/application.yml: %w", err)
	}

	var cfg applicationConfig

	err = yaml.Unmarshal([]byte(data), &cfg)
	if err != nil {
		return applicationConfig{}, fmt.Errorf("parse configs/application.yml: %w", err)
	}

	cfg.SettingsDocsURL = strings.TrimSpace(cfg.SettingsDocsURL)
	if cfg.SettingsDocsURL == "" {
		cfg.SettingsDocsURL = defaultSettingsDocsURL
	}

	return cfg, nil
}
