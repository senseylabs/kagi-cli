package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	kagi "github.com/senseylabs/kagi-sdk"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/spf13/cobra"
)

// personalEnvSlug is the reserved environment slug for a user's personal
// environment. The backend addresses it as a normal environment by this slug
// (GET/POST /kagi/apps/{appId}/environments/personal/secrets...). It is
// user-scoped: available to human (JWT) callers only, never to machine/CI
// (PAT) tokens. The --personal flag is sugar for --env personal.
const personalEnvSlug = "personal"

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

// resolveOpts tunes how resolveAppEnvWith behaves for a single call site.
//
// allowPersonalFallback opts a caller into the personal-environment fallback:
// when the user passes --personal but the target app has no personal
// environment, resolution falls back to the kagi.yaml environment (with a
// stderr warning) instead of failing. It is enabled ONLY for `run` and `pull`.
// The `secrets` subcommands leave it off: a silent fallback there would write
// to (or read/delete from) the shared environment every developer pulls, so
// they must keep the strict "environment not found" error.
type resolveOpts struct{ allowPersonalFallback bool }

// resolveAppEnv resolves the app ID and environment slug with the strict,
// no-fallback default. All `secrets` subcommands use this so a missing personal
// environment is a hard error, never a silent redirect to a shared environment.
func resolveAppEnv(cmd *cobra.Command, vc *client.KagiClient) (*resolvedContext, error) {
	return resolveAppEnvWith(cmd, vc, resolveOpts{})
}

// resolveAppEnvWith resolves the app ID and environment slug for a secret
// operation from flags first, then the kagi.yaml / ~/.kagi/config.yaml binding.
//
// Addressing is by app ID. A human-friendly --path (or the config folder-path)
// is resolved to the app ID once via the folder-browse route. The resolved app
// and environment are verified so callers get a distinguishing error: app does
// not exist / no access to app / environment does not exist.
func resolveAppEnvWith(cmd *cobra.Command, vc *client.KagiClient, opts resolveOpts) (*resolvedContext, error) {
	pathFlag, _ := cmd.Flags().GetString("path")
	appIDFlag, _ := cmd.Flags().GetString("app-id")
	envFlag, _ := cmd.Flags().GetString("env")
	personalFlag, _ := cmd.Flags().GetBool("personal")

	cfg := config.Load()

	// --personal is sugar for --env personal. It and an explicit --env are
	// mutually exclusive: allowing both only invites a silent conflict. The one
	// exception is --env personal, which names the same target.
	if personalFlag && envFlag != "" && !strings.EqualFold(envFlag, personalEnvSlug) {
		return nil, fmt.Errorf("use either --personal or --env, not both")
	}

	// Env-slug precedence: --personal (forces the personal slug) > --env >
	// kagi.yaml environment. Selection stays explicit here. The one exception is
	// the opt-in personal fallback (run/pull only, opts.allowPersonalFallback):
	// if --personal names a personal environment the app does not have, the
	// kagi.yaml environment is used instead with a warning (see chooseEnvSlug).
	// An explicit --env personal is never redirected; nor is any secrets call.
	envSlug := envFlag
	if personalFlag {
		envSlug = personalEnvSlug
	}
	if envSlug == "" {
		envSlug = cfg.Environment
	}
	if envSlug == "" {
		return nil, fmt.Errorf("environment not specified. Use --env flag or run 'kagi setup' to create a kagi.yaml")
	}

	// PAT guard: the personal environment is user-scoped and rejected by the
	// backend for machine/CI (PAT) tokens. Fail fast with an actionable message
	// before any network call, whether personal was requested via --personal or
	// --env personal. This stays a HARD ERROR even for run/pull — the personal
	// fallback below never applies to a PAT, so CI can never silently drift onto
	// a shared environment.
	if vc.IsPAT() && strings.EqualFold(envSlug, personalEnvSlug) {
		return nil, fmt.Errorf("personal secrets are user-scoped and not available to machine/CI (PAT) tokens; run with a user login ('kagi login') or select a shared environment")
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

	envSlug, warning, err := chooseEnvSlug(envSlug, personalFlag, opts.allowPersonalFallback, cfg.Environment, envs, appLabel(folderPath, appID))
	if err != nil {
		return nil, err
	}
	if warning != "" {
		// Warnings go to stderr, never stdout: `kagi pull` streams KEY=VALUE to
		// stdout for consumers to parse/eval, and a warning there would corrupt it.
		fmt.Fprintf(os.Stderr, "warning: %s\n", warning)
	}

	return &resolvedContext{
		AppID:      appID,
		EnvSlug:    envSlug,
		FolderPath: folderPath,
	}, nil
}

// chooseEnvSlug picks the environment to use, given what the user asked for and
// what the app actually has. Returns the chosen canonical slug and, when a
// fallback occurred, a non-empty warning to surface on stderr.
//
// Rules, applied in order:
//  1. requested matches an available slug case-insensitively -> return the
//     canonical slug from the API (preserving the API's normalization), no
//     warning.
//  2. else, when a fallback is permitted (run/pull) AND --personal was the
//     source of the request AND the kagi.yaml environment is set and itself
//     matches an available slug -> return that canonical slug plus a warning.
//  3. else -> today's strict "not found" error, verbatim, reporting the
//     originally requested slug (so it still reads "personal").
//
// The fallback is gated on personalFlag, NOT on requested == personalEnvSlug:
// someone who typed --env personal explicitly named a target and must get the
// strict error, never a silent redirect.
//
// appLabel is the human-readable app identifier woven into the warning and the
// strict errors so both stay verbatim; it is not a decision input.
func chooseEnvSlug(
	requested string,
	personalFlag bool,
	allowFallback bool,
	configEnv string,
	available []client.Environment,
	appLabel string,
) (slug string, warning string, err error) {
	// Rule 1: exact (case-insensitive) match wins, normalized to the API's slug.
	if canonical, ok := matchEnvSlug(requested, available); ok {
		return canonical, "", nil
	}

	// Rule 2: opt-in personal fallback to the kagi.yaml environment.
	if allowFallback && personalFlag && configEnv != "" {
		if canonical, ok := matchEnvSlug(configEnv, available); ok {
			warning = fmt.Sprintf(
				"app %s has no %q environment; falling back to %q from kagi.yaml",
				appLabel, personalEnvSlug, canonical)
			return canonical, warning, nil
		}
	}

	// Rule 3: strict errors, reporting the originally requested slug.
	if len(available) == 0 {
		return "", "", fmt.Errorf("environment %q not found: app %s has no environments", requested, appLabel)
	}
	slugs := make([]string, len(available))
	for i, e := range available {
		slugs[i] = e.Slug
	}
	return "", "", fmt.Errorf("environment %q not found in app %s. Available: %s", requested, appLabel, strings.Join(slugs, ", "))
}

// matchEnvSlug looks up want among the available environments case-insensitively
// and returns the canonical slug (the API's casing) when found.
func matchEnvSlug(want string, available []client.Environment) (string, bool) {
	for _, e := range available {
		if strings.EqualFold(e.Slug, want) {
			return e.Slug, true
		}
	}
	return "", false
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
//	--path      human folder/app path, resolved once to the app ID
//	--app-id    the app's stable internal ID (skips path resolution)
//	--env       environment slug
//	--personal  sugar for --env personal (user login only, not PAT)
//
// All override the kagi.yaml binding for the single invocation.
func addSecretFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("path", "p", "", "Folder/app path, e.g. /village/kaizen (overrides kagi.yaml)")
	cmd.Flags().String("app-id", "", "App ID — the stable machine binding (overrides --path and kagi.yaml)")
	cmd.Flags().StringP("env", "e", "", "Environment slug (overrides kagi.yaml)")
	cmd.Flags().Bool("personal", false, "Target your personal environment (sugar for --env personal; requires a user login, not a PAT)")
}
