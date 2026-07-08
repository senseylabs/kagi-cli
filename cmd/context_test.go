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
