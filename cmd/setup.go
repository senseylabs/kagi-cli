package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/spf13/cobra"
)

var (
	setupProject string
	setupApp     string
	setupEnv     string
	setupForce   bool
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Set up project, app, and environment for the current directory",
	Long:  "Interactively select a project, app, and environment, then write a kagi.yaml config to the current directory.",
	RunE:  runSetup,
}

func init() {
	setupCmd.Flags().StringVarP(&setupProject, "project", "p", "", "Project name (skip interactive selection)")
	setupCmd.Flags().StringVarP(&setupApp, "app", "a", "", "App name (skip interactive selection)")
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

	// Step 1: Resolve project
	projectName := setupProject
	if projectName == "" {
		projects, err := vc.ListProjects()
		if err != nil {
			return fmt.Errorf("failed to list projects: %w", err)
		}
		if len(projects) == 0 {
			return fmt.Errorf("no projects found. Create one with 'kagi project create'")
		}

		fmt.Println("Select a project:")
		for i, p := range projects {
			fmt.Printf("  %d. %s\n", i+1, p.Name)
		}
		fmt.Print("\nEnter number: ")

		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read input: %w", err)
		}
		input = strings.TrimSpace(input)

		choice, err := strconv.Atoi(input)
		if err != nil || choice < 1 || choice > len(projects) {
			return fmt.Errorf("invalid selection: %s", input)
		}
		projectName = projects[choice-1].Name
	}

	// Step 2: Find project slug to list apps and environments
	proj, err := findProject(vc, projectName)
	if err != nil {
		return err
	}

	// Step 3: Resolve app
	appName := setupApp
	if appName == "" {
		apps, err := vc.ListApps(proj.Slug)
		if err != nil {
			return fmt.Errorf("failed to list apps: %w", err)
		}
		if len(apps) == 0 {
			return fmt.Errorf("no apps found for project %q. Create one with 'kagi app create'", projectName)
		}

		if len(apps) == 1 {
			// Auto-select the only app
			appName = apps[0].Name
			fmt.Printf("\nAuto-selected app: %s\n", appName)
		} else {
			fmt.Println("\nSelect an app:")
			for i, a := range apps {
				fmt.Printf("  %d. %s\n", i+1, a.Name)
			}
			fmt.Print("\nEnter number: ")

			reader := bufio.NewReader(os.Stdin)
			input, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("failed to read input: %w", err)
			}
			input = strings.TrimSpace(input)

			choice, err := strconv.Atoi(input)
			if err != nil || choice < 1 || choice > len(apps) {
				return fmt.Errorf("invalid selection: %s", input)
			}
			appName = apps[choice-1].Name
		}
	}

	// Step 4: Resolve environment
	envSlug := setupEnv
	if envSlug == "" {
		envs, err := vc.ListEnvironments(proj.Slug)
		if err != nil {
			return fmt.Errorf("failed to list environments: %w", err)
		}
		if len(envs) == 0 {
			return fmt.Errorf("no environments found for project %q", projectName)
		}

		fmt.Println("\nSelect an environment:")
		for i, e := range envs {
			fmt.Printf("  %d. %s (%s)\n", i+1, e.Name, e.Slug)
		}
		fmt.Print("\nEnter number: ")

		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read input: %w", err)
		}
		input = strings.TrimSpace(input)

		choice, err := strconv.Atoi(input)
		if err != nil || choice < 1 || choice > len(envs) {
			return fmt.Errorf("invalid selection: %s", input)
		}
		envSlug = envs[choice-1].Slug
	}

	// Step 5: Write kagi.yaml
	if _, err := os.Stat("kagi.yaml"); err == nil && !setupForce {
		fmt.Print("Configuration kagi.yaml already exists. Overwrite? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(input)) != "y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	content := fmt.Sprintf("project: %s\napp: %s\nenvironment: %s\n", projectName, appName, envSlug)
	if err := os.WriteFile("kagi.yaml", []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write kagi.yaml: %w", err)
	}

	fmt.Printf("Configuration saved to kagi.yaml.\n  Project:     %s\n  App:         %s\n  Environment: %s\n", projectName, appName, envSlug)
	return nil
}
