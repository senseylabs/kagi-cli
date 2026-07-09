package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/spf13/cobra"
)

var (
	pullOutput string
	pullFormat string
)

var pullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Fetch secrets as KEY=VALUE pairs",
	Long:  "Fetches secrets from Kagi for a given app and environment. Outputs as KEY=VALUE to stdout or to a file.",
	RunE:  runPull,
}

func init() {
	addSecretFlags(pullCmd)
	pullCmd.Flags().StringVar(&pullOutput, "output", "", "Output file path (writes .env file)")
	pullCmd.Flags().StringVar(&pullFormat, "format", "env", "Output format: env or json")
	rootCmd.AddCommand(pullCmd)
}

func runPull(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	// pull opts into the personal fallback: if --personal is passed but the app
	// has no personal environment, fall back to the kagi.yaml environment. The
	// warning is emitted on stderr, so the KEY=VALUE stdout stream stays clean.
	ctx, err := resolveAppEnvWith(cmd, vc, resolveOpts{allowPersonalFallback: true})
	if err != nil {
		return err
	}

	// Fetch secrets
	secrets, err := vc.FetchSecrets(ctx.AppID, ctx.EnvSlug)
	if err != nil {
		return fmt.Errorf("failed to fetch secrets: %w", err)
	}

	// Format output
	var output string
	switch pullFormat {
	case "json":
		data, err := json.MarshalIndent(secrets, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		output = string(data)
	case "env":
		var sb strings.Builder
		for key, value := range secrets {
			sb.WriteString(fmt.Sprintf("%s=\"%s\"\n", key, escapeEnvValue(value)))
		}
		output = sb.String()
	default:
		return fmt.Errorf("unsupported format: %s (use 'env' or 'json')", pullFormat)
	}

	// Write output
	if pullOutput != "" {
		if err := os.WriteFile(pullOutput, []byte(output), 0600); err != nil {
			return fmt.Errorf("failed to write to %s: %w", pullOutput, err)
		}
		fmt.Fprintf(os.Stderr, "Secrets written to %s\n", pullOutput)
	} else {
		fmt.Print(output)
	}

	return nil
}

// escapeEnvValue escapes special characters for safe double-quoted .env values.
func escapeEnvValue(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`$`, `\$`,
		"`", "\\`",
		"\n", `\n`,
	)
	return r.Replace(s)
}
