package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/senseylabs/kagi-cli/internal/client"
)

var pullOutFile string

var pullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Fetch secrets as KEY=VALUE pairs",
	Long:  "Fetches secrets from Kagi for a given app and environment. Outputs as KEY=VALUE (env), JSON, or YAML to stdout or to a file.",
	Args:  cobra.NoArgs,
	RunE:  runPull,
}

func init() {
	addSecretFlags(pullCmd)
	pullCmd.Flags().StringVar(&pullOutFile, "out-file", "", "Write output to this file (0600) instead of stdout")
	rootCmd.AddCommand(pullCmd)
}

func runPull(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	u := newUI()

	// Resolve the output format up front (pre-flight), before any network call, so
	// a typo fails instantly rather than after fetching secrets. pull has no table
	// view, so the global -o default (unset) means env; an explicit -o must name
	// one of env|json|yaml.
	format := "env"
	if cmd.Flags().Changed("output") {
		format = strings.ToLower(strings.TrimSpace(outputValue))
	}
	switch format {
	case "env", "json", "yaml":
	default:
		return fmt.Errorf("unsupported format %q (use 'env', 'json', or 'yaml')", outputValue)
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
	switch format {
	case "json":
		data, err := json.MarshalIndent(secrets, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		output = string(data) + "\n"
	case "yaml":
		data, err := yaml.Marshal(secrets)
		if err != nil {
			return fmt.Errorf("failed to marshal YAML: %w", err)
		}
		output = string(data)
	case "env":
		// Emit keys in a deterministic (sorted) order so repeated pulls diff cleanly.
		keys := make([]string, 0, len(secrets))
		for k := range secrets {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&sb, "%s=\"%s\"\n", k, escapeEnvValue(secrets[k]))
		}
		output = sb.String()
	}

	// Write output
	if pullOutFile != "" {
		if err := os.WriteFile(pullOutFile, []byte(output), 0600); err != nil {
			return fmt.Errorf("failed to write to %s: %w", pullOutFile, err)
		}
		u.Success("Secrets written to %s.", pullOutFile)
		return nil
	}

	fmt.Fprint(u.Out(), output)
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
