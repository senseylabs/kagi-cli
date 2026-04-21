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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		deviceFlow := auth.NewDeviceFlow(creds.IssuerURL, "kagi-cli", auth.DefaultScope)
		endpoints, err := deviceFlow.DiscoverEndpoints()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: server-side token revocation failed: %v — your tokens remain valid on the server until their lifetime expires\n", err)
		} else if endpoints.RevocationEndpoint == "" {
			fmt.Fprintf(os.Stderr, "warning: server-side token revocation failed: revocation_endpoint not advertised by issuer — your tokens remain valid on the server until their lifetime expires\n")
		} else if err := deviceFlow.RevokeToken(ctx, endpoints.RevocationEndpoint, creds.RefreshToken); err != nil {
			fmt.Fprintf(os.Stderr, "warning: server-side token revocation failed: %v — your tokens remain valid on the server until their lifetime expires\n", err)
		}
	}

	if err := store.Delete(); err != nil {
		return fmt.Errorf("failed to clear credentials: %w", err)
	}

	fmt.Println("Logged out successfully.")
	return nil
}
