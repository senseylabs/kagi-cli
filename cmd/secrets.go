package cmd

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/spf13/cobra"
)

var keyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

var secretsCmd = &cobra.Command{
	Use:   "secrets [path]",
	Short: "Browse folders/apps and manage secrets",
	Long: "Browse the secrets folder tree and manage secrets for an app's environment.\n" +
		"  kagi secrets                              browse the secrets root (folders + apps)\n" +
		"  kagi secrets /village                     browse a folder\n" +
		"  kagi secrets envs -p /village/kaizen      list an app's environments\n" +
		"  kagi secrets list -p /village/kaizen -e prod   list masked secrets\n\n" +
		"Apps are addressed by their stable app ID. A --path is resolved to that ID\n" +
		"once; --app-id supplies it directly. Both override the kagi.yaml binding.",
	Args: cobra.MaximumNArgs(1),
	RunE: runSecretsBrowse,
}

var secretSetFromFile string

var secretSetCmd = &cobra.Command{
	Use:   "set [KEY=VALUE...]",
	Short: "Set one or more secrets",
	Long:  "Set secrets as KEY=VALUE pairs, or import from an .env file with --from-file.",
	RunE:  runSecretSet,
}

var secretGetCmd = &cobra.Command{
	Use:   "get <KEY>",
	Short: "Get a single secret (decrypted)",
	Args:  cobra.ExactArgs(1),
	RunE:  runSecretGet,
}

var secretDeleteYes bool

var secretDeleteCmd = &cobra.Command{
	Use:   "delete <KEY>",
	Short: "Delete a secret",
	Args:  cobra.ExactArgs(1),
	RunE:  runSecretDelete,
}

var secretListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all secrets (masked)",
	RunE:  runSecretList,
}

var secretEnvsCmd = &cobra.Command{
	Use:   "envs",
	Short: "List environments for an app",
	RunE:  runSecretEnvs,
}

func init() {
	secretSetCmd.Flags().StringVar(&secretSetFromFile, "from-file", "", "Import secrets from an .env file")
	addSecretFlags(secretSetCmd)
	addSecretFlags(secretGetCmd)
	addSecretFlags(secretDeleteCmd)
	addSecretFlags(secretListCmd)
	addSecretFlags(secretEnvsCmd)

	secretDeleteCmd.Flags().BoolVarP(&secretDeleteYes, "yes", "y", false, "Skip confirmation prompt")

	secretsCmd.AddCommand(secretSetCmd, secretGetCmd, secretDeleteCmd, secretListCmd, secretEnvsCmd)

	rootCmd.AddCommand(secretsCmd)
}

// runSecretsBrowse handles bare `kagi secrets [path]` — it browses the SECRETS
// folder tree at the given path (root when omitted), listing child folders and
// the apps directly under it. Apps carry their stable ID, which is what setup
// captures and what addresses secrets.
func runSecretsBrowse(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	path := "/"
	if len(args) == 1 {
		path = args[0]
	}

	children, err := vc.ListFolderChildren(path)
	if err != nil {
		return fmt.Errorf("failed to browse %q: %w", path, err)
	}

	if len(children.Folders) == 0 && len(children.Apps) == 0 {
		fmt.Printf("No folders or apps under %q.\n", path)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TYPE\tNAME\tSLUG\tAPP ID")
	for _, f := range children.Folders {
		fmt.Fprintf(w, "folder\t%s\t%s\t%s\n", f.Name, f.Slug, "")
	}
	for _, a := range children.Apps {
		fmt.Fprintf(w, "app\t%s\t%s\t%s\n", a.Name, a.Slug, a.ID)
	}
	return w.Flush()
}

func runSecretEnvs(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	appID, label, err := resolveAppOnly(cmd, vc)
	if err != nil {
		return err
	}

	envs, err := vc.ListEnvironments(appID)
	if err != nil {
		return classifyAppError(err, label)
	}

	if len(envs) == 0 {
		fmt.Printf("No environments found for app %s.\n", label)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSLUG")
	for _, e := range envs {
		fmt.Fprintf(w, "%s\t%s\t%s\n", e.ID, e.Name, e.Slug)
	}
	return w.Flush()
}

func runSecretSet(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	ctx, err := resolveAppEnv(cmd, vc)
	if err != nil {
		return err
	}

	secrets := make(map[string]string)

	if secretSetFromFile != "" {
		// Parse .env file
		file, err := os.Open(secretSetFromFile)
		if err != nil {
			return fmt.Errorf("failed to open file %s: %w", secretSetFromFile, err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := strings.TrimSpace(scanner.Text())

			// Skip empty lines and comments
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			key, value, ok := parseKeyValue(line)
			if !ok {
				return fmt.Errorf("invalid format at line %d: %s", lineNum, line)
			}
			if !keyPattern.MatchString(key) {
				return fmt.Errorf("invalid key %q at line %d: must be UPPERCASE_WITH_UNDERSCORES (e.g., DATABASE_URL)", key, lineNum)
			}
			secrets[key] = value
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}
	} else {
		// Parse KEY=VALUE args
		if len(args) == 0 {
			return fmt.Errorf("provide KEY=VALUE pairs or use --from-file")
		}

		for _, arg := range args {
			key, value, ok := parseKeyValue(arg)
			if !ok {
				return fmt.Errorf("invalid format: %s (expected KEY=VALUE)", arg)
			}
			if !keyPattern.MatchString(key) {
				return fmt.Errorf("invalid key %q: must be UPPERCASE_WITH_UNDERSCORES (e.g., DATABASE_URL)", key)
			}
			secrets[key] = value
		}
	}

	if len(secrets) == 0 {
		return fmt.Errorf("no secrets to set")
	}

	if err := vc.SetSecrets(ctx.AppID, ctx.EnvSlug, secrets); err != nil {
		return fmt.Errorf("failed to set secrets: %w", err)
	}

	fmt.Printf("Set %d secret(s).\n", len(secrets))
	return nil
}

func runSecretGet(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	ctx, err := resolveAppEnv(cmd, vc)
	if err != nil {
		return err
	}

	keyName := args[0]

	// List secrets to find the one with matching key name
	secretsList, err := vc.ListSecrets(ctx.AppID, ctx.EnvSlug)
	if err != nil {
		return fmt.Errorf("failed to list secrets: %w", err)
	}

	var secretID string
	for _, s := range secretsList {
		if strings.EqualFold(s.KeyName, keyName) {
			secretID = s.ID
			break
		}
	}
	if secretID == "" {
		return fmt.Errorf("secret %q not found", keyName)
	}

	revealed, err := vc.GetSecret(ctx.AppID, ctx.EnvSlug, secretID)
	if err != nil {
		return fmt.Errorf("failed to get secret: %w", err)
	}

	fmt.Println(revealed.Value)
	return nil
}

func runSecretDelete(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	ctx, err := resolveAppEnv(cmd, vc)
	if err != nil {
		return err
	}

	keyName := args[0]

	// List secrets to find the one with matching key name
	secretsList, err := vc.ListSecrets(ctx.AppID, ctx.EnvSlug)
	if err != nil {
		return fmt.Errorf("failed to list secrets: %w", err)
	}

	var secretID string
	for _, s := range secretsList {
		if strings.EqualFold(s.KeyName, keyName) {
			secretID = s.ID
			break
		}
	}
	if secretID == "" {
		return fmt.Errorf("secret %q not found", keyName)
	}

	// Confirm deletion
	if !secretDeleteYes {
		fmt.Printf("Are you sure you want to delete secret %q? [y/N]: ", keyName)
		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read input: %w", err)
		}
		input = strings.TrimSpace(strings.ToLower(input))
		if input != "y" && input != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if err := vc.DeleteSecret(ctx.AppID, ctx.EnvSlug, secretID); err != nil {
		return fmt.Errorf("failed to delete secret: %w", err)
	}

	fmt.Printf("Deleted secret %q.\n", keyName)
	return nil
}

func runSecretList(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	ctx, err := resolveAppEnv(cmd, vc)
	if err != nil {
		return err
	}

	secrets, err := vc.ListSecrets(ctx.AppID, ctx.EnvSlug)
	if err != nil {
		return fmt.Errorf("failed to list secrets: %w", err)
	}

	if len(secrets) == 0 {
		fmt.Println("No secrets found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tVALUE\tUPDATED")
	for _, s := range secrets {
		fmt.Fprintf(w, "%s\t%s\t%s\n", s.KeyName, s.MaskedValue, s.UpdatedAt)
	}
	return w.Flush()
}

// parseKeyValue splits a string on the first '=' into key and value.
// It strips optional surrounding quotes from the value.
func parseKeyValue(s string) (string, string, bool) {
	idx := strings.Index(s, "=")
	if idx < 1 {
		return "", "", false
	}
	key := strings.TrimSpace(s[:idx])
	value := s[idx+1:]

	// Strip surrounding quotes from the value
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
	}

	return key, value, true
}
