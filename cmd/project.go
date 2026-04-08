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

var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Manage projects",
}

var projectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all projects",
	RunE:  runProjects, // reuse existing runProjects function from projects.go
}

var (
	projectCreateName string
	projectCreateDesc string
)

var projectCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new project",
	RunE:  runProjectCreate,
}

var (
	projectDeleteName string
	projectDeleteYes  bool
)

var projectDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete a project",
	RunE:  runProjectDelete,
}

func init() {
	projectCreateCmd.Flags().StringVar(&projectCreateName, "name", "", "Project name (required)")
	projectCreateCmd.Flags().StringVar(&projectCreateDesc, "description", "", "Project description")
	_ = projectCreateCmd.MarkFlagRequired("name")

	projectDeleteCmd.Flags().StringVar(&projectDeleteName, "name", "", "Project name (required)")
	projectDeleteCmd.Flags().BoolVarP(&projectDeleteYes, "yes", "y", false, "Skip confirmation prompt")
	_ = projectDeleteCmd.MarkFlagRequired("name")

	projectCmd.AddCommand(projectListCmd)
	projectCmd.AddCommand(projectCreateCmd)
	projectCmd.AddCommand(projectDeleteCmd)
	rootCmd.AddCommand(projectCmd)
}

func runProjectCreate(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	project, err := vc.CreateProject(projectCreateName, projectCreateDesc)
	if err != nil {
		return fmt.Errorf("failed to create project: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSLUG\tDESCRIPTION")
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", project.ID, project.Name, project.Slug, project.Description)
	_ = w.Flush()

	fmt.Printf("Created project %q.\n", project.Name)
	return nil
}

func runProjectDelete(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	// Find project by name
	proj, err := findProject(vc, projectDeleteName)
	if err != nil {
		return err
	}

	// Confirm deletion
	if !projectDeleteYes {
		fmt.Printf("Are you sure you want to delete project %q? This cannot be undone. [y/N]: ", projectDeleteName)
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

	if err := vc.DeleteProject(proj.ID); err != nil {
		return fmt.Errorf("failed to delete project: %w", err)
	}

	fmt.Printf("Deleted project %q.\n", projectDeleteName)
	return nil
}
