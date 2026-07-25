package cmd

import (
	"fmt"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/ui"
	"github.com/spf13/cobra"
)

// The token commands are deliberately read/revoke only. There is NO
// `kagi token create`: the CLI must never mint an access token, so that the
// only way a token comes into existence is through the audited web console.
// Minting a bearer credential from a shell would be a security regression.

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "List and revoke access tokens",
	Long: "Inspect and revoke Kagi access tokens.\n\n" +
		"There is deliberately no `token create` command: the CLI must never mint an\n" +
		"access token. Tokens are read and revoke only.",
}

var tokenListCmd = &cobra.Command{
	Use:   "list",
	Short: "List access tokens",
	Args:  cobra.NoArgs,
	RunE:  runTokenList,
}

var tokenRevokeYes bool

var tokenRevokeCmd = &cobra.Command{
	Use:               "revoke <id>",
	Short:             "Revoke an access token by id",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeTokenIDs,
	RunE:              runTokenRevoke,
}

func init() {
	tokenRevokeCmd.Flags().BoolVarP(&tokenRevokeYes, "yes", "y", false, "Skip confirmation prompt")

	tokenCmd.AddCommand(tokenListCmd)
	tokenCmd.AddCommand(tokenRevokeCmd)
	rootCmd.AddCommand(tokenCmd)
}

func runTokenList(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	format, err := outputFormat()
	if err != nil {
		return err
	}
	u := newUI()

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	tokens, err := vc.ListAccessTokens()
	if err != nil {
		return fmt.Errorf("failed to list access tokens: %w", err)
	}

	if len(tokens) == 0 && format == ui.FormatTable {
		u.Info("No access tokens found.")
		return nil
	}

	// ID shrinks first — it is the least human-meaningful column but the one a
	// revoke needs, so keep it present and let the name/type stay readable.
	table := ui.NewTable("NAME", "ID", "TYPE", "EXPIRES", "LAST USED").
		SetTruncatable(1, 0)
	for _, t := range tokens {
		expires := t.ExpiresAt
		if expires == "" {
			expires = "never"
		}
		// The backend list response carries no last-used timestamp, so this
		// column is always a placeholder for now.
		table.AddRow(t.Name, t.ID, t.TokenType, expires, "-")
	}

	return u.Print(format, tokens, table)
}

func runTokenRevoke(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	u := newUI()

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	id := args[0]

	if !tokenRevokeYes {
		if !u.Confirm(fmt.Sprintf("Revoke access token %q? This cannot be undone.", id)) {
			return nil
		}
	}

	if err := vc.RevokeAccessToken(id); err != nil {
		return fmt.Errorf("failed to revoke access token: %w", err)
	}

	u.Success("Revoked access token %s.", id)
	return nil
}

// completeTokenIDs offers the caller's access token ids (with the token name as
// the completion description) for `kagi token revoke <id>`, reusing the same
// list endpoint the command consumes.
func completeTokenIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if err := requireAuth(); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	tokens, err := vc.ListAccessTokens()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var out []string
	for _, t := range tokens {
		out = append(out, fmt.Sprintf("%s\t%s", t.ID, t.Name))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
