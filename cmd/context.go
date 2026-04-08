package cmd

import (
	"fmt"
	"strings"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/spf13/cobra"
)

// resolvedContext holds the resolved project, app, and environment IDs.
type resolvedContext struct {
	ProjectName string
	ProjectID   string
	AppName     string
	AppID       string
	EnvSlug     string
	EnvID       string
}

// resolveProjectAppEnv resolves project, app, and environment from flags, kagi.yaml, or returns an error.
// It uses the API to map project name → ID, app name → ID, and env slug → env ID.
// If a project has exactly one app and --app is not specified, it auto-selects that app.
func resolveProjectAppEnv(cmd *cobra.Command, vc *client.KagiClient) (*resolvedContext, error) {
	projectName, _ := cmd.Flags().GetString("project")
	appName, _ := cmd.Flags().GetString("app")
	envSlug, _ := cmd.Flags().GetString("env")

	// Fall back to kagi.yaml config
	if projectName == "" || appName == "" || envSlug == "" {
		cfg := config.Load()
		if projectName == "" {
			projectName = cfg.Project
		}
		if appName == "" {
			appName = cfg.App
		}
		if envSlug == "" {
			envSlug = cfg.Environment
		}
	}

	if projectName == "" {
		return nil, fmt.Errorf("project not specified. Use --project flag or run 'kagi setup' to create a kagi.yaml")
	}
	if envSlug == "" {
		return nil, fmt.Errorf("environment not specified. Use --env flag or run 'kagi setup' to create a kagi.yaml")
	}

	// Resolve project name → ID
	projects, err := vc.ListProjects()
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}

	var projectID string
	for _, p := range projects {
		if strings.EqualFold(p.Name, projectName) {
			projectID = p.ID
			break
		}
	}
	if projectID == "" {
		return nil, fmt.Errorf("project %q not found", projectName)
	}

	// Resolve app name → ID
	apps, err := vc.ListApps(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list apps: %w", err)
	}

	var appID string
	if appName == "" {
		// Auto-select if project has exactly one app
		if len(apps) == 1 {
			appName = apps[0].Name
			appID = apps[0].ID
		} else if len(apps) == 0 {
			return nil, fmt.Errorf("no apps found in project %q. Create one with 'kagi app create'", projectName)
		} else {
			return nil, fmt.Errorf("app not specified. Use --app flag or run 'kagi setup' to create a kagi.yaml")
		}
	} else {
		for _, a := range apps {
			if strings.EqualFold(a.Name, appName) {
				appID = a.ID
				break
			}
		}
		if appID == "" {
			return nil, fmt.Errorf("app %q not found in project %q", appName, projectName)
		}
	}

	// Resolve env slug → ID
	envs, err := vc.ListEnvironments(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list environments: %w", err)
	}

	var envID string
	for _, e := range envs {
		if strings.EqualFold(e.Slug, envSlug) {
			envID = e.ID
			break
		}
	}
	if envID == "" {
		return nil, fmt.Errorf("environment %q not found in project %q", envSlug, projectName)
	}

	return &resolvedContext{
		ProjectName: projectName,
		ProjectID:   projectID,
		AppName:     appName,
		AppID:       appID,
		EnvSlug:     envSlug,
		EnvID:       envID,
	}, nil
}

// addProjectAppEnvFlags adds --project, --app, and --env flags to a command.
func addProjectAppEnvFlags(cmd *cobra.Command) {
	cmd.Flags().StringP("project", "p", "", "Project name (overrides kagi.yaml)")
	cmd.Flags().StringP("app", "a", "", "App name (overrides kagi.yaml)")
	cmd.Flags().StringP("env", "e", "", "Environment slug (overrides kagi.yaml)")
}
