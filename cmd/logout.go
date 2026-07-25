package cmd

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/senseylabs/kagi-cli/internal/auth"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/senseylabs/kagi-cli/internal/ui"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out and clear stored credentials",
	Args:  cobra.NoArgs,
	RunE:  runLogout,
}

func init() {
	rootCmd.AddCommand(logoutCmd)
}

func runLogout(cmd *cobra.Command, args []string) error {
	u := newUI()
	store := auth.NewTokenStore()

	creds, err := store.Load()
	if err != nil {
		u.Info("You are not logged in")
		return nil
	}

	// Best-effort server-side revocation. Never block local logout on network issues.
	if creds.RefreshToken != "" && creds.IssuerURL != "" {
		// Discovery and revocation get separate budgets. DiscoverEndpoints now
		// retries internally via httpx.GetWithRetry, so it needs the more
		// generous window; sharing a single 5s budget with revocation would
		// regress the effective cold-start tolerance this branch exists to add.
		const (
			// discoveryTimeout restores the prior effective tolerance and leaves
			// room for roughly one internal retry through a Keycloak cold start.
			discoveryTimeout = 15 * time.Second
			// revocationTimeout is the fresh, independent budget for the revoke
			// call so a slow discovery cannot eat into it.
			revocationTimeout = 5 * time.Second
		)

		deviceFlow := auth.NewDeviceFlow(creds.IssuerURL, "cli", auth.DefaultScope)

		discoveryCtx, discoveryCancel := context.WithTimeout(context.Background(), discoveryTimeout)
		defer discoveryCancel()

		endpoints, err := deviceFlow.DiscoverEndpoints(discoveryCtx)
		if err != nil {
			u.Warn("server-side token revocation failed: %v — your tokens remain valid on the server until their lifetime expires", err)
		} else if endpoints.RevocationEndpoint == "" {
			u.Warn("server-side token revocation failed: revocation_endpoint not advertised by issuer — your tokens remain valid on the server until their lifetime expires")
		} else {
			revocationCtx, revocationCancel := context.WithTimeout(context.Background(), revocationTimeout)
			defer revocationCancel()

			if err := deviceFlow.RevokeToken(revocationCtx, endpoints.RevocationEndpoint, creds.RefreshToken); err != nil {
				u.Warn("server-side token revocation failed: %v — your tokens remain valid on the server until their lifetime expires", err)
			}
		}
	}

	if err := store.Delete(); err != nil {
		return ui.Wrapf(err, "failed to clear credentials")
	}

	// Clear any stored organization selection so a later multi-org login does not
	// inherit a stale org (which would surface as opaque 403s).
	if err := config.ClearOrganization(); err != nil {
		u.Warn("could not clear the stored organization selection: %v", err)
	}

	u.Success("Logged out successfully")
	return nil
}
