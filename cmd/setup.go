package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/senseylabs/kagi-cli/internal/ui"
)

var (
	setupPath string
	setupEnv  string
	setupYes  bool
)

// errSetupAborted signals a clean, user-requested abort of interactive setup —
// quitting the folder browser / environment picker or declining the overwrite
// prompt. runSetup treats it as a clean exit (nil), so every interactive abort
// behaves identically instead of some returning nil and others an error.
var errSetupAborted = errors.New("setup aborted")

// errBrowseSelected is an internal sentinel: browseForApp's onLeaf returns it to
// stop the shared browse loop the moment an app is chosen (the loop otherwise
// keeps browsing after a leaf). It never escapes browseForApp.
var errBrowseSelected = errors.New("app selected")

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
			// overwrite prompt — not an error. Echo it so a quit isn't silent (the
			// picker erases its own screen region on the way out).
			if errors.Is(err, errSetupAborted) {
				u.Info("Aborted")
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
			// Quitting the environment picker is a clean exit, like a 'q' in the
			// folder browser or a declined overwrite.
			if errors.Is(err, errSetupAborted) {
				u.Info("Aborted")
				return nil
			}
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

// browseForApp walks the SECRETS folder tree interactively (via the shared
// vim-style picker) and returns the chosen app's full folder path. At each level
// the user descends into a child folder or picks an app; picking an app ends the
// walk. Folder paths are only used to resolve the app ID once — the returned
// path is informational.
func browseForApp(u *ui.UI, vc *client.KagiClient) (string, error) {
	var selected string

	listChildren := func(path string) (folders, leaves []BrowseNode, err error) {
		children, err := vc.ListFolderChildren(path)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to browse %q: %w", path, err)
		}
		// An empty library root is a real error worth explaining; an empty
		// subfolder just lets the user navigate back up (the picker renders it as
		// "(no matches)" with the go-up key available).
		if len(children.Folders) == 0 && len(children.Apps) == 0 && isBrowseRoot(path) {
			return nil, nil, fmt.Errorf("no folders or apps under %q. Create an app in the Kagi web app first", path)
		}
		for _, f := range children.Folders {
			folders = append(folders, BrowseNode{Name: f.Name, Path: joinFolderPath(path, f.Slug)})
		}
		for _, a := range children.Apps {
			// Secondary is the app's stable ID (matching secrets browse): it
			// disambiguates same-named apps and, being unique, keeps filtering by
			// name meaningful — unlike a constant "app" tag, which every app shared
			// so any letter of it matched every row.
			leaves = append(leaves, BrowseNode{Name: a.Name, Path: joinFolderPath(path, a.Slug), Secondary: a.ID})
		}
		return folders, leaves, nil
	}

	// Choosing an app (a leaf) captures its path and stops the browse loop via a
	// sentinel; folders are descended into automatically by runInteractiveBrowse.
	onLeaf := func(path string, _ BrowseNode) error {
		selected = path
		return errBrowseSelected
	}

	switch err := runInteractiveBrowse(u, "/", listChildren, onLeaf); {
	case errors.Is(err, errBrowseSelected):
		return selected, nil
	case err != nil:
		return "", err
	case !u.Interactive():
		// Non-interactive (piped/redirected) with no selection means the input ran
		// out, not a deliberate quit — fail loudly rather than exit 0 doing nothing.
		return "", fmt.Errorf("no app selected; pass --path (and --env) for non-interactive setup")
	default:
		return "", errSetupAborted // interactive user quit without choosing an app
	}
}

// selectEnvironment prompts the user to choose an environment via the picker,
// auto-selecting when the app has exactly one. A quit returns errSetupAborted.
func selectEnvironment(u *ui.UI, envs []client.Environment) (string, error) {
	if len(envs) == 1 {
		u.Info("auto-selected environment: %s (%s)", envs[0].Name, envs[0].Slug)
		return envs[0].Slug, nil
	}

	items := make([]ui.PickItem, 0, len(envs))
	for _, e := range envs {
		label := e.Name
		if label == "" {
			label = e.Slug
		}
		items = append(items, ui.PickItem{Label: label, Secondary: e.Slug, Value: e.Slug})
	}

	res, err := u.Pick("Select an environment", items, ui.PickOptions{})
	if err != nil {
		return "", err
	}
	if res.Kind != ui.PickSelected {
		if !u.Interactive() {
			return "", fmt.Errorf("no environment selected; pass --env for non-interactive setup")
		}
		return "", errSetupAborted
	}
	return res.Item.Value.(string), nil
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
// sessions and directories under a 'kagi login' session — under a KAGI_TOKEN
// access token the org is bound to the token and the pinned value is ignored.
func writeSetupConfig(folderPath, appID, envSlug string) error {
	// Pin the organization from the account-level (home) selection, NOT the merged
	// config. config.Load() folds in the cwd kagi.yaml we are about to overwrite,
	// so a stale org pinned there would be re-pinned here — and under strict
	// tenancy that stale org 403s every subsequent setup step with no way to clear
	// it. HomeOrganization() reflects the true active selection instead.
	orgSlug, orgID := config.HomeOrganization()

	var sb strings.Builder
	sb.WriteString("# Kagi binding for this directory. Secrets are addressed by the stable\n")
	sb.WriteString("# app-id; folder-path is a human reference only and is not used for addressing.\n")
	fmt.Fprintf(&sb, "folder-path: %s\n", folderPath)
	fmt.Fprintf(&sb, "app-id: %s\n", appID)
	fmt.Fprintf(&sb, "environment: %s\n", envSlug)
	if orgSlug != "" {
		fmt.Fprintf(&sb, "organization: %s\n", orgSlug)
	}
	if orgID != "" {
		fmt.Fprintf(&sb, "organization-id: %s\n", orgID)
	}

	if err := os.WriteFile("kagi.yaml", []byte(sb.String()), 0644); err != nil { //nolint:gosec // kagi.yaml is a non-secret, per-project binding (folder path, app ID, org) meant to be committed and shared; 0644 is intentional
		return fmt.Errorf("failed to write kagi.yaml: %w", err)
	}
	return nil
}
