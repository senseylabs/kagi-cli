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
	"github.com/senseylabs/kagi-cli/internal/ui"
)

var pullOutFile string

var pullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Fetch secrets as KEY=VALUE pairs",
	Long: "Fetches secrets from Kagi for a given app and environment. Outputs as KEY=VALUE (env), JSON, or YAML to stdout or to a file.\n\n" +
		"Deprecated: --output <path> is the pre-v0.20.0 spelling of --out-file. It still writes that file, with a warning on stderr; migrate to --out-file.",
	Args: cobra.NoArgs,
	RunE: runPull,
}

func init() {
	addSecretFlags(pullCmd)
	pullCmd.Flags().StringVar(&pullOutFile, "out-file", "", "Write output to this file (0600) instead of stdout (deprecated alias: --output <path>)")
	rootCmd.AddCommand(pullCmd)
}

func runPull(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	u := newUI()

	// Fold the deprecated `--output <path>` spelling into --out-file before the
	// format is read, so a path-shaped value never reaches the format switch.
	outFile, outputIsFile, err := resolvePullOutFile(u, outputValue, cmd.Flags().Changed("output"),
		pullOutFile, cmd.Flags().Changed("out-file"))
	if err != nil {
		return err
	}

	// Resolve the output format up front (pre-flight), before any network call, so
	// a typo fails instantly rather than after fetching secrets. pull has no table
	// view, so the global -o default (unset) means env; an explicit -o must name
	// one of env|json|yaml.
	format := "env"
	if cmd.Flags().Changed("output") && !outputIsFile {
		format = strings.ToLower(strings.TrimSpace(outputValue))
	}
	switch format {
	case "env", "json", "yaml":
	default:
		return enrichFormatError(outputValue,
			fmt.Errorf("unsupported format %q (use 'env', 'json', or 'yaml')", outputValue), true)
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
	if outFile != "" {
		if err := os.WriteFile(outFile, []byte(output), 0600); err != nil {
			return fmt.Errorf("failed to write to %s: %w", outFile, err)
		}
		u.Success("Secrets written to %s.", outFile)
		return nil
	}

	fmt.Fprint(u.Out(), output)
	return nil
}

// resolvePullOutFile folds the deprecated `--output <path>` spelling into
// --out-file.
//
// Before v0.20.0, --output on `kagi pull` named the output FILE; v0.20.0
// repurposed it as the payload FORMAT and moved the file to --out-file, which
// broke every caller carrying an old invocation forward. A path-shaped value is
// unambiguous — no format name contains a separator or a dot — so it is still
// honored as the file, with a deprecation warning on stderr, leaving stdout
// clean for callers parsing the KEY=VALUE stream.
//
// It returns the file to write (empty means stdout) and whether --output was
// consumed as that file, which tells the caller not to also read it as a
// format. Passing both spellings at once is an error rather than a silent
// precedence rule: a caller doing that has a real bug and should be told.
func resolvePullOutFile(u *ui.UI, outputVal string, outputChanged bool, outFile string, outFileChanged bool) (string, bool, error) {
	if !outputChanged || !looksLikeFilePath(outputVal) {
		return outFile, false, nil
	}
	if outFileChanged {
		return "", false, fmt.Errorf(
			"--output %s and --out-file %s were both given: --output with a file path is the deprecated spelling of --out-file, so pass only --out-file",
			outputVal, outFile)
	}
	u.Warn("--output %s is deprecated for naming a file; use --out-file %s instead — the alias will be removed in a future release",
		outputVal, outputVal)
	return outputVal, true, nil
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
