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
	Use:   "secrets",
	Short: "Manage projects, apps, environments, and secrets",
	Long: "Flag-driven listing and mutations for the project/app/env/secrets hierarchy.\n" +
		"  kagi secrets                                  list all projects\n" +
		"  kagi secrets -p Village                       list apps in project\n" +
		"  kagi secrets -p Village -a kaizen -e prod     list masked secrets",
	Args: cobra.NoArgs,
	RunE: runSecretsDispatch,
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

	// Dispatcher flags on `kagi secrets` itself. Regular Flags (not PersistentFlags)
	// so they don't collide with the -p/-a/-e flags already declared on child
	// commands (set/get/delete/list, and the management subtrees).
	secretsCmd.Flags().StringP("project", "p", "", "Project name")
	secretsCmd.Flags().StringP("app", "a", "", "App name")
	secretsCmd.Flags().StringP("env", "e", "", "Environment slug")

	// Management subtrees moved under `secrets`.
	secretsCmd.AddCommand(projectCmd, appCmd, envCmd)

	// Secret mutations + explicit list alias.
	secretsCmd.AddCommand(secretSetCmd, secretGetCmd, secretDeleteCmd, secretListCmd)

	rootCmd.AddCommand(secretsCmd)
}

// runSecretsDispatch routes bare `kagi secrets [-p ...] [-a ...] [-e ...]` invocations
// to the appropriate listing based on which flags were explicitly passed.
//
// Important: use cmd.Flags().Changed(...) to detect flag presence rather than
// comparing the string value to "" — that conflates "not passed" with "passed
// empty" and would silently activate the kagi.yaml fallback inside
// resolveProjectAppEnv, which is not what the user asked for here.
func runSecretsDispatch(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	hasP := cmd.Flags().Changed("project")
	hasA := cmd.Flags().Changed("app")

	projectName, _ := cmd.Flags().GetString("project")

	switch {
	case !hasP && !hasA:
		return listAllProjects(vc)
	case hasP && !hasA:
		return listAppsInProject(vc, projectName)
	case hasP && hasA:
		// Reuse the existing secret-list handler — it calls resolveProjectAppEnv
		// which enforces that -e is also set (no silent default for env).
		return runSecretList(cmd, args)
	default: // !hasP && hasA
		return fmt.Errorf("-a/--app requires -p/--project")
	}
}

// listAllProjects renders every project the user can see. Moved here from the
// deleted cmd/projects.go so the bare `kagi secrets` invocation stays
// self-contained.
func listAllProjects(vc *client.KagiClient) error {
	projects, err := vc.ListProjects()
	if err != nil {
		return fmt.Errorf("failed to list projects: %w", err)
	}

	if len(projects) == 0 {
		fmt.Println("No projects found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSLUG\tDESCRIPTION")
	for _, p := range projects {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.ID, p.Name, p.Slug, p.Description)
	}
	return w.Flush()
}

// listAppsInProject renders all apps belonging to the named project. We
// deliberately do NOT call resolveProjectAppEnv here — its single-app
// auto-select would short-circuit into secrets listing instead of showing the
// app list when a project happens to contain exactly one app.
func listAppsInProject(vc *client.KagiClient, projectName string) error {
	proj, err := findProject(vc, projectName)
	if err != nil {
		return err
	}

	apps, err := vc.ListApps(proj.Slug)
	if err != nil {
		return fmt.Errorf("failed to list apps: %w", err)
	}

	if len(apps) == 0 {
		fmt.Println("No apps found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSLUG\tDESCRIPTION")
	for _, a := range apps {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.ID, a.Name, a.Slug, a.Description)
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

	if err := vc.SetSecrets(ctx.ProjectSlug, ctx.AppSlug, ctx.EnvID, secrets); err != nil {
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
	secretsList, err := vc.ListSecrets(ctx.ProjectSlug, ctx.AppSlug, ctx.EnvID)
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

	revealed, err := vc.GetSecret(ctx.ProjectSlug, ctx.AppSlug, ctx.EnvID, secretID)
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
	secretsList, err := vc.ListSecrets(ctx.ProjectSlug, ctx.AppSlug, ctx.EnvID)
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

	if err := vc.DeleteSecret(ctx.ProjectSlug, ctx.AppSlug, ctx.EnvID, secretID); err != nil {
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

	secrets, err := vc.ListSecrets(ctx.ProjectSlug, ctx.AppSlug, ctx.EnvID)
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
