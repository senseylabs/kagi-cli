package cmd

import (
	"errors"
	"fmt"
	"strings"

	kagi "github.com/senseylabs/kagi-sdk"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/spf13/cobra"
)

// resolvedContext holds the resolved folder-model addressing for a secret
// operation: the app's stable internal ID and the environment slug. FolderPath
// is carried for human-readable error/output messages only.
type resolvedContext struct {
	AppID      string
	EnvSlug    string
	FolderPath string
}

// upgradeErr is the actionable error shown when a config still uses the legacy
// project/app binding (no app ID). The CLI never silently falls back — the user
// must re-run setup to capture the stable app ID under the folder model.
var upgradeErr = errors.New(
	"this kagi.yaml uses the legacy project/app model, which is no longer supported.\n" +
		"  Re-run 'kagi setup' to bind to a folder path + environment and capture the app ID,\n" +
		"  or set the binding explicitly with --path <folder/app path> --env <env> (or --app-id <id> --env <env>)")

// resolveAppEnv resolves the app ID and environment slug for a secret operation
// from flags first, then the kagi.yaml / ~/.kagi/config.yaml binding.
//
// Addressing is by app ID. A human-friendly --path (or the config folder-path)
// is resolved to the app ID once via the folder-browse route. The resolved app
// and environment are verified so callers get a distinguishing error: app does
// not exist / no access to app / environment does not exist.
func resolveAppEnv(cmd *cobra.Command, vc *client.KagiClient) (*resolvedContext, error) {
	pathFlag, _ := cmd.Flags().GetString("path")
	appIDFlag, _ := cmd.Flags().GetString("app-id")
	envFlag, _ := cmd.Flags().GetString("env")

	cfg := config.Load()

	envSlug := envFlag
	if envSlug == "" {
		envSlug = cfg.Environment
	}
	if envSlug == "" {
		return nil, fmt.Errorf("environment not specified. Use --env flag or run 'kagi setup' to create a kagi.yaml")
	}

	// Resolve the app ID. Priority: explicit --app-id, then --path resolution,
	// then the config binding (app-id, or folder-path resolved once).
	appID := appIDFlag
	folderPath := pathFlag
	switch {
	case appID != "":
		// Explicit app ID — nothing to resolve. folderPath stays as the flag (may
		// be empty) and is only used for messages.
	case pathFlag != "":
		resolved, err := vc.ResolveApp(pathFlag)
		if err != nil {
			return nil, classifyAppError(err, pathFlag)
		}
		appID = resolved
	case cfg.AppID != "":
		appID = cfg.AppID
		folderPath = cfg.FolderPath
	case cfg.IsLegacy():
		return nil, upgradeErr
	default:
		return nil, fmt.Errorf("app not specified. Use --path <folder/app path> or --app-id <id>, or run 'kagi setup' to create a kagi.yaml")
	}

	// Verify the app is reachable and the environment exists, so the caller gets
	// a precise error instead of an opaque failure on the secrets call.
	envs, err := vc.ListEnvironments(appID)
	if err != nil {
		return nil, classifyAppError(err, appLabel(folderPath, appID))
	}

	found := false
	available := make([]string, 0, len(envs))
	for _, e := range envs {
		available = append(available, e.Slug)
		if strings.EqualFold(e.Slug, envSlug) {
			envSlug = e.Slug
			found = true
		}
	}
	if !found {
		if len(available) == 0 {
			return nil, fmt.Errorf("environment %q not found: app %s has no environments", envSlug, appLabel(folderPath, appID))
		}
		return nil, fmt.Errorf("environment %q not found in app %s. Available: %s", envSlug, appLabel(folderPath, appID), strings.Join(available, ", "))
	}

	return &resolvedContext{
		AppID:      appID,
		EnvSlug:    envSlug,
		FolderPath: folderPath,
	}, nil
}

// resolveAppOnly resolves just the app ID (no environment) from flags then the
// kagi.yaml binding, returning the ID and a human-readable label for messages.
// It is used by commands that operate at the app level, e.g. listing
// environments.
func resolveAppOnly(cmd *cobra.Command, vc *client.KagiClient) (string, string, error) {
	pathFlag, _ := cmd.Flags().GetString("path")
	appIDFlag, _ := cmd.Flags().GetString("app-id")

	cfg := config.Load()

	switch {
	case appIDFlag != "":
		return appIDFlag, appLabel(pathFlag, appIDFlag), nil
	case pathFlag != "":
		appID, err := vc.ResolveApp(pathFlag)
		if err != nil {
			return "", "", classifyAppError(err, pathFlag)
		}
		return appID, appLabel(pathFlag, appID), nil
	case cfg.AppID != "":
		return cfg.AppID, appLabel(cfg.FolderPath, cfg.AppID), nil
	case cfg.IsLegacy():
		return "", "", upgradeErr
	default:
		return "", "", fmt.Errorf("app not specified. Use --path <folder/app path> or --app-id <id>, or run 'kagi setup' to create a kagi.yaml")
	}
}

// appLabel renders the most human-meaningful identifier available for an app:
// the folder path when known, otherwise the bare app ID.
func appLabel(folderPath, appID string) string {
	if folderPath != "" {
		return fmt.Sprintf("%q (%s)", folderPath, appID)
	}
	return appID
}

// classifyAppError turns a folder/app resolution or lookup error into a message
// that distinguishes "app does not exist" from "no access to app", per the
// folder-model error-reporting requirement. The underlying error is always
// preserved (wrapped) — failures are never swallowed.
func classifyAppError(err error, label string) error {
	switch {
	case errors.Is(err, kagi.ErrAppNotFound):
		return fmt.Errorf("app not found at path %s. Browse with 'kagi secrets [path]' to see available apps", label)
	case isStatus(err, 403):
		return fmt.Errorf("no access to app %s. You may not have permission, or it belongs to another organization: %w", label, err)
	case isStatus(err, 404):
		return fmt.Errorf("app %s not found. Check the path or app ID: %w", label, err)
	default:
		return fmt.Errorf("failed to resolve app %s: %w", label, err)
	}
}

// isStatus reports whether the SDK error carries the given HTTP status. The SDK
// formats non-2xx responses as "kagi: API returned status N: ...", so a
// substring check is sufficient and keeps the SDK surface small.
func isStatus(err error, status int) bool {
	return err != nil && strings.Contains(err.Error(), fmt.Sprintf("status %d", status))
}

// addSecretFlags adds the folder-model addressing flags to a command:
//
//	--path     human folder/app path, resolved once to the app ID
//	--app-id   the app's stable internal ID (skips path resolution)
//	--env      environment slug
//
// All three override the kagi.yaml binding for the single invocation.
func addSecretFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("path", "p", "", "Folder/app path, e.g. /village/kaizen (overrides kagi.yaml)")
	cmd.Flags().String("app-id", "", "App ID — the stable machine binding (overrides --path and kagi.yaml)")
	cmd.Flags().StringP("env", "e", "", "Environment slug (overrides kagi.yaml)")
}
