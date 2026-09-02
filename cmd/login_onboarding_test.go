package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/senseylabs/kagi-cli/internal/auth"
	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/senseylabs/kagi-cli/internal/ui"

	kagi "github.com/senseylabs/kagi-sdk"
)

// pinURLs points the URL-derived helpers at a known deployment for the duration
// of a test, restoring the package state afterwards.
func pinURLs(t *testing.T, apiURL string, dev bool) {
	t.Helper()
	prevAPI, prevDev := cfgAPIURL, cfgDevMode
	cfgAPIURL, cfgDevMode = apiURL, dev
	t.Cleanup(func() { cfgAPIURL, cfgDevMode = prevAPI, prevDev })
}

// captureUI returns a UI whose messaging stream is a buffer, so a test can read
// exactly what the person would see.
func captureUI() (*ui.UI, *bytes.Buffer) {
	var buf bytes.Buffer
	return ui.New(ui.Options{Err: &buf}), &buf
}

// onboardingStateServer serves GET /kagi/onboarding/state with the given
// payload, and GET /kagi/organizations as an empty membership list (what the
// backend answers a caller who has not been placed anywhere yet).
func onboardingStateServer(t *testing.T, status map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/kagi/onboarding/state":
			json.NewEncoder(w).Encode(map[string]any{"data": status, "message": "ok", "status": 200})
		case "/kagi/organizations":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{}, "message": "ok", "status": 200})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

// --- hasUsableMembership ------------------------------------------------------

// The gate runs on the two answers that definitively say "no membership": an
// empty 200, and the backend's not-onboarded refusal. Anything else proves
// nothing and must not cost the user a login.
func TestHasUsableMembership(t *testing.T) {
	notOnboarded := mapNotOnboardedError(t)

	cases := []struct {
		name string
		orgs []client.Organization
		err  error
		want bool
	}{
		{name: "one membership", orgs: []client.Organization{{ID: "o1"}}, want: true},
		{name: "empty list means not placed anywhere", orgs: nil, want: false},
		{name: "not-onboarded refusal", err: notOnboarded, want: false},
		{name: "transient failure proves nothing", err: errors.New("connection refused"), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasUsableMembership(tc.orgs, tc.err); got != tc.want {
				t.Errorf("hasUsableMembership = %v, want %v", got, tc.want)
			}
		})
	}
}

// mapNotOnboardedError produces the error a real KGI_SEC_038 response yields, so
// the classification is exercised end to end rather than with a hand-built stub.
func mapNotOnboardedError(t *testing.T) error {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"success": false, "message": "Your account has not finished onboarding.", "error": {"code": "KGI_SEC_038"}}`))
	}))
	defer ts.Close()

	_, err := client.NewKagiClientWithToken(ts.URL, "t").ListOrganizations()
	if err == nil {
		t.Fatal("expected the refusal to surface as an error")
	}
	return err
}

// --- portal URL ---------------------------------------------------------------

func TestPortalOnboardingURL(t *testing.T) {
	cases := []struct {
		name   string
		apiURL string
		dev    bool
		want   string
	}{
		{name: "production", apiURL: "https://api.kagi.pw", want: "https://kagi.pw/onboarding"},
		{name: "dev mode ignores the API host", apiURL: "http://localhost:8081", dev: true, want: devPortalURL + "/onboarding"},
		// An unfamiliar deployment yields no link rather than one that does not
		// resolve; every caller omits the line when it is empty.
		{name: "unknown deployment", apiURL: "https://kagi.internal.example", want: ""},
		{name: "unparseable", apiURL: "://nope", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinURLs(t, tc.apiURL, tc.dev)
			if got := portalOnboardingURL(); got != tc.want {
				t.Errorf("portalOnboardingURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- the waiting-for-approval state ------------------------------------------

// The state a person lands in most often has to read as a deliberate product
// state: it names who is approving, says nothing more is required of them, and
// never presents itself as an error or a permission problem.
func TestDescribeOnboardingSituation_JoinRequestPending(t *testing.T) {
	pinURLs(t, "https://api.kagi.pw", false)
	u, out := captureUI()

	describeOnboardingSituation(u, &client.OnboardingStatus{
		State:                       kagi.OnboardingStateJoinRequestPending,
		JoinRequestOrganizationName: "Sensey B.V.",
	})

	got := out.String()
	for _, want := range []string{"Waiting to be approved by Sensey B.V.", "administrators", "https://kagi.pw/onboarding"} {
		// The name must survive intact: the ui layer trims a trailing period, so
		// an organization name may never sit at the end of a line.
		if !strings.Contains(got, want) {
			t.Errorf("expected the output to contain %q, got:\n%s", want, got)
		}
	}
	assertCalmTone(t, got)
	// Creating an own organization is refused by the backend on this path, so
	// offering it would be a dead end.
	if strings.Contains(strings.ToLower(got), "create") {
		t.Errorf("a pending request must not offer creating an organization, got:\n%s", got)
	}
}

// The one blocked state where creating an own organization is still honored
// says so; the same state without the server's blessing does not.
func TestDescribeOnboardingSituation_OrgNotAvailable(t *testing.T) {
	pinURLs(t, "https://api.kagi.pw", false)

	u, out := captureUI()
	describeOnboardingSituation(u, &client.OnboardingStatus{
		State:                       kagi.OnboardingStateOrgNotAvailable,
		JoinRequestOrganizationName: "Sensey B.V.",
		CanCreateOwnOrganization:    true,
	})
	got := out.String()
	if !strings.Contains(got, "Sensey B.V. is not accepting members right now") {
		t.Errorf("expected the organization to be named, got:\n%s", got)
	}
	if !strings.Contains(got, "organization of your own") {
		t.Errorf("expected the create-your-own option to be offered, got:\n%s", got)
	}
	assertCalmTone(t, got)

	u, out = captureUI()
	describeOnboardingSituation(u, &client.OnboardingStatus{
		State:                       kagi.OnboardingStateOrgNotAvailable,
		JoinRequestOrganizationName: "Sensey B.V.",
		CanCreateOwnOrganization:    false,
	})
	if strings.Contains(out.String(), "organization of your own") {
		t.Errorf("the create option must follow the server's verdict, got:\n%s", out.String())
	}
}

// canCreateOwnOrganization is read from the wire, never inferred from the state:
// the backend answers false for an ONBOARDING_REQUIRED caller whose email domain
// is claimed by an organization that would refuse the create. Offering it there
// would put a button in front of a person that the server rejects.
func TestDescribeOnboardingSituation_OnboardingRequiredHonoursServerVerdict(t *testing.T) {
	pinURLs(t, "https://api.kagi.pw", false)

	u, out := captureUI()
	describeOnboardingSituation(u, &client.OnboardingStatus{
		State:                    kagi.OnboardingStateRequired,
		CanCreateOwnOrganization: true,
	})
	if !strings.Contains(out.String(), "creating your own organization") {
		t.Errorf("expected the create option when the server allows it, got:\n%s", out.String())
	}
	assertCalmTone(t, out.String())

	u, out = captureUI()
	describeOnboardingSituation(u, &client.OnboardingStatus{
		State:                    kagi.OnboardingStateRequired,
		CanCreateOwnOrganization: false,
	})
	got := out.String()
	if strings.Contains(got, "creating your own organization") {
		t.Errorf("the create option must not be offered against the server's verdict, got:\n%s", got)
	}
	if !strings.Contains(got, "requesting to join it") {
		t.Errorf("expected the join route to be named instead, got:\n%s", got)
	}
}

// An unknown state (a newer backend) falls back to the setup guidance rather
// than printing a blank or claiming an approval, and a withheld organization
// name never renders as an empty gap.
func TestDescribeOnboardingSituation_UnknownStateAndMissingName(t *testing.T) {
	pinURLs(t, "https://kagi.internal.example", false)
	u, out := captureUI()

	describeOnboardingSituation(u, &client.OnboardingStatus{State: "SOMETHING_NEW"})

	got := out.String()
	if !strings.Contains(got, "Your Kagi account is not set up yet") {
		t.Errorf("expected the setup fallback, got:\n%s", got)
	}
	// The portal for this deployment cannot be derived, so no link is printed.
	if strings.Contains(got, "Continue here") {
		t.Errorf("no portal URL is known, so no link may be offered, got:\n%s", got)
	}
	assertCalmTone(t, got)
}

// assertCalmTone holds the whole output to the product-state bar: it is not an
// error, not a permission denial, and never a raw status string.
func assertCalmTone(t *testing.T, out string) {
	t.Helper()
	lowered := strings.ToLower(out)
	for _, banned := range []string{"error", "denied", "permission", "forbidden", "403", "failed"} {
		if strings.Contains(lowered, banned) {
			t.Errorf("output must read as a product state, not a failure; found %q in:\n%s", banned, out)
		}
	}
}

// --- the refusal error --------------------------------------------------------

// The one line the command exits on names the actual situation. The pending case
// in particular must say what is being waited on, not just that something failed.
func TestOnboardingRefusalError(t *testing.T) {
	pending := onboardingRefusalError(&client.OnboardingStatus{
		State:                       kagi.OnboardingStateJoinRequestPending,
		JoinRequestOrganizationName: "Sensey B.V.",
	})
	if !strings.Contains(pending.Error(), "Sensey B.V.") || !strings.Contains(pending.Error(), "awaiting approval") {
		t.Errorf("expected the pending reason to name the organization and the approval, got %q", pending)
	}

	// With no name reported the sentence still has to hold together.
	anonymous := onboardingRefusalError(&client.OnboardingStatus{State: kagi.OnboardingStateJoinRequestPending})
	if strings.Contains(anonymous.Error(), "join  is") || strings.Contains(anonymous.Error(), "join is") {
		t.Errorf("a withheld organization name must not leave a gap, got %q", anonymous)
	}

	unavailable := onboardingRefusalError(&client.OnboardingStatus{
		State:                       kagi.OnboardingStateOrgNotAvailable,
		JoinRequestOrganizationSlug: "sensey",
	})
	if !strings.Contains(unavailable.Error(), "sensey") {
		t.Errorf("expected the slug fallback to be used, got %q", unavailable)
	}

	required := onboardingRefusalError(&client.OnboardingStatus{State: kagi.OnboardingStateRequired})
	if !strings.Contains(required.Error(), "not set up yet") {
		t.Errorf("unexpected setup reason: %q", required)
	}
}

// --- the gate itself ----------------------------------------------------------

// The bug this wave exists to fix: a login for an account that never finished
// onboarding used to keep its credentials and exit 0 — a working session that
// skipped onboarding. It must now leave nothing behind and end non-zero.
func TestRequireOnboardedAccount_ClearsTheSessionAndFails(t *testing.T) {
	hermeticHome(t)
	pinURLs(t, "https://api.kagi.pw", false)

	ts := onboardingStateServer(t, map[string]any{
		"state":                       "JOIN_REQUEST_PENDING",
		"userStatus":                  "PENDING_ONBOARDING",
		"joinRequestOrganizationName": "Sensey B.V.",
		"canCreateOwnOrganization":    false,
	})
	defer ts.Close()

	store := auth.NewTokenStore()
	if err := store.Save(auth.Credentials{
		AccessToken:  "stale-access",
		RefreshToken: "stale-refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
		IssuerURL:    "https://auth.kagi.pw/realms/kagi",
		APIURL:       "https://api.kagi.pw",
	}); err != nil {
		t.Fatalf("seeding a session: %v", err)
	}
	if err := config.SaveOrganization("stale", "00000000-0000-0000-0000-000000000001"); err != nil {
		t.Fatalf("seeding an organization selection: %v", err)
	}

	u, out := captureUI()
	err := requireOnboardedAccount(u, client.NewKagiClientWithToken(ts.URL, "token"), false)
	if err == nil {
		t.Fatal("a not-yet-onboarded account must not end in a usable login")
	}
	if !strings.Contains(err.Error(), "Sensey B.V.") {
		t.Errorf("expected the exit reason to name the organization, got %q", err)
	}
	if !strings.Contains(out.String(), "Waiting to be approved by Sensey B.V. —") {
		t.Errorf("expected the waiting state to be shown, got:\n%s", out.String())
	}

	if _, loadErr := store.Load(); !errors.Is(loadErr, auth.ErrNoCredentials) {
		t.Errorf("credentials must not survive a refused login, Load() = %v", loadErr)
	}
	if _, id := config.HomeOrganization(); id != "" {
		t.Errorf("the organization selection must be cleared too, got %q", id)
	}
}

// An account that IS onboarded but holds no membership is a repair case, not an
// onboarding one: the login stands and the session is left alone, because
// clearing it would only make the situation harder to fix.
func TestRequireOnboardedAccount_CompleteKeepsTheSession(t *testing.T) {
	hermeticHome(t)
	pinURLs(t, "https://api.kagi.pw", false)

	ts := onboardingStateServer(t, map[string]any{
		"state":      "COMPLETE",
		"userStatus": "ACTIVE",
	})
	defer ts.Close()

	store := auth.NewTokenStore()
	if err := store.Save(auth.Credentials{AccessToken: "live", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("seeding a session: %v", err)
	}

	u, out := captureUI()
	if err := requireOnboardedAccount(u, client.NewKagiClientWithToken(ts.URL, "token"), false); err != nil {
		t.Fatalf("an onboarded account must keep its login: %v", err)
	}
	if _, loadErr := store.Load(); loadErr != nil {
		t.Errorf("the session must survive, Load() = %v", loadErr)
	}
	if !strings.Contains(out.String(), "belongs to no organization") {
		t.Errorf("expected the situation to be surfaced, got:\n%s", out.String())
	}
}

// A failed state read on a caller the backend has ALREADY refused with
// KGI_SEC_038 changes nothing about the verdict: the account is not onboarded,
// the session goes, and only the tailored wording is lost. This is requirement
// R3 holding while the diagnostic read is unavailable.
func TestRequireOnboardedAccount_ProvenRefusalSurvivesAStateReadFailure(t *testing.T) {
	hermeticHome(t)
	pinURLs(t, "https://api.kagi.pw", false)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()

	store := auth.NewTokenStore()
	if err := store.Save(auth.Credentials{AccessToken: "stale", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("seeding a session: %v", err)
	}

	u, out := captureUI()
	err := requireOnboardedAccount(u, client.NewKagiClientWithToken(ts.URL, "token"), true)
	if err == nil {
		t.Fatal("expected the login to be refused")
	}
	if !strings.Contains(out.String(), "could not read your account setup status") {
		t.Errorf("the failed read must be surfaced, not swallowed, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Your Kagi account is not set up yet") {
		t.Errorf("expected the safe fallback guidance, got:\n%s", out.String())
	}
	if _, loadErr := store.Load(); !errors.Is(loadErr, auth.ErrNoCredentials) {
		t.Errorf("credentials must not survive a refused login, Load() = %v", loadErr)
	}
}

// The regression this fix exists for, on the login path: without a definitive
// refusal in hand, an unreachable backend must not be dressed up as an
// onboarding verdict. The login still does not complete — creating a session on
// an answer we could not read is the wrong direction to fail — but nothing
// claims the account is unfinished, and a session that was already on disk is
// not destroyed over what may be a network blip.
func TestRequireOnboardedAccount_TransientReadIsNotAnOnboardingVerdict(t *testing.T) {
	hermeticHome(t)
	pinURLs(t, "https://api.kagi.pw", false)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()

	store := auth.NewTokenStore()
	if err := store.Save(auth.Credentials{AccessToken: "live", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("seeding a session: %v", err)
	}
	if err := config.SaveOrganization("acme", "00000000-0000-0000-0000-000000000002"); err != nil {
		t.Fatalf("seeding an organization selection: %v", err)
	}

	u, out := captureUI()
	err := requireOnboardedAccount(u, client.NewKagiClientWithToken(ts.URL, "token"), false)
	if err == nil {
		t.Fatal("an unverifiable setup state must not complete the login")
	}
	if !strings.Contains(err.Error(), "could not read your account setup status") {
		t.Errorf("the exit reason must name the read failure, got %q", err)
	}
	for _, banned := range []string{"not set up yet", "awaiting approval", "not accepting members"} {
		if strings.Contains(err.Error(), banned) || strings.Contains(out.String(), banned) {
			t.Errorf("a failed read must not be reported as an onboarding refusal; found %q in:\n%s\n%s", banned, err, out.String())
		}
	}
	if _, loadErr := store.Load(); loadErr != nil {
		t.Errorf("a session must not be destroyed over an unreadable state, Load() = %v", loadErr)
	}
	if _, id := config.HomeOrganization(); id == "" {
		t.Error("the organization selection must survive an unreadable state too")
	}
}

// --- readOnboardingStatus -----------------------------------------------------

// The whole distinction in one place: what the backend SAYS about the account
// (including its KGI_SEC_038 refusal, which an older backend may still answer on
// this route) is an answer; failing to reach or parse it is not.
func TestReadOnboardingStatus_SeparatesTheBackendsAnswerFromAFailedRead(t *testing.T) {
	definitive := func(t *testing.T, handler http.HandlerFunc, want kagi.OnboardingState) {
		t.Helper()
		ts := httptest.NewServer(handler)
		defer ts.Close()

		status, err := readOnboardingStatus(client.NewKagiClientWithToken(ts.URL, "token"))
		if err != nil {
			t.Fatalf("a definitive answer must not surface as a read failure: %v", err)
		}
		if got := status.EffectiveState(); got != want {
			t.Errorf("EffectiveState() = %q, want %q", got, want)
		}
	}

	t.Run("a served state is the answer", func(t *testing.T) {
		definitive(t, func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"state": "COMPLETE"}, "status": 200})
		}, kagi.OnboardingStateComplete)
	})

	t.Run("KGI_SEC_038 on this route is still an answer", func(t *testing.T) {
		definitive(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"success": false, "message": "Your account has not finished onboarding.", "error": {"code": "KGI_SEC_038"}}`))
		}, kagi.OnboardingStateRequired)
	})

	transient := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "5xx", handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }},
		{name: "an unrelated refusal", handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"success": false, "error": {"code": "KGI_SEC_001"}}`))
		}},
		{name: "a body that is not the envelope", handler: func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("<html>proxy says no</html>"))
		}},
	}
	for _, tc := range transient {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(tc.handler)
			defer ts.Close()

			status, err := readOnboardingStatus(client.NewKagiClientWithToken(ts.URL, "token"))
			if err == nil {
				t.Fatalf("expected a read failure, got status %+v", status)
			}
			if status != nil {
				t.Errorf("a failed read must not hand back a state to act on, got %+v", status)
			}
			if !strings.Contains(err.Error(), "could not read your account setup status") {
				t.Errorf("unexpected read error: %v", err)
			}
		})
	}

	// A transport failure (nothing listening) is the same kind of nothing.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ts.URL
	ts.Close()
	if _, err := readOnboardingStatus(client.NewKagiClientWithToken(url, "token")); err == nil {
		t.Error("an unreachable API must surface as a read failure")
	}
}
