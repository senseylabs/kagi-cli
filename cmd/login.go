package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	kagi "github.com/senseylabs/kagi-sdk"

	"github.com/spf13/cobra"

	"github.com/senseylabs/kagi-cli/internal/auth"
	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/senseylabs/kagi-cli/internal/httpx"
	"github.com/senseylabs/kagi-cli/internal/ui"
)

// devPortalURL is the Kagi portal's local dev server (apps/portal runs on 3008),
// used for the "finish setting up" link when the CLI is pointed at the dev API.
// The production portal is derived from the API host instead — see
// portalBaseURL.
const devPortalURL = "http://localhost:3008"

var loginForce bool

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with Keycloak via Device Authorization Grant",
	Long: "Starts a Keycloak Device Authorization Grant flow. Opens your browser to complete authentication.\n\n" +
		"When a valid session already exists, login reports it and exits without re-authenticating; " +
		"pass --force to log in again (for example, as a different user).",
	Example: "  kagi login\n" +
		"  kagi login --force",
	Args: cobra.NoArgs,
	RunE: runLogin,
}

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.Flags().BoolVar(&loginForce, "force", false,
		"Re-authenticate even if a valid session already exists")
}

func runLogin(cmd *cobra.Command, args []string) error {
	u := newUI()

	// A bad KAGI_DISCOVERY_TIMEOUT is surfaced here (initConfig cannot return an
	// error) rather than being silently ignored.
	if cfgDiscoveryTimeoutErr != nil {
		return cfgDiscoveryTimeoutErr
	}

	// Short-circuit when a usable session already exists: re-running `kagi login`
	// must recognize the logged-in session instead of walking the whole device
	// flow (printing a URL, a code, and "Waiting for authentication...") again.
	// NewKagiClient is the same gate every command uses — it loads the stored
	// session and refreshes it when the access token has expired, returning an
	// error only when there is no usable session. --force skips this to allow a
	// deliberate re-login (e.g. switching users).
	//
	// Guard on the environment: NewKagiClient loads whatever session is stored
	// without checking it targets the requested API/issuer, so without this a
	// `kagi login --dev` (or a KAGI_API_URL override) would report the existing
	// prod session as "already logged in" and block the re-login the user is
	// actually asking for. Empty URL fields (very old credential blobs) match
	// anything, so an upgrade never wedges login.
	if !loginForce {
		store := auth.NewTokenStore()
		if creds, err := store.Load(); err == nil &&
			(creds.IssuerURL == "" || creds.IssuerURL == cfgIssuer) &&
			(creds.APIURL == "" || creds.APIURL == cfgAPIURL) {
			if sc, err := client.NewSessionClient(cfgAPIURL, cfgIssuer); err == nil {
				// A stored session is not automatically a usable one. An account
				// that has not finished onboarding can only read about itself, so
				// reporting "Already logged in" here would hand back exactly the
				// session-that-skipped-onboarding this command must not produce —
				// and it is also how a session written by an older CLI gets
				// cleaned up. The gate ends the command when it applies.
				sessionOrgs, sessionErr := sc.ListOrganizations()
				if !hasUsableMembership(sessionOrgs, sessionErr) {
					if err := requireOnboardedAccount(u, sc, kagi.IsNotOnboarded(sessionErr)); err != nil {
						return err
					}
				}
				u.Success("Already logged in")
				u.Info("API: %s", cfgAPIURL)
				if slug, id := config.HomeOrganization(); id != "" {
					u.Info("Active organization: %s", slug)
				}
				if auth.StaticToken() != "" {
					u.Warn("KAGI_TOKEN is set and takes precedence — other commands will use it, not this session. Unset it to use this login")
				}
				u.Info("Run 'kagi login --force' to log in again")
				return nil
			}
		}
	}

	if cfgDevMode {
		u.Info("Using local development URLs")
	}

	deviceFlow := auth.NewDeviceFlow(cfgIssuer, "cli", auth.DefaultScope)

	// Step 1: Discover OIDC endpoints. The overall retry budget is the context
	// deadline; per-attempt timeouts and backoff live inside httpx.GetWithRetry.
	u.Status("Discovering Keycloak endpoints...")
	ctx, cancel := context.WithTimeout(context.Background(), cfgDiscoveryTimeout)
	defer cancel()

	endpoints, err := deviceFlow.DiscoverEndpoints(ctx)
	if err != nil {
		if errors.Is(err, httpx.ErrRetryBudgetExhausted) {
			return fmt.Errorf(
				"could not reach the Kagi auth service at %s after %s.\n"+
					"It may be restarting — wait a minute and run `kagi login` again.\n"+
					"If this persists, check %s/.well-known/openid-configuration directly.\n"+
					"(last error: %w)",
				issuerHost(cfgIssuer), cfgDiscoveryTimeout, cfgIssuer, err)
		}
		return fmt.Errorf("failed to discover OIDC endpoints: %w", err)
	}

	// Step 2: Request device authorization
	deviceResp, err := deviceFlow.RequestDeviceAuthorization(endpoints.DeviceAuthorizationEndpoint)
	if err != nil {
		return fmt.Errorf("failed to start device authorization: %w", err)
	}

	// Step 3: Display instructions and try to open browser
	u.Info("")
	u.Info("Open this URL in your browser: %s", deviceResp.VerificationURIComplete)
	u.Info("Enter code: %s", deviceResp.UserCode)
	u.Info("")

	if deviceResp.VerificationURIComplete != "" {
		openBrowser(deviceResp.VerificationURIComplete)
	}

	// Step 4: Poll for token
	u.Status("Waiting for authentication...")
	interval := time.Duration(deviceResp.Interval) * time.Second
	if interval == 0 {
		interval = 5 * time.Second
	}
	expiresAt := time.Now().Add(time.Duration(deviceResp.ExpiresIn) * time.Second)

	tokenResp, err := deviceFlow.PollForToken(endpoints.TokenEndpoint, deviceResp.DeviceCode, interval, expiresAt)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	// Step 5: Refuse to establish a session for an account that has not finished
	// onboarding. Authentication succeeded, but a person who has not been placed
	// in an organization has nothing to use the CLI for, and leaving credentials
	// on disk would let them skip onboarding entirely. The check therefore runs
	// on the freshly issued token BEFORE anything is written, so the refusal path
	// has nothing to undo beyond a session an earlier login left behind.
	vc := client.NewKagiClientWithToken(cfgAPIURL, tokenResp.AccessToken)
	orgs, listErr := vc.ListOrganizations()
	if !hasUsableMembership(orgs, listErr) {
		if err := requireOnboardedAccount(u, vc, kagi.IsNotOnboarded(listErr)); err != nil {
			return err
		}
	}

	// Step 6: Store credentials
	store := auth.NewTokenStore()
	creds := auth.Credentials{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		IssuerURL:    cfgIssuer,
		APIURL:       cfgAPIURL,
		DevMode:      cfgDevMode,
	}

	if err := store.Save(creds); err != nil {
		return fmt.Errorf("failed to store credentials: %w", err)
	}

	u.Success("Login successful")
	u.Info("API: %s", cfgAPIURL)

	// Step 7: Reconcile the organization selection against the membership list
	// already fetched above. Non-fatal: a hiccup here must not block a successful
	// login — the user can always run `kagi org use` later.
	selectOrganizationAfterLogin(u, orgs, listErr)

	return nil
}

// hasUsableMembership reports whether the org-list result proves the caller has
// a workspace to work in, and therefore that the onboarding gate can be skipped.
//
// The two "no" answers are deliberately different in kind. An empty 200 is the
// backend's definitive statement that this account holds no membership — a
// not-yet-onboarded caller is admitted to the list endpoint precisely so it can
// be told that instead of being met with an opaque 403 — and so is the
// KGI_SEC_038 refusal an older backend would send. Any OTHER failure (a network
// blip, a 502) proves nothing, so it counts as usable here: refusing a login
// over a transient error would be worse than letting it through, and every
// subsequent command still meets the same gate server-side.
func hasUsableMembership(orgs []client.Organization, listErr error) bool {
	if listErr != nil {
		return !kagi.IsNotOnboarded(listErr)
	}
	return len(orgs) > 0
}

// requireOnboardedAccount ends a login that would leave a session for an account
// which has not finished onboarding. It returns nil when the account is
// onboarded (so the caller proceeds), and otherwise explains the person's own
// situation, clears any stored session, and returns an error so the command
// exits non-zero.
//
// It reads GET /kagi/onboarding/state, the one route such an account may call
// about itself. provenNotOnboarded says whether the caller already holds the
// backend's DEFINITIVE not-onboarded refusal (KGI_SEC_038) from the org-list
// call, because that decides what a failed state read may be turned into:
//
//   - proven: the account's situation is already settled, so a failed read only
//     costs the tailored wording. The refusal stands (requirement R3) and the
//     generic setup guidance takes over.
//   - not proven: all that is known is an empty membership list, which an
//     ONBOARDED account can also produce. Calling that "you have not finished
//     onboarding" because a request failed would be a lie, so the read failure
//     is reported as itself. The login still does not complete — refusing to
//     CREATE a session on an unverifiable answer is the safe direction — but no
//     onboarding verdict is announced and no stored session is destroyed over
//     what may be a network blip.
func requireOnboardedAccount(u *ui.UI, vc *client.KagiClient, provenNotOnboarded bool) error {
	status, readErr := readOnboardingStatus(vc)
	if readErr != nil {
		if !provenNotOnboarded {
			u.Info("")
			u.Info("This is a problem reaching Kagi, not a problem with your account — try `kagi login` again in a moment")
			return ui.Wrapf(readErr, "the login was not completed")
		}
		// The org-list call already answered KGI_SEC_038: not onboarded is not
		// in doubt, only the wording is. Surface the read failure and fall back
		// to the zero value, which EffectiveState reads as ONBOARDING_REQUIRED.
		u.Warn("%v", readErr)
		status = &client.OnboardingStatus{}
	}

	if status.EffectiveState() == kagi.OnboardingStateComplete {
		// Onboarded, yet holding no membership: a repair case rather than an
		// onboarding one. Say so and let the login stand — clearing the session
		// would only make it harder to fix.
		u.Warn("your account is set up but belongs to no organization — ask an administrator to add you")
		return nil
	}

	describeOnboardingSituation(u, status)
	clearStoredSession(u)
	return onboardingRefusalError(status)
}

// describeOnboardingSituation prints the person's own setup state as a calm,
// finished product state — what is happening, who it is waiting on, and where to
// continue — rather than as a failure. The wording is per state because the
// three states ask for genuinely different things.
func describeOnboardingSituation(u *ui.UI, status *client.OnboardingStatus) {
	org := status.JoinTargetLabel()
	if org == "" {
		// The backend reports no name for a state that has no join target, and
		// may legitimately withhold one; never print a blank organization.
		org = "the organization that manages your email address"
	}

	portalLabel := "Continue here: %s"

	u.Info("")
	switch status.EffectiveState() {
	case kagi.OnboardingStateJoinRequestPending:
		portalLabel = "Check the status here: %s"
		// Nothing dynamic may end a line: the ui layer trims a trailing period,
		// which would eat the last character of a name like "Sensey B.V.".
		u.Info("Waiting to be approved by %s — your request to join is with its administrators", org)
		u.Info("Nothing more is needed from you — run `kagi login` again once it has been approved")
	case kagi.OnboardingStateOrgNotAvailable:
		u.Info("%s is not accepting members right now", org)
		// Phrased without repeating the name, so it reads the same whether the
		// organization was named or the neutral fallback is standing in for it.
		u.Info("Your email address belongs there, but the organization cannot take new members at the moment")
		if status.CanCreateOwnOrganization {
			u.Info("You can set up an organization of your own instead")
		}
	default:
		u.Info("Your Kagi account is not set up yet")
		if status.CanCreateOwnOrganization {
			u.Info("Finish setting up by creating your own organization, or by requesting to join an existing one")
		} else {
			// The server has already decided a create would be refused
			// (KGI_STA_002), so offering it here would be a dead end.
			u.Info("Your email address belongs to an existing organization, so setup continues by requesting to join it")
		}
	}

	if portal := portalOnboardingURL(); portal != "" {
		u.Info("")
		u.Info(portalLabel, portal)
	}
}

// onboardingRefusalError is the one line explaining why a command that needs a
// set-up account exits non-zero. describeOnboardingSituation has already told
// the whole story, so this only has to state the reason without blame.
func onboardingRefusalError(status *client.OnboardingStatus) error {
	org := status.JoinTargetLabel()

	switch status.EffectiveState() {
	case kagi.OnboardingStateJoinRequestPending:
		if org != "" {
			return ui.Errorf("your request to join %s is awaiting approval, so your account is not active yet", org)
		}
		return ui.Errorf("your request to join an organization is awaiting approval, so your account is not active yet")
	case kagi.OnboardingStateOrgNotAvailable:
		if org != "" {
			// The name is never first: ui.Errorf lowercases the opening letter of
			// an error, which would render "Sensey B.V." as "sensey B.V.".
			return ui.Errorf("the organization %s cannot take new members right now, so your account is not set up yet", org)
		}
		return ui.Errorf("the organization that manages your email address cannot take new members right now, so your account is not set up yet")
	default:
		return ui.Errorf("your Kagi account is not set up yet")
	}
}

// readOnboardingStatus fetches the caller's own setup state for a command that
// has to explain itself. The two ways this can go wrong are deliberately kept
// apart, because only one of them is an answer about the account:
//
//   - a status (nil error) is the backend's own verdict, definitive whatever it
//     says. So is the endpoint answering KGI_SEC_038 — an older backend that
//     does not exempt this route still states plainly that the account has not
//     finished onboarding — which is reported as the zero value, read by
//     EffectiveState as ONBOARDING_REQUIRED.
//   - anything else (no connection, a timeout, a 5xx, a body that is not the
//     envelope) is a failure to REACH or UNDERSTAND the backend. It says nothing
//     about the account, so it comes back as an error for the caller to report
//     as itself rather than as an onboarding refusal.
func readOnboardingStatus(vc *client.KagiClient) (*client.OnboardingStatus, error) {
	status, err := vc.GetOnboardingState()
	switch {
	case err == nil:
		return status, nil
	case kagi.IsNotOnboarded(err):
		return &client.OnboardingStatus{}, nil
	default:
		return nil, fmt.Errorf("could not read your account setup status: %w", err)
	}
}

// clearStoredSession removes any credentials and organization selection left on
// disk, so a refused login never leaves a usable session behind — including one
// written by an earlier login or by an older CLI version. Failures are surfaced
// together with the command that fixes them; they do not change the refusal.
func clearStoredSession(u *ui.UI) {
	if err := auth.NewTokenStore().Delete(); err != nil {
		u.Warn("could not clear the stored session: %v — run `kagi logout` to remove it", err)
	}
	if err := config.ClearOrganization(); err != nil {
		u.Warn("could not clear the stored organization selection: %v", err)
	}
}

// portalOnboardingURL is where a person finishes setting up their Kagi account,
// or "" when this deployment's portal cannot be derived (see portalBaseURL).
func portalOnboardingURL() string {
	base := portalBaseURL()
	if base == "" {
		return ""
	}
	return base + "/onboarding"
}

// portalBaseURL derives the Kagi portal origin from the configured API URL: the
// portal is the API host without its "api." label (api.kagi.pw -> kagi.pw). In
// dev mode it is the local portal dev server instead.
//
// A deployment whose shape is not recognized yields "" rather than a guess —
// printing a URL that does not resolve is worse than printing none, and every
// caller omits the line when it is empty.
func portalBaseURL() string {
	if cfgDevMode {
		return devPortalURL
	}
	parsed, err := url.Parse(cfgAPIURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	host, found := strings.CutPrefix(parsed.Host, "api.")
	if !found {
		return ""
	}
	return parsed.Scheme + "://" + host
}

// selectOrganizationAfterLogin reconciles the stored selection with the
// membership list runLogin already fetched. A stored org that is no longer a
// membership is cleared (so a stale id can't surface as opaque 403s), but a
// still-valid selection is left untouched — we never force re-selection on a
// routine multi-org login. With exactly one org it auto-selects; with several
// and no valid stored selection it points the user at `kagi org use`. Every
// branch is best-effort — failures are surfaced as warnings, never errors.
//
// It takes the list rather than fetching it because the onboarding gate has
// already made that call, and the answer is the same one.
func selectOrganizationAfterLogin(u *ui.UI, orgs []client.Organization, listErr error) {
	if listErr != nil {
		u.Warn("could not load your organizations: %v — run 'kagi org list' to retry", listErr)
		return
	}

	// Reconcile the stored selection: clear it only when it is no longer one of
	// this user's memberships.
	_, storedID := config.HomeOrganization()
	if storedID != "" && !orgContains(orgs, storedID) {
		if err := config.ClearOrganization(); err != nil {
			u.Warn("could not clear the previously selected organization: %v", err)
		}
		storedID = ""
	}

	switch len(orgs) {
	case 0:
		// Reached only by an ACTIVE account whose memberships are missing: a
		// not-yet-onboarded caller never gets this far, requireOnboardedAccount
		// ends the command first.
		u.Info("")
		u.Info("You do not belong to any organizations yet. Ask an admin to add you, then run 'kagi org use <slug>'")
	case 1:
		org := orgs[0]
		if err := config.SaveOrganization(org.Slug, org.ID); err != nil {
			u.Warn("could not save active organization: %v — run 'kagi org use %s'", err, org.Slug)
			return
		}
		u.Success("Active organization: %s (%s)", org.Slug, org.Name)
	default:
		// A still-valid selection is kept; just tell the user how to switch.
		if storedID != "" {
			u.Info("")
			u.Info("Active organization kept. Switch with: kagi org use <slug>")
			return
		}
		u.Info("")
		u.Info("You belong to multiple organizations:")
		for _, o := range orgs {
			u.Info("  - %s (%s)", o.Slug, o.Name)
		}
		u.Info("Select one with: kagi org use <slug>")
	}
}

// orgContains reports whether any org in orgs has the given id.
func orgContains(orgs []client.Organization, id string) bool {
	for _, o := range orgs {
		if o.ID == id {
			return true
		}
	}
	return false
}

// issuerHost renders the scheme+host of an issuer URL for user-facing messages
// (e.g. "https://auth.kagi.pw"), falling back to the raw issuer if it cannot be
// parsed so we never drop context in an error.
func issuerHost(issuer string) string {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return issuer
	}
	return u.Scheme + "://" + u.Host
}

func openBrowser(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "linux":
		cmd = exec.Command("xdg-open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Could not open browser automatically. Please open the URL manually.\n")
	}
}
