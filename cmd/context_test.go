package cmd

import (
	"strings"
	"testing"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/spf13/cobra"
)

// newSecretCmd builds a throwaway command carrying the shared secret flags,
// with the given flag values applied.
func newSecretCmd(t *testing.T, flags map[string]string) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "test"}
	addSecretFlags(c)
	for k, v := range flags {
		if err := c.Flags().Set(k, v); err != nil {
			t.Fatalf("set flag %q=%q: %v", k, v, err)
		}
	}
	return c
}

// newPATClient constructs a client authenticated with a Personal Access Token
// (via KAGI_TOKEN). No network call happens at construction time.
func newPATClient(t *testing.T) *client.KagiClient {
	t.Helper()
	t.Setenv("KAGI_TOKEN", "test-pat")
	vc, err := client.NewKagiClient("http://127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("NewKagiClient: %v", err)
	}
	if !vc.IsPAT() {
		t.Fatal("expected a PAT client")
	}
	return vc
}

// --personal and an explicit --env naming a different environment are mutually
// exclusive. The check fires before any network call, so a PAT client is fine.
func TestResolveAppEnv_PersonalAndEnvMutuallyExclusive(t *testing.T) {
	vc := newPATClient(t)
	cmd := newSecretCmd(t, map[string]string{"personal": "true", "env": "prod", "app-id": "app-123"})

	_, err := resolveAppEnv(cmd, vc)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "use either --personal or --env, not both") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --personal --env personal names the same target, so it is NOT a conflict: it
// must fall through to the PAT guard (not the mutual-exclusion error).
func TestResolveAppEnv_PersonalWithEnvPersonalNotAConflict(t *testing.T) {
	vc := newPATClient(t)
	cmd := newSecretCmd(t, map[string]string{"personal": "true", "env": "personal", "app-id": "app-123"})

	_, err := resolveAppEnv(cmd, vc)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if strings.Contains(err.Error(), "not both") {
		t.Fatalf("--personal --env personal should not be a mutual-exclusion conflict: %v", err)
	}
	if !strings.Contains(err.Error(), "not available to machine/CI (PAT) tokens") {
		t.Fatalf("expected the PAT guard error, got: %v", err)
	}
}

// A PAT token requesting the personal env via --personal fails fast with the
// user-scoped guard, before any network call.
func TestResolveAppEnv_PATGuard_PersonalFlag(t *testing.T) {
	vc := newPATClient(t)
	cmd := newSecretCmd(t, map[string]string{"personal": "true", "app-id": "app-123"})

	_, err := resolveAppEnv(cmd, vc)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "personal secrets are user-scoped") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// The PAT guard also catches the personal env requested via --env personal
// (case-insensitively), not only the --personal sugar.
func TestResolveAppEnv_PATGuard_EnvPersonal(t *testing.T) {
	vc := newPATClient(t)
	cmd := newSecretCmd(t, map[string]string{"env": "Personal", "app-id": "app-123"})

	_, err := resolveAppEnv(cmd, vc)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "not available to machine/CI (PAT) tokens") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// envs is a small helper to build the available-environment list from slugs.
func envs(slugs ...string) []client.Environment {
	out := make([]client.Environment, len(slugs))
	for i, s := range slugs {
		out[i] = client.Environment{Slug: s}
	}
	return out
}

// TestChooseEnvSlug exercises the pure environment-selection decision, including
// the run/pull personal fallback and — most importantly — that the fallback is
// NOT applied when it is disallowed (the secrets-subcommand safety case) or when
// the personal env was named explicitly via --env rather than --personal.
func TestChooseEnvSlug(t *testing.T) {
	const label = `"/clients/fepatex/portal" (app-123)`

	tests := []struct {
		name          string
		requested     string
		personalFlag  bool
		allowFallback bool
		configEnv     string
		available     []client.Environment
		wantSlug      string
		wantWarning   bool   // expect a non-empty warning
		wantErr       bool   // expect a non-nil error
		errContains   string // substring the error must contain (when wantErr)
	}{
		{
			name:      "exact match, no warning",
			requested: "production",
			available: envs("local", "development", "production"),
			wantSlug:  "production",
		},
		{
			name:      "case-insensitive match normalizes to canonical",
			requested: "PRODUCTION",
			available: envs("local", "development", "production"),
			wantSlug:  "production",
		},
		{
			name:          "personal missing, fallback allowed, configEnv present -> falls back",
			requested:     personalEnvSlug,
			personalFlag:  true,
			allowFallback: true,
			configEnv:     "local",
			available:     envs("local", "development", "production"),
			wantSlug:      "local",
			wantWarning:   true,
		},
		{
			name:          "personal missing, fallback allowed, configEnv empty -> error",
			requested:     personalEnvSlug,
			personalFlag:  true,
			allowFallback: true,
			configEnv:     "",
			available:     envs("local", "development", "production"),
			wantErr:       true,
			errContains:   `environment "personal" not found in app`,
		},
		{
			name:          "personal missing, fallback allowed, configEnv not available -> error naming personal",
			requested:     personalEnvSlug,
			personalFlag:  true,
			allowFallback: true,
			configEnv:     "staging", // not in available
			available:     envs("local", "development", "production"),
			wantErr:       true,
			errContains:   `environment "personal" not found in app`,
		},
		{
			name:          "personal missing, fallback DISALLOWED (secrets) -> strict error",
			requested:     personalEnvSlug,
			personalFlag:  true,
			allowFallback: false, // the secrets safety case
			configEnv:     "local",
			available:     envs("local", "development", "production"),
			wantErr:       true,
			errContains:   `environment "personal" not found in app`,
		},
		{
			name:          "--env personal (personalFlag false), fallback allowed -> strict error",
			requested:     personalEnvSlug,
			personalFlag:  false, // named via --env, not --personal
			allowFallback: true,
			configEnv:     "local",
			available:     envs("local", "development", "production"),
			wantErr:       true,
			errContains:   `environment "personal" not found in app`,
		},
		{
			name:          "no environments -> has-no-environments error",
			requested:     personalEnvSlug,
			personalFlag:  true,
			allowFallback: true,
			configEnv:     "local",
			available:     envs(),
			wantErr:       true,
			errContains:   "has no environments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slug, warning, err := chooseEnvSlug(tt.requested, tt.personalFlag, tt.allowFallback, tt.configEnv, tt.available, label)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got nil (slug=%q, warning=%q)", slug, warning)
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if slug != tt.wantSlug {
				t.Fatalf("slug = %q, want %q", slug, tt.wantSlug)
			}
			if tt.wantWarning && warning == "" {
				t.Fatal("expected a non-empty warning, got empty")
			}
			if !tt.wantWarning && warning != "" {
				t.Fatalf("expected no warning, got %q", warning)
			}
		})
	}
}
