package cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/senseylabs/kagi-cli/internal/auth"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/spf13/cobra"
)

const (
	prodAPIURL = "https://api.kagi.pw"
	prodIssuer = "https://auth.kagi.pw/realms/kagi"
	devAPIURL  = "http://localhost:8081"
	devIssuer  = "http://localhost:8085/realms/kagi"

	// defaultDiscoveryTimeout is the overall budget for OIDC discovery retries,
	// overridable via KAGI_DISCOVERY_TIMEOUT.
	defaultDiscoveryTimeout = 90 * time.Second
)

var (
	cfgAPIURL  string
	cfgIssuer  string
	cfgDevMode bool
	appVersion string

	// cfgDiscoveryTimeout is the overall budget for OIDC discovery (see login).
	// cfgDiscoveryTimeoutErr holds a parse error for KAGI_DISCOVERY_TIMEOUT so
	// the consuming command can surface it rather than silently ignoring it —
	// initConfig runs via cobra.OnInitialize and cannot return an error itself.
	cfgDiscoveryTimeout    = defaultDiscoveryTimeout
	cfgDiscoveryTimeoutErr error
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
		} else if cfg.APIURL != "" && !isStaleAPIURL(cfg.APIURL) {
			cfgAPIURL = cfg.APIURL
		} else if storedCreds.APIURL != "" && !isStaleAPIURL(storedCreds.APIURL) {
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

	// KAGI_DISCOVERY_TIMEOUT overrides the discovery retry budget (a Go duration,
	// e.g. "45s"). An invalid value must not be silently ignored: record the
	// error so the login command surfaces it and keep the safe default meanwhile.
	cfgDiscoveryTimeout = defaultDiscoveryTimeout
	cfgDiscoveryTimeoutErr = nil
	if v := os.Getenv("KAGI_DISCOVERY_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			cfgDiscoveryTimeoutErr = fmt.Errorf("invalid KAGI_DISCOVERY_TIMEOUT %q: %w", v, err)
		case d <= 0:
			cfgDiscoveryTimeoutErr = fmt.Errorf("invalid KAGI_DISCOVERY_TIMEOUT %q: must be a positive duration", v)
		default:
			cfgDiscoveryTimeout = d
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

// isStaleAPIURL rejects the pre-migration `kagi-api.sensey.io` API host so users
// upgrading from older CLI versions (whose stored credentials or config cached
// that host) don't have it win over the new `api.kagi.pw` default. The
// KAGI_API_URL env var stays authoritative and bypasses this filter.
func isStaleAPIURL(url string) bool {
	return strings.Contains(url, "kagi-api.sensey.io")
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
