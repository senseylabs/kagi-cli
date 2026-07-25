package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/senseylabs/kagi-cli/internal/client"
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

// newTestClient constructs a client with an explicit token and no active org.
// No network call happens at construction time, and org-scoped SDK calls fail
// fast with ErrNoOrganizationSelected rather than reaching out.
func newTestClient(t *testing.T) *client.KagiClient {
	t.Helper()
	return client.NewKagiClientWithToken("http://127.0.0.1:0", "test-token")
}

// --personal and an explicit --env naming a different environment are mutually
// exclusive. The check fires before any network call.
func TestResolveAppEnv_PersonalAndEnvMutuallyExclusive(t *testing.T) {
	vc := newTestClient(t)
	cmd := newSecretCmd(t, map[string]string{"personal": "true", "env": "prod", "app-id": "app-123"})

	_, err := resolveAppEnv(cmd, vc)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "use either --personal or --env, not both") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --personal --env personal names the same target, so it is NOT a mutual-
// exclusion conflict: resolution proceeds past the flag check.
func TestResolveAppEnv_PersonalWithEnvPersonalNotAConflict(t *testing.T) {
	vc := newTestClient(t)
	cmd := newSecretCmd(t, map[string]string{"personal": "true", "env": "personal", "app-id": "app-123"})

	_, err := resolveAppEnv(cmd, vc)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if strings.Contains(err.Error(), "not both") {
		t.Fatalf("--personal --env personal should not be a mutual-exclusion conflict: %v", err)
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
