package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/senseylabs/kagi-cli/internal/auth"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/spf13/cobra"
)

const (
	prodAPIURL  = "https://api.kagi.pw"
	prodIssuer  = "https://auth.sensey.io/realms/kagi"
	devAPIURL   = "http://localhost:8081"
	devIssuer   = "http://localhost:8085/realms/kagi"
)

var (
	cfgAPIURL  string
	cfgIssuer  string
	cfgDevMode bool
	appVersion string
)

func SetVersion(v string) {
	appVersion = v
	rootCmd.Version = v
}

var rootCmd = &cobra.Command{
	Use:   "kagi",
	Short: "Kagi CLI — secrets management for Sensey",
	Long:  "A CLI tool for managing secrets in Kagi. Supports Keycloak Device Authorization Grant for interactive login and Personal Access Tokens for CI/CD.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().BoolVar(&cfgDevMode, "dev", false, "Use local development URLs (localhost)")

	rootCmd.Version = appVersion
}

func initConfig() {
	cfg := config.Load()

	// Load stored credentials for fallback values
	store := auth.NewTokenStore()
	storedCreds, err := store.Load()
	if err != nil {
		// "no credentials found" is expected on first run — only warn on unexpected errors
		if !strings.Contains(err.Error(), "no credentials found") {
			fmt.Fprintf(os.Stderr, "Warning: could not load stored credentials: %v\n", err)
		}
	}

	// If --dev not explicitly set, check stored credentials
	if !cfgDevMode && storedCreds.DevMode {
		cfgDevMode = true
	}

	// Resolve API URL: --dev flag → env var → config file → stored creds → production default
	if cfgDevMode {
		cfgAPIURL = devAPIURL
		cfgIssuer = devIssuer
	}

	if cfgAPIURL == "" {
		if v := os.Getenv("KAGI_API_URL"); v != "" {
			cfgAPIURL = v
		} else if cfg.APIURL != "" {
			cfgAPIURL = cfg.APIURL
		} else if storedCreds.APIURL != "" {
			cfgAPIURL = storedCreds.APIURL
		} else {
			cfgAPIURL = prodAPIURL
		}
	}

	if cfgIssuer == "" {
		if v := os.Getenv("KAGI_KEYCLOAK_ISSUER"); v != "" {
			cfgIssuer = v
		} else if cfg.Issuer != "" && !isStaleIssuer(cfg.Issuer) {
			cfgIssuer = cfg.Issuer
		} else if storedCreds.IssuerURL != "" && !isStaleIssuer(storedCreds.IssuerURL) {
			cfgIssuer = storedCreds.IssuerURL
		} else {
			cfgIssuer = prodIssuer
		}
	}

}

// isStaleIssuer rejects issuer URLs from the pre-migration `sensey` realm so
// users upgrading from older CLI versions don't have their cached (stale)
// issuer win over the new `kagi` realm default. KAGI_KEYCLOAK_ISSUER env stays
// authoritative and bypasses this filter.
func isStaleIssuer(url string) bool {
	return strings.Contains(url, "/realms/sensey")
}

func requireAuth() error {
	if os.Getenv("KAGI_TOKEN") != "" {
		return nil
	}
	store := auth.NewTokenStore()
	if _, err := store.Load(); err != nil {
		return fmt.Errorf("you are not logged in. Run 'kagi login' to authenticate")
	}
	return nil
}

