package config

import (
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
			}
		}
	}

	return cfg
}
