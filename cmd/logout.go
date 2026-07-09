package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/senseylabs/kagi-cli/internal/auth"
	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out and clear stored credentials",
	RunE:  runLogout,
}

func init() {
	rootCmd.AddCommand(logoutCmd)
}

func runLogout(cmd *cobra.Command, args []string) error {
	store := auth.NewTokenStore()

	creds, err := store.Load()
	if err != nil {
		fmt.Println("You are not logged in.")
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
			fmt.Fprintf(os.Stderr, "warning: server-side token revocation failed: %v — your tokens remain valid on the server until their lifetime expires\n", err)
		} else if endpoints.RevocationEndpoint == "" {
			fmt.Fprintf(os.Stderr, "warning: server-side token revocation failed: revocation_endpoint not advertised by issuer — your tokens remain valid on the server until their lifetime expires\n")
		} else {
			revocationCtx, revocationCancel := context.WithTimeout(context.Background(), revocationTimeout)
			defer revocationCancel()

			if err := deviceFlow.RevokeToken(revocationCtx, endpoints.RevocationEndpoint, creds.RefreshToken); err != nil {
				fmt.Fprintf(os.Stderr, "warning: server-side token revocation failed: %v — your tokens remain valid on the server until their lifetime expires\n", err)
			}
		}
	}

	if err := store.Delete(); err != nil {
		return fmt.Errorf("failed to clear credentials: %w", err)
	}

	fmt.Println("Logged out successfully.")
	return nil
}
