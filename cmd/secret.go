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

var secretCmd = &cobra.Command{
	Use:   "secret",
	Short: "Manage secrets",
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

func init() {
	secretSetCmd.Flags().StringVar(&secretSetFromFile, "from-file", "", "Import secrets from an .env file")
	addProjectAppEnvFlags(secretSetCmd)
	addProjectAppEnvFlags(secretGetCmd)
	addProjectAppEnvFlags(secretDeleteCmd)
	addProjectAppEnvFlags(secretListCmd)

	secretDeleteCmd.Flags().BoolVarP(&secretDeleteYes, "yes", "y", false, "Skip confirmation prompt")

	secretCmd.AddCommand(secretSetCmd)
	secretCmd.AddCommand(secretGetCmd)
	secretCmd.AddCommand(secretDeleteCmd)
	secretCmd.AddCommand(secretListCmd)
	rootCmd.AddCommand(secretCmd)
}

func runSecretSet(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	ctx, err := resolveProjectAppEnv(cmd, vc)
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

	if err := vc.SetSecrets(ctx.AppID, ctx.EnvID, secrets); err != nil {
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

	ctx, err := resolveProjectAppEnv(cmd, vc)
	if err != nil {
		return err
	}

	keyName := args[0]

	// List secrets to find the one with matching key name
	secretsList, err := vc.ListSecrets(ctx.AppID, ctx.EnvID)
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

	revealed, err := vc.GetSecret(ctx.AppID, ctx.EnvID, secretID)
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

	ctx, err := resolveProjectAppEnv(cmd, vc)
	if err != nil {
		return err
	}

	keyName := args[0]

	// List secrets to find the one with matching key name
	secretsList, err := vc.ListSecrets(ctx.AppID, ctx.EnvID)
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

	if err := vc.DeleteSecret(ctx.AppID, ctx.EnvID, secretID); err != nil {
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

	ctx, err := resolveProjectAppEnv(cmd, vc)
	if err != nil {
		return err
	}

	secrets, err := vc.ListSecrets(ctx.AppID, ctx.EnvID)
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
