package cmd

import (
	"fmt"

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

	if _, err := store.Load(); err != nil {
		fmt.Println("You are not logged in.")
		return nil
	}

	if err := store.Delete(); err != nil {
		return fmt.Errorf("failed to clear credentials: %w", err)
	}

	fmt.Println("Logged out successfully.")
	return nil
}
