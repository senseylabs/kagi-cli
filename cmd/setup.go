package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/senseylabs/kagi-cli/internal/ui"
	"github.com/spf13/cobra"
)

var (
	setupPath string
	setupEnv  string
	setupYes  bool
)

// errSetupAborted signals a clean, user-requested abort of interactive setup —
// 'q' at the folder browser or declining the overwrite prompt. runSetup treats
// it as a clean exit (nil), so the two interactive aborts behave identically
// instead of one returning nil and the other an error.
var errSetupAborted = errors.New("setup aborted")

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Bind the current directory to a Kagi app and environment",
	Long: "Resolve a secrets folder/app path to the app's stable internal ID and write a\n" +
		"kagi.yaml binding (app-id + environment) to the current directory. Addressing\n" +
		"thereafter uses the app ID, which survives app renames and folder moves.",
	Example: "  # Browse interactively for the app and environment\n" +
		"  kagi setup\n\n" +
		"  # Skip the interactive browser\n" +
		"  kagi setup --path /village/kaizen --env prod",
	Args: cobra.NoArgs,
	RunE: runSetup,
}

func init() {
	setupCmd.Flags().StringVarP(&setupPath, "path", "p", "", "Folder/app path, e.g. /village/kaizen (skip interactive browse)")
	setupCmd.Flags().StringVarP(&setupEnv, "env", "e", "", "Environment slug (skip interactive selection)")
	setupCmd.Flags().BoolVarP(&setupYes, "yes", "y", false, "Overwrite an existing kagi.yaml without prompting")
	rootCmd.AddCommand(setupCmd)
}

func runSetup(cmd *cobra.Command, args []string) error {
	u := newUI()

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
		folderPath, err = browseForApp(u, vc)
		if err != nil {
			// A user-requested abort ('q') is a clean exit, matching a declined
			// overwrite prompt — not an error.
			if errors.Is(err, errSetupAborted) {
				return nil
			}
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
		envSlug, err = selectEnvironment(u, envs)
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
	if _, statErr := os.Stat("kagi.yaml"); statErr == nil && !setupYes {
		// Confirm prints "Aborted." on a decline; a declined overwrite is a clean
		// exit, matching a 'q' abort in the browser.
		if !u.Confirm("Configuration kagi.yaml already exists. Overwrite?") {
			return nil
		}
	}

	if err := writeSetupConfig(folderPath, appID, envSlug); err != nil {
		return err
	}

	u.Success("configuration saved to kagi.yaml")
	u.Info("folder path: %s", folderPath)
	u.Info("app ID: %s", appID)
	u.Info("environment: %s", envSlug)
	return nil
}

// browseForApp walks the SECRETS folder tree interactively and returns the
// chosen app's full folder path. At each level the user descends into a child
// folder or picks an app; picking an app ends the walk. Folder paths are only
// used to resolve the app ID once — the returned path is informational.
func browseForApp(u *ui.UI, vc *client.KagiClient) (string, error) {
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

		// The interactive menu is human-facing, so it goes to stderr, keeping
		// stdout free of anything a script would consume.
		fmt.Fprintf(u.Err(), "\n%s\n", path)
		for i, e := range entries {
			kind := "folder/"
			if e.isApp {
				kind = "app"
			}
			fmt.Fprintf(u.Err(), "  %d. %s  (%s)\n", i+1, e.name, kind)
		}

		// Re-prompt on an invalid selection rather than aborting the whole setup.
		for {
			fmt.Fprint(u.Err(), "\nSelect a number (or 'q' to abort): ")

			input, err := reader.ReadString('\n')
			if err != nil {
				return "", fmt.Errorf("failed to read input: %w", err)
			}
			input = strings.TrimSpace(input)
			if strings.EqualFold(input, "q") {
				fmt.Fprintln(u.Err(), "Aborted.")
				return "", errSetupAborted
			}

			choice, err := strconv.Atoi(input)
			if err != nil || choice < 1 || choice > len(entries) {
				u.Warn("invalid selection: %s", input)
				continue
			}

			chosen := entries[choice-1]
			path = joinFolderPath(path, chosen.slug)
			if chosen.isApp {
				return path, nil
			}
			break // descended into a folder; re-list at the new path
		}
	}
}

// selectEnvironment prompts the user to choose an environment, auto-selecting
// when the app has exactly one.
func selectEnvironment(u *ui.UI, envs []client.Environment) (string, error) {
	if len(envs) == 1 {
		u.Info("auto-selected environment: %s (%s)", envs[0].Name, envs[0].Slug)
		return envs[0].Slug, nil
	}

	fmt.Fprintln(u.Err(), "\nSelect an environment:")
	for i, e := range envs {
		fmt.Fprintf(u.Err(), "  %d. %s (%s)\n", i+1, e.Name, e.Slug)
	}

	reader := bufio.NewReader(os.Stdin)
	// Re-prompt on an invalid selection rather than aborting.
	for {
		fmt.Fprint(u.Err(), "\nEnter number: ")

		input, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("failed to read input: %w", err)
		}
		input = strings.TrimSpace(input)

		choice, err := strconv.Atoi(input)
		if err != nil || choice < 1 || choice > len(envs) {
			u.Warn("invalid selection: %s", input)
			continue
		}
		return envs[choice-1].Slug, nil
	}
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
// (slug + UUID) is pinned when known so the binding is reproducible across
// sessions and directories.
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
