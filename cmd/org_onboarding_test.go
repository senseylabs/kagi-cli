package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/senseylabs/kagi-cli/internal/client"

	kagi "github.com/senseylabs/kagi-sdk"
)

// --- refuseIfNotOnboarded -----------------------------------------------------

// The read commands' gate: it may refuse only on what the backend actually said
// about the account. An empty membership list is not that on its own — a fully
// onboarded account can hold one — so when the setup state cannot be read, the
// caller keeps its own wording instead of being told it never finished setup.
func TestRefuseIfNotOnboarded_TransientReadDoesNotBecomeARefusal(t *testing.T) {
	pinURLs(t, "https://api.kagi.pw", false)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()

	u, out := captureUI()
	if err := refuseIfNotOnboarded(u, client.NewKagiClientWithToken(ts.URL, "token"), false); err != nil {
		t.Fatalf("a backend we could not reach must not refuse the user: %v", err)
	}
	if !strings.Contains(out.String(), "could not read your account setup status") {
		t.Errorf("the failed read must be surfaced, not swallowed, got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "not set up yet") {
		t.Errorf("a failed read must not be narrated as an onboarding state, got:\n%s", out.String())
	}
}

// With the backend's own KGI_SEC_038 refusal already in hand the situation is
// settled, so an unreadable state costs the tailored wording and nothing else.
// Requirement R3 does not depend on the diagnostic read succeeding.
func TestRefuseIfNotOnboarded_ProvenRefusalStandsWithoutTheStateRead(t *testing.T) {
	pinURLs(t, "https://api.kagi.pw", false)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()

	u, out := captureUI()
	err := refuseIfNotOnboarded(u, client.NewKagiClientWithToken(ts.URL, "token"), true)
	if err == nil {
		t.Fatal("a caller the backend already refused must still be refused here")
	}
	if !strings.Contains(err.Error(), "not set up yet") {
		t.Errorf("expected the generic setup reason, got %q", err)
	}
	if !strings.Contains(out.String(), "could not read your account setup status") {
		t.Errorf("the failed read must still be surfaced, got:\n%s", out.String())
	}
}

// A state the backend did serve decides the outcome on its own — no proof from a
// previous call needed, in either direction.
func TestRefuseIfNotOnboarded_ServedStateDecides(t *testing.T) {
	pinURLs(t, "https://api.kagi.pw", false)

	pending := onboardingStateServer(t, map[string]any{
		"state":                       string(kagi.OnboardingStateJoinRequestPending),
		"joinRequestOrganizationName": "Sensey B.V.",
	})
	defer pending.Close()

	u, out := captureUI()
	err := refuseIfNotOnboarded(u, client.NewKagiClientWithToken(pending.URL, "token"), false)
	if err == nil {
		t.Fatal("a pending join request must refuse the command")
	}
	if !strings.Contains(err.Error(), "Sensey B.V.") {
		t.Errorf("expected the organization to be named, got %q", err)
	}
	if !strings.Contains(out.String(), "Waiting to be approved by Sensey B.V. —") {
		t.Errorf("expected the situation to be described, got:\n%s", out.String())
	}

	complete := onboardingStateServer(t, map[string]any{"state": string(kagi.OnboardingStateComplete)})
	defer complete.Close()

	u, _ = captureUI()
	if err := refuseIfNotOnboarded(u, client.NewKagiClientWithToken(complete.URL, "token"), true); err != nil {
		t.Errorf("a COMPLETE account must keep the caller's own wording, got %v", err)
	}
}
