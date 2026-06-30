package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/spf13/cobra"
)

var (
	setupPath  string
	setupEnv   string
	setupForce bool
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Bind the current directory to a Kagi app and environment",
	Long: "Resolve a secrets folder/app path to the app's stable internal ID and write a\n" +
		"kagi.yaml binding (app-id + environment) to the current directory. Addressing\n" +
		"thereafter uses the app ID, which survives app renames and folder moves.\n\n" +
		"Pass --path and --env to skip the interactive folder browser.",
	RunE: runSetup,
}

func init() {
	setupCmd.Flags().StringVarP(&setupPath, "path", "p", "", "Folder/app path, e.g. /village/kaizen (skip interactive browse)")
	setupCmd.Flags().StringVarP(&setupEnv, "env", "e", "", "Environment slug (skip interactive selection)")
	setupCmd.Flags().BoolVarP(&setupForce, "force", "f", false, "Overwrite existing kagi.yaml")
	rootCmd.AddCommand(setupCmd)
}

func runSetup(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	// Step 1: resolve the folder/app path — from --path or by browsing the tree.
	folderPath := setupPath
	if folderPath == "" {
		folderPath, err = browseForApp(vc)
		if err != nil {
			return err
		}
	}

	// Step 2: resolve the path to the app's stable ID — the durable binding the
	// config stores. Path resolution happens once, here, never on every command.
	appID, err := vc.ResolveApp(folderPath)
	if err != nil {
		return classifyAppError(err, folderPath)
	}

	// Step 3: resolve the environment — from --env or by selecting from the app's.
	envs, err := vc.ListEnvironments(appID)
	if err != nil {
		return classifyAppError(err, appLabel(folderPath, appID))
	}
	if len(envs) == 0 {
		return fmt.Errorf("app %s has no environments", appLabel(folderPath, appID))
	}

	envSlug := setupEnv
	if envSlug == "" {
		envSlug, err = selectEnvironment(envs)
		if err != nil {
			return err
		}
	} else {
		matched := ""
		available := make([]string, 0, len(envs))
		for _, e := range envs {
			available = append(available, e.Slug)
			if strings.EqualFold(e.Slug, envSlug) {
				matched = e.Slug
			}
		}
		if matched == "" {
			return fmt.Errorf("environment %q not found in app %s. Available: %s", envSlug, appLabel(folderPath, appID), strings.Join(available, ", "))
		}
		envSlug = matched
	}

	// Step 4: write kagi.yaml.
	if _, statErr := os.Stat("kagi.yaml"); statErr == nil && !setupForce {
		fmt.Print("Configuration kagi.yaml already exists. Overwrite? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(input)) != "y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if err := writeSetupConfig(folderPath, appID, envSlug); err != nil {
		return err
	}

	fmt.Printf("Configuration saved to kagi.yaml.\n  Folder path: %s\n  App ID:      %s\n  Environment: %s\n", folderPath, appID, envSlug)
	return nil
}

// browseForApp walks the SECRETS folder tree interactively and returns the
// chosen app's full folder path. At each level the user descends into a child
// folder or picks an app; picking an app ends the walk. Folder paths are only
// used to resolve the app ID once — the returned path is informational.
func browseForApp(vc *client.KagiClient) (string, error) {
	reader := bufio.NewReader(os.Stdin)
	path := "/"

	for {
		children, err := vc.ListFolderChildren(path)
		if err != nil {
			return "", fmt.Errorf("failed to browse %q: %w", path, err)
		}

		if len(children.Folders) == 0 && len(children.Apps) == 0 {
			return "", fmt.Errorf("no folders or apps under %q. Create an app in the Kagi web app first", path)
		}

		// One combined numbered list: folders first (to descend into), then apps.
		type entry struct {
			isApp bool
			name  string
			slug  string
		}
		entries := make([]entry, 0, len(children.Folders)+len(children.Apps))
		for _, f := range children.Folders {
			entries = append(entries, entry{isApp: false, name: f.Name, slug: f.Slug})
		}
		for _, a := range children.Apps {
			entries = append(entries, entry{isApp: true, name: a.Name, slug: a.Slug})
		}

		fmt.Printf("\n%s\n", path)
		for i, e := range entries {
			kind := "folder/"
			if e.isApp {
				kind = "app"
			}
			fmt.Printf("  %d. %s  (%s)\n", i+1, e.name, kind)
		}
		fmt.Print("\nSelect a number (or 'q' to abort): ")

		input, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("failed to read input: %w", err)
		}
		input = strings.TrimSpace(input)
		if strings.EqualFold(input, "q") {
			return "", fmt.Errorf("setup aborted")
		}

		choice, err := strconv.Atoi(input)
		if err != nil || choice < 1 || choice > len(entries) {
			return "", fmt.Errorf("invalid selection: %s", input)
		}

		chosen := entries[choice-1]
		path = joinFolderPath(path, chosen.slug)
		if chosen.isApp {
			return path, nil
		}
	}
}

// selectEnvironment prompts the user to choose an environment, auto-selecting
// when the app has exactly one.
func selectEnvironment(envs []client.Environment) (string, error) {
	if len(envs) == 1 {
		fmt.Printf("\nAuto-selected environment: %s (%s)\n", envs[0].Name, envs[0].Slug)
		return envs[0].Slug, nil
	}

	fmt.Println("\nSelect an environment:")
	for i, e := range envs {
		fmt.Printf("  %d. %s (%s)\n", i+1, e.Name, e.Slug)
	}
	fmt.Print("\nEnter number: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read input: %w", err)
	}
	input = strings.TrimSpace(input)

	choice, err := strconv.Atoi(input)
	if err != nil || choice < 1 || choice > len(envs) {
		return "", fmt.Errorf("invalid selection: %s", input)
	}
	return envs[choice-1].Slug, nil
}

// joinFolderPath appends a slug to a folder path, yielding an absolute,
// single-slash-separated path (e.g. "/" + "village" -> "/village").
func joinFolderPath(base, slug string) string {
	if base == "" || base == "/" {
		return "/" + slug
	}
	return strings.TrimRight(base, "/") + "/" + slug
}

// writeSetupConfig writes the folder-model kagi.yaml binding. Addressing uses
// the stable app-id; folder-path is informational only. The active organization
// (slug + UUID) is pinned when known so the binding is reproducible under JWT
// auth — under a KAGI_TOKEN PAT the org is bound to the token and is ignored.
func writeSetupConfig(folderPath, appID, envSlug string) error {
	cfg := config.Load()

	var sb strings.Builder
	sb.WriteString("# Kagi binding for this directory. Secrets are addressed by the stable\n")
	sb.WriteString("# app-id; folder-path is a human reference only and is not used for addressing.\n")
	sb.WriteString(fmt.Sprintf("folder-path: %s\n", folderPath))
	sb.WriteString(fmt.Sprintf("app-id: %s\n", appID))
	sb.WriteString(fmt.Sprintf("environment: %s\n", envSlug))
	if cfg.Organization != "" {
		sb.WriteString(fmt.Sprintf("organization: %s\n", cfg.Organization))
	}
	if cfg.OrganizationID != "" {
		sb.WriteString(fmt.Sprintf("organization-id: %s\n", cfg.OrganizationID))
	}

	if err := os.WriteFile("kagi.yaml", []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("failed to write kagi.yaml: %w", err)
	}
	return nil
}
