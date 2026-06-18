package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// Config holds the configuration loaded from kagi.yaml files.
type Config struct {
	APIURL      string `mapstructure:"api-url"`
	Issuer      string `mapstructure:"issuer"`
	Project     string `mapstructure:"project"`
	App         string `mapstructure:"app"`
	Environment string `mapstructure:"environment"`
	// Organization is the active organization SLUG, kept for human-readable
	// display. OrganizationID is the org UUID sent as the X-Organization-ID
	// header on JWT requests.
	Organization   string `mapstructure:"organization"`
	OrganizationID string `mapstructure:"organization-id"`
}

// Load reads configuration from kagi.yaml in the current directory,
// then falls back to ~/.kagi/config.yaml. CWD values take priority.
func Load() Config {
	var cfg Config

	// Second priority: ~/.kagi/config.yaml (load first, will be overridden by CWD)
	home, err := os.UserHomeDir()
	if err == nil {
		hv := viper.New()
		hv.SetConfigName("config")
		hv.SetConfigType("yaml")
		hv.AddConfigPath(filepath.Join(home, ".kagi"))
		if err := hv.ReadInConfig(); err == nil {
			_ = hv.Unmarshal(&cfg)
		}
	}

	// First priority: kagi.yaml in current working directory (overrides home config)
	cwd, err := os.Getwd()
	if err == nil {
		cv := viper.New()
		cv.SetConfigName("kagi")
		cv.SetConfigType("yaml")
		cv.AddConfigPath(cwd)
		if err := cv.ReadInConfig(); err == nil {
			var cwdCfg Config
			if err := cv.Unmarshal(&cwdCfg); err == nil {
				// Merge: CWD values override home values when non-empty
				if cwdCfg.APIURL != "" {
					cfg.APIURL = cwdCfg.APIURL
				}
				if cwdCfg.Issuer != "" {
					cfg.Issuer = cwdCfg.Issuer
				}
				if cwdCfg.Project != "" {
					cfg.Project = cwdCfg.Project
				}
				if cwdCfg.App != "" {
					cfg.App = cwdCfg.App
				}
				if cwdCfg.Environment != "" {
					cfg.Environment = cwdCfg.Environment
				}
				if cwdCfg.Organization != "" {
					cfg.Organization = cwdCfg.Organization
				}
				if cwdCfg.OrganizationID != "" {
					cfg.OrganizationID = cwdCfg.OrganizationID
				}
			}
		}
	}

	return cfg
}

// homeConfigPath returns the path to ~/.kagi/config.yaml.
func homeConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(home, ".kagi", "config.yaml"), nil
}

// SaveOrganization persists the active organization (slug + UUID) to
// ~/.kagi/config.yaml. It reads the existing home config first and rewrites it
// so other keys (api-url, project, etc.) are preserved rather than clobbered.
func SaveOrganization(slug, id string) error {
	path, err := homeConfigPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	v := viper.New()
	v.SetConfigFile(path)
	// Missing file is fine on first save; any other read error is surfaced.
	if err := v.ReadInConfig(); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return fmt.Errorf("failed to read existing config %s: %w", path, err)
		}
	}

	v.Set("organization", slug)
	v.Set("organization-id", id)

	if err := v.WriteConfigAs(path); err != nil {
		return fmt.Errorf("failed to write config %s: %w", path, err)
	}
	return nil
}
