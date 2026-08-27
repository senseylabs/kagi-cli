package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/senseylabs/kagi-cli/internal/auth"
	"github.com/senseylabs/kagi-cli/internal/client"
)

// hermeticHome isolates the credential store and config lookup from the real
// machine so these tests never read or write the developer's session.
func hermeticHome(t *testing.T) {
	t.Helper()
	keyring.MockInit()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(t.TempDir())
}

// --- requireAuth --------------------------------------------------------------

// KAGI_TOKEN is itself the credential: with it set, requireAuth passes even
// though no `kagi login` session exists. This is the whole point of the
// non-interactive path — a CI runner never logs in.
func TestRequireAuth_StaticTokenNeedsNoSession(t *testing.T) {
	hermeticHome(t)
	t.Setenv("KAGI_TOKEN", "vv_ci_token")

	if err := requireAuth(); err != nil {
		t.Fatalf("requireAuth with KAGI_TOKEN set: %v", err)
	}
}

// With KAGI_TOKEN unset and no stored session, requireAuth still reports the
// unchanged "not logged in" error — the device-grant path is untouched.
func TestRequireAuth_NoStaticTokenNoSession(t *testing.T) {
	hermeticHome(t)
	t.Setenv("KAGI_TOKEN", "")

	err := requireAuth()
	if err == nil {
		t.Fatal("expected a not-logged-in error, got nil")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("unexpected error: %v", err)
	}
}

// An empty or whitespace-only KAGI_TOKEN must NOT be mistaken for a credential:
// an unpopulated CI secret has to surface as "not logged in", not as an opaque
// 401 from an empty bearer token.
func TestRequireAuth_EmptyStaticTokenIsNotACredential(t *testing.T) {
	for _, value := range []string{"", "  ", "\n"} {
		hermeticHome(t)
		t.Setenv("KAGI_TOKEN", value)

		err := requireAuth()
		if err == nil {
			t.Fatalf("KAGI_TOKEN=%q should not authenticate", value)
		}
		if !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("KAGI_TOKEN=%q: unexpected error: %v", value, err)
		}
	}
}

// A stored session with KAGI_TOKEN unset passes requireAuth exactly as before.
func TestRequireAuth_StoredSessionUnchanged(t *testing.T) {
	hermeticHome(t)
	t.Setenv("KAGI_TOKEN", "")

	store := auth.NewTokenStore()
	if err := store.Save(auth.Credentials{
		AccessToken: "stored-session-jwt",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}

	if err := requireAuth(); err != nil {
		t.Fatalf("requireAuth with a stored session: %v", err)
	}
}

// --- org selection under PAT auth ---------------------------------------------

// Organization selection is meaningless under KAGI_TOKEN: the token is bound to
// one organization server-side and the CLI never sends X-Organization-ID for it.
// All three org subcommands say so rather than pretending the local selection
// has any effect.
func TestRejectPATForOrgSelection(t *testing.T) {
	t.Setenv("KAGI_TOKEN", "vv_ci_token")
	err := rejectPATForOrgSelection()
	if err == nil {
		t.Fatal("expected org selection to be rejected under KAGI_TOKEN")
	}
	if !strings.Contains(err.Error(), "already bound to a single organization") {
		t.Errorf("unexpected error: %v", err)
	}

	t.Setenv("KAGI_TOKEN", "")
	if err := rejectPATForOrgSelection(); err != nil {
		t.Errorf("org selection must be allowed without KAGI_TOKEN, got: %v", err)
	}
}

// --- personal-environment fallback under PAT auth ------------------------------

// The `run`/`pull` personal fallback is suppressed under PAT auth. A CI job that
// asked for the personal environment must get the strict "not found" error, not
// a quiet redirect onto the shared environment every developer pulls.
func TestResolveAppEnv_PersonalFallbackSuppressedUnderPAT(t *testing.T) {
	hermeticHome(t)

	// The app has a shared "prod" environment and no personal one.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data":    []map[string]string{{"id": "e1", "slug": "prod", "name": "Production"}},
			"message": "ok",
			"status":  200,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	// kagi.yaml names prod, which is what the fallback would redirect onto.
	if err := os.WriteFile("kagi.yaml", []byte("app-id: app-123\nenvironment: prod\n"), 0600); err != nil {
		t.Fatalf("write kagi.yaml: %v", err)
	}

	t.Setenv("KAGI_TOKEN", "vv_ci_token")
	vc, err := client.NewKagiClient(ts.URL, "")
	if err != nil {
		t.Fatalf("NewKagiClient: %v", err)
	}
	if !vc.IsPAT() {
		t.Fatal("expected a PAT client")
	}

	cmd := newSecretCmd(t, map[string]string{"personal": "true", "app-id": "app-123"})
	_, err = resolveAppEnvWith(cmd, vc, resolveOpts{allowPersonalFallback: true})
	if err == nil {
		t.Fatal("expected the strict environment-not-found error, got nil (silently fell back)")
	}
	if !strings.Contains(err.Error(), `environment "personal" not found`) {
		t.Errorf("unexpected error: %v", err)
	}
}

// The same request under a logged-in session still gets the fallback: it is the
// human convenience the option exists for, and the warning goes to stderr.
func TestResolveAppEnv_PersonalFallbackKeptForSession(t *testing.T) {
	hermeticHome(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data":    []map[string]string{{"id": "e1", "slug": "prod", "name": "Production"}},
			"message": "ok",
			"status":  200,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	if err := os.WriteFile("kagi.yaml", []byte("app-id: app-123\nenvironment: prod\norganization-id: 11111111-2222-3333-4444-555555555555\n"), 0600); err != nil {
		t.Fatalf("write kagi.yaml: %v", err)
	}

	t.Setenv("KAGI_TOKEN", "")
	store := auth.NewTokenStore()
	if err := store.Save(auth.Credentials{
		AccessToken: "stored-session-jwt",
		ExpiresAt:   time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}

	vc, err := client.NewKagiClient(ts.URL, "")
	if err != nil {
		t.Fatalf("NewKagiClient: %v", err)
	}
	if vc.IsPAT() {
		t.Fatal("expected a session client")
	}

	cmd := newSecretCmd(t, map[string]string{"personal": "true", "app-id": "app-123"})
	got, err := resolveAppEnvWith(cmd, vc, resolveOpts{allowPersonalFallback: true})
	if err != nil {
		t.Fatalf("resolveAppEnvWith: %v", err)
	}
	if got.EnvSlug != "prod" {
		t.Errorf("EnvSlug = %q, want the kagi.yaml fallback %q", got.EnvSlug, "prod")
	}
}
