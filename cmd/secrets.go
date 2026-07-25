package cmd

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/ui"
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
	Use:               "get <KEY>",
	Short:             "Get a single secret (decrypted)",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSecretKeys,
	RunE:              runSecretGet,
}

var secretDeleteYes bool

var secretDeleteCmd = &cobra.Command{
	Use:               "delete <KEY>",
	Short:             "Delete a secret",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSecretKeys,
	RunE:              runSecretDelete,
}

var secretListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all secrets (masked)",
	Args:  cobra.NoArgs,
	RunE:  runSecretList,
}

var secretEnvsCmd = &cobra.Command{
	Use:   "envs",
	Short: "List environments for an app",
	Args:  cobra.NoArgs,
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

// completeSecretKeys offers the secret KEYs of the resolved app/environment for
// `secrets get <KEY>` / `secrets delete <KEY>` shell completion, reusing the
// folder/app resolution and the ListSecrets call. Any failure (unauthenticated,
// unresolvable app, network) yields no suggestions rather than an error.
func completeSecretKeys(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
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
	ctx, err := resolveAppEnv(cmd, vc)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	secrets, err := vc.ListSecrets(ctx.AppID, ctx.EnvSlug)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	keys := make([]string, 0, len(secrets))
	for _, s := range secrets {
		keys = append(keys, s.KeyName)
	}
	return keys, cobra.ShellCompDirectiveNoFileComp
}

// runSecretsBrowse handles bare `kagi secrets [path]` — it browses the SECRETS
// folder tree at the given path (root when omitted), listing child folders and
// the apps directly under it. Apps carry their stable ID, which is what setup
// captures and what addresses secrets.
func runSecretsBrowse(cmd *cobra.Command, args []string) error {
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

	// Bare `kagi secrets` on a terminal (table output, no path arg) drops into the
	// read-only drill-down picker. Every scripted/non-table/path-given invocation
	// keeps the one-shot listing below untouched.
	if shouldBrowseInteractively(cmd, args) {
		return runSecretsInteractiveBrowse(u, vc)
	}

	path := "/"
	if len(args) == 1 {
		path = args[0]
	}

	children, err := vc.ListFolderChildren(path)
	if err != nil {
		return fmt.Errorf("failed to browse %q: %w", path, err)
	}

	if len(children.Folders) == 0 && len(children.Apps) == 0 && format == ui.FormatTable {
		u.Info("No folders or apps under %q.", path)
		return nil
	}

	// APP ID is a UUID — the least human-meaningful column, so it yields width
	// first while the name and slug stay readable.
	table := ui.NewTable("TYPE", "NAME", "SLUG", "APP ID").
		SetTruncatable(3, 0)
	for _, f := range children.Folders {
		table.AddRow("folder", f.Name, f.Slug, "")
	}
	for _, a := range children.Apps {
		table.AddRow("app", a.Name, a.Slug, a.ID)
	}

	return u.Print(format, children, table)
}

// runSecretsInteractiveBrowse drives the read-only drill-down for a bare
// `kagi secrets` on a terminal. It walks the SECRETS folder tree via
// ListFolderChildren (folders plus app leaves), and on selecting an app shows
// that app's environments so the user can pick one and view its masked secrets —
// values are never revealed. It also prints the exact one-shot command to act on
// the chosen app. Everything here is read-only.
func runSecretsInteractiveBrowse(u *ui.UI, vc *client.KagiClient) error {
	// appIDByPath maps a leaf's display path to the app's stable ID, captured
	// while listing so onLeaf can address the app without re-resolving the path.
	appIDByPath := map[string]string{}

	listChildren := func(path string) (folders []BrowseNode, leaves []BrowseNode, err error) {
		children, err := vc.ListFolderChildren(path)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to browse %q: %w", path, err)
		}
		folders = make([]BrowseNode, 0, len(children.Folders))
		for _, f := range children.Folders {
			folders = append(folders, BrowseNode{
				Name: f.Slug,
				Path: secretsChildPath(path, f.Slug),
			})
		}
		leaves = make([]BrowseNode, 0, len(children.Apps))
		for _, a := range children.Apps {
			appPath := secretsChildPath(path, a.Slug)
			appIDByPath[appPath] = a.ID
			leaves = append(leaves, BrowseNode{
				Name:      a.Slug,
				Path:      appPath,
				Label:     a.Name,
				Secondary: a.ID,
			})
		}
		return folders, leaves, nil
	}

	onLeaf := func(path string, _ BrowseNode) error {
		return browseSecretsApp(u, vc, path, appIDByPath[path])
	}

	return runInteractiveBrowse(u, "/", listChildren, onLeaf)
}

// browseSecretsApp is the leaf action of the secrets browser: it lists the app's
// environments, lets the user pick one, and prints that environment's masked
// secrets (reusing the same masked-list shape as `secrets list`). It always
// echoes the one-shot command to act on the app. Selecting nothing (go up / quit)
// simply returns to the folder listing. Read-only throughout.
func browseSecretsApp(u *ui.UI, vc *client.KagiClient, appPath, appID string) error {
	label := appLabel(appPath, appID)

	envs, err := vc.ListEnvironments(appID)
	if err != nil {
		return classifyAppError(err, label)
	}

	u.Info("App %s", label)
	u.Info("Act on it with: kagi secrets list -p %s -e <env>", appPath)

	if len(envs) == 0 {
		u.Info("No environments for this app.")
		return nil
	}

	items := make([]ui.PickItem, 0, len(envs))
	for _, e := range envs {
		items = append(items, ui.PickItem{
			Label:     e.Slug,
			Secondary: e.Name,
			Value:     e,
		})
	}

	res, err := u.Pick(appPath+" — pick an environment", items, ui.PickOptions{AllowUp: true})
	if err != nil {
		return err
	}
	if res.Kind != ui.PickSelected {
		return nil
	}
	env := res.Item.Value.(client.Environment)

	secrets, err := vc.ListSecrets(appID, env.Slug)
	if err != nil {
		return fmt.Errorf("failed to list secrets: %w", err)
	}

	u.Info("Masked secrets for %s -e %s (view with: kagi secrets get <KEY> -p %s -e %s)",
		appPath, env.Slug, appPath, env.Slug)

	if len(secrets) == 0 {
		u.Info("No secrets in %s -e %s.", appPath, env.Slug)
		return nil
	}

	table := ui.NewTable("KEY", "VALUE", "UPDATED").
		SetTruncatable(1, 0)
	for _, s := range secrets {
		table.AddRow(s.KeyName, s.MaskedValue, s.UpdatedAt)
	}
	return u.Render(table)
}

// secretsChildPath joins a browse parent path and a child slug into the "/"-
// rooted path form used throughout the secrets browser (matching browseParent).
func secretsChildPath(parent, slug string) string {
	if parent == "" || parent == "/" {
		return "/" + slug
	}
	return strings.TrimRight(parent, "/") + "/" + slug
}

func runSecretEnvs(cmd *cobra.Command, args []string) error {
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

	appID, label, err := resolveAppOnly(cmd, vc)
	if err != nil {
		return err
	}

	envs, err := vc.ListEnvironments(appID)
	if err != nil {
		return classifyAppError(err, label)
	}

	if len(envs) == 0 && format == ui.FormatTable {
		u.Info("No environments found for app %s.", label)
		return nil
	}

	table := ui.NewTable("ID", "NAME", "SLUG").
		SetTruncatable(0, 0)
	for _, e := range envs {
		table.AddRow(e.ID, e.Name, e.Slug)
	}

	return u.Print(format, envs, table)
}

func runSecretSet(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	u := newUI()

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

	u.Success("Set %d secret(s).", len(secrets))
	return nil
}

func runSecretGet(cmd *cobra.Command, args []string) error {
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

	// The table view of a single secret is just its value — the payload a script
	// consuming `kagi secrets get KEY` expects on stdout. JSON/YAML emit the full
	// revealed record (id, keyName, value).
	if format == ui.FormatTable {
		u.Data(revealed.Value)
		return nil
	}
	return u.Print(format, revealed, nil)
}

func runSecretDelete(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	u := newUI()

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
		if !u.Confirm(fmt.Sprintf("Are you sure you want to delete secret %q?", keyName)) {
			u.Info("Aborted.")
			return nil
		}
	}

	if err := vc.DeleteSecret(ctx.AppID, ctx.EnvSlug, secretID); err != nil {
		return fmt.Errorf("failed to delete secret: %w", err)
	}

	u.Success("Deleted secret %q.", keyName)
	return nil
}

func runSecretList(cmd *cobra.Command, args []string) error {
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

	ctx, err := resolveAppEnv(cmd, vc)
	if err != nil {
		return err
	}

	secrets, err := vc.ListSecrets(ctx.AppID, ctx.EnvSlug)
	if err != nil {
		return fmt.Errorf("failed to list secrets: %w", err)
	}

	if len(secrets) == 0 && format == ui.FormatTable {
		u.Info("No secrets found.")
		return nil
	}

	table := ui.NewTable("KEY", "VALUE", "UPDATED").
		SetTruncatable(1, 0)
	for _, s := range secrets {
		table.AddRow(s.KeyName, s.MaskedValue, s.UpdatedAt)
	}

	return u.Print(format, secrets, table)
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
