package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/spf13/cobra"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Manage environments",
}

var envListProject string

var envListCmd = &cobra.Command{
	Use:   "list",
	Short: "List environments for a project",
	RunE:  runEnvList,
}

var (
	envCreateProject string
	envCreateName    string
	envCreateSlug    string
)

var envCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new environment",
	RunE:  runEnvCreate,
}

var (
	envDeleteProject string
	envDeleteSlug    string
	envDeleteYes     bool
)

var envDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete an environment",
	RunE:  runEnvDelete,
}

func init() {
	envListCmd.Flags().StringVar(&envListProject, "project", "", "Project name (required)")
	_ = envListCmd.MarkFlagRequired("project")

	envCreateCmd.Flags().StringVar(&envCreateProject, "project", "", "Project name (required)")
	envCreateCmd.Flags().StringVar(&envCreateName, "name", "", "Environment name (required)")
	envCreateCmd.Flags().StringVar(&envCreateSlug, "slug", "", "Environment slug (required)")
	_ = envCreateCmd.MarkFlagRequired("project")
	_ = envCreateCmd.MarkFlagRequired("name")
	_ = envCreateCmd.MarkFlagRequired("slug")

	envDeleteCmd.Flags().StringVar(&envDeleteProject, "project", "", "Project name (required)")
	envDeleteCmd.Flags().StringVar(&envDeleteSlug, "slug", "", "Environment slug (required)")
	envDeleteCmd.Flags().BoolVarP(&envDeleteYes, "yes", "y", false, "Skip confirmation prompt")
	_ = envDeleteCmd.MarkFlagRequired("project")
	_ = envDeleteCmd.MarkFlagRequired("slug")

	envCmd.AddCommand(envListCmd)
	envCmd.AddCommand(envCreateCmd)
	envCmd.AddCommand(envDeleteCmd)
	rootCmd.AddCommand(envCmd)
}

func findProjectID(vc *client.KagiClient, projectName string) (string, error) {
	projects, err := vc.ListProjects()
	if err != nil {
		return "", fmt.Errorf("failed to list projects: %w", err)
	}

	for _, p := range projects {
		if strings.EqualFold(p.Name, projectName) {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("project %q not found", projectName)
}

func runEnvList(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	projectID, err := findProjectID(vc, envListProject)
	if err != nil {
		return err
	}

	envs, err := vc.ListEnvironments(projectID)
	if err != nil {
		return fmt.Errorf("failed to list environments: %w", err)
	}

	if len(envs) == 0 {
		fmt.Println("No environments found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSLUG")
	for _, e := range envs {
		fmt.Fprintf(w, "%s\t%s\t%s\n", e.ID, e.Name, e.Slug)
	}
	return w.Flush()
}

func runEnvCreate(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	projectID, err := findProjectID(vc, envCreateProject)
	if err != nil {
		return err
	}

	env, err := vc.CreateEnvironment(projectID, envCreateName, envCreateSlug)
	if err != nil {
		return fmt.Errorf("failed to create environment: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSLUG")
	fmt.Fprintf(w, "%s\t%s\t%s\n", env.ID, env.Name, env.Slug)
	_ = w.Flush()

	fmt.Printf("Created environment %q.\n", env.Name)
	return nil
}

func runEnvDelete(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	projectID, err := findProjectID(vc, envDeleteProject)
	if err != nil {
		return err
	}

	// Find environment by slug
	envs, err := vc.ListEnvironments(projectID)
	if err != nil {
		return fmt.Errorf("failed to list environments: %w", err)
	}

	var envID string
	for _, e := range envs {
		if strings.EqualFold(e.Slug, envDeleteSlug) {
			envID = e.ID
			break
		}
	}
	if envID == "" {
		return fmt.Errorf("environment %q not found in project %q", envDeleteSlug, envDeleteProject)
	}

	// Confirm deletion
	if !envDeleteYes {
		fmt.Printf("Are you sure you want to delete environment %q? This cannot be undone. [y/N]: ", envDeleteSlug)
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

	if err := vc.DeleteEnvironment(projectID, envID); err != nil {
		return fmt.Errorf("failed to delete environment: %w", err)
	}

	fmt.Printf("Deleted environment %q.\n", envDeleteSlug)
	return nil
}
