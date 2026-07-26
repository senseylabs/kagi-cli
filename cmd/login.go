package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/senseylabs/kagi-cli/internal/auth"
	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/senseylabs/kagi-cli/internal/httpx"
	"github.com/senseylabs/kagi-cli/internal/ui"
)

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
			if _, err := client.NewKagiClient(cfgAPIURL, cfgIssuer); err == nil {
				u.Success("Already logged in")
				u.Info("API: %s", cfgAPIURL)
				if slug, id := config.HomeOrganization(); id != "" {
					u.Info("Active organization: %s", slug)
				}
				if os.Getenv("KAGI_TOKEN") != "" {
					u.Warn("KAGI_TOKEN is set; other commands reject it — unset it to use this session")
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

	// Step 5: Store credentials
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

	// Resolve organization membership. Non-fatal: a hiccup here must not block a
	// successful login — the user can always run `kagi org use` later.
	selectOrganizationAfterLogin(u, tokenResp.AccessToken)

	return nil
}

// selectOrganizationAfterLogin lists the user's organizations and reconciles the
// stored selection with them. A stored org that is no longer a membership is
// cleared (so a stale id can't surface as opaque 403s), but a still-valid
// selection is left untouched — we never force re-selection on a routine
// multi-org login. With exactly one org it auto-selects; with several and no
// valid stored selection it points the user at `kagi org use`; with none it
// hints they need to create or join one. Every branch is best-effort — failures
// are surfaced as warnings, never errors.
func selectOrganizationAfterLogin(u *ui.UI, accessToken string) {
	vc := client.NewKagiClientWithToken(cfgAPIURL, accessToken)
	orgs, err := vc.ListOrganizations()
	if err != nil {
		u.Warn("could not load your organizations: %v — run 'kagi org list' to retry", err)
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
