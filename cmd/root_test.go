package cmd

import (
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/senseylabs/kagi-cli/internal/auth"
)

// resetDevResolution puts the package-level resolution globals and the shared
// `--dev` persistent flag back to a clean, unset state, and restores them after
// the test so no state leaks between initConfig() runs (these are process-wide
// globals bound to rootCmd). initConfig only fills cfgAPIURL/cfgIssuer when they
// are empty, so they must be cleared before each call.
func resetDevResolution(t *testing.T) {
	t.Helper()
	origAPIURL, origIssuer, origDev := cfgAPIURL, cfgIssuer, cfgDevMode
	devFlag := rootCmd.PersistentFlags().Lookup("dev")
	origChanged := devFlag.Changed
	t.Cleanup(func() {
		cfgAPIURL, cfgIssuer, cfgDevMode = origAPIURL, origIssuer, origDev
		_ = devFlag.Value.Set("false")
		devFlag.Changed = origChanged
	})
	cfgAPIURL, cfgIssuer, cfgDevMode = "", "", false
	_ = devFlag.Value.Set("false")
	devFlag.Changed = false
}

// TestInitConfig_ExplicitDevFalseEscapesStoredDevSession covers FIX #4: after a
// `kagi login --dev` persists localhost URLs with DevMode=true, running an
// explicit `kagi --dev=false ...` must resolve to the PRODUCTION URLs rather than
// inheriting the stored localhost URLs (which are not "stale" per the host
// filters and would otherwise win over the prod default, trapping the user in
// dev). An implicit run (no --dev) still stays sticky-dev, which is intended.
func TestInitConfig_ExplicitDevFalseEscapesStoredDevSession(t *testing.T) {
	keyring.MockInit()

	// Hermetic home so config.Load() and the token store never touch real files.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(t.TempDir())

	// No env overrides in play — they would bypass the stored-URL fallback.
	t.Setenv("KAGI_API_URL", "")
	t.Setenv("KAGI_KEYCLOAK_ISSUER", "")
	t.Setenv("KAGI_DISCOVERY_TIMEOUT", "")

	// Seed a stored dev session exactly as `login --dev` would.
	store := auth.NewTokenStore()
	if err := store.Save(auth.Credentials{
		AccessToken: "seed-token",
		IssuerURL:   devIssuer,
		APIURL:      devAPIURL,
		DevMode:     true,
	}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}

	t.Run("explicit --dev=false resolves to prod", func(t *testing.T) {
		resetDevResolution(t)
		if err := rootCmd.PersistentFlags().Set("dev", "false"); err != nil {
			t.Fatalf("set --dev=false: %v", err)
		}

		initConfig()

		if cfgAPIURL != prodAPIURL {
			t.Errorf("cfgAPIURL = %q, want prod %q", cfgAPIURL, prodAPIURL)
		}
		if cfgIssuer != prodIssuer {
			t.Errorf("cfgIssuer = %q, want prod %q", cfgIssuer, prodIssuer)
		}
	})

	t.Run("implicit (no --dev) stays sticky-dev", func(t *testing.T) {
		resetDevResolution(t) // leaves --dev unchanged (Changed=false)

		initConfig()

		if cfgAPIURL != devAPIURL {
			t.Errorf("cfgAPIURL = %q, want dev %q", cfgAPIURL, devAPIURL)
		}
		if cfgIssuer != devIssuer {
			t.Errorf("cfgIssuer = %q, want dev %q", cfgIssuer, devIssuer)
		}
	})
}
