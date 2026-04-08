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

var appCmd = &cobra.Command{
	Use:   "app",
	Short: "Manage apps",
}

var appListProject string

var appListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all apps for a project",
	RunE:  runApps, // reuse existing runApps function from apps.go
}

var (
	appCreateProject string
	appCreateName    string
	appCreateDesc    string
)

var appCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new app",
	RunE:  runAppCreate,
}

var (
	appDeleteProject string
	appDeleteName    string
	appDeleteYes     bool
)

var appDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete an app",
	RunE:  runAppDelete,
}

func init() {
	appListCmd.Flags().StringVar(&appListProject, "project", "", "Project name (required)")
	_ = appListCmd.MarkFlagRequired("project")

	appCreateCmd.Flags().StringVar(&appCreateProject, "project", "", "Project name (required)")
	appCreateCmd.Flags().StringVar(&appCreateName, "name", "", "App name (required)")
	appCreateCmd.Flags().StringVar(&appCreateDesc, "description", "", "App description")
	_ = appCreateCmd.MarkFlagRequired("project")
	_ = appCreateCmd.MarkFlagRequired("name")

	appDeleteCmd.Flags().StringVar(&appDeleteProject, "project", "", "Project name (required)")
	appDeleteCmd.Flags().StringVar(&appDeleteName, "name", "", "App name (required)")
	appDeleteCmd.Flags().BoolVarP(&appDeleteYes, "yes", "y", false, "Skip confirmation prompt")
	_ = appDeleteCmd.MarkFlagRequired("project")
	_ = appDeleteCmd.MarkFlagRequired("name")

	appCmd.AddCommand(appListCmd)
	appCmd.AddCommand(appCreateCmd)
	appCmd.AddCommand(appDeleteCmd)
	rootCmd.AddCommand(appCmd)
}

func runAppCreate(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	projectID, err := findProjectID(vc, appCreateProject)
	if err != nil {
		return err
	}

	app, err := vc.CreateApp(projectID, appCreateName, appCreateDesc)
	if err != nil {
		return fmt.Errorf("failed to create app: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tDESCRIPTION")
	fmt.Fprintf(w, "%s\t%s\t%s\n", app.ID, app.Name, app.Description)
	_ = w.Flush()

	fmt.Printf("Created app %q.\n", app.Name)
	return nil
}

func runAppDelete(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	projectID, err := findProjectID(vc, appDeleteProject)
	if err != nil {
		return err
	}

	// Find app by name
	apps, err := vc.ListApps(projectID)
	if err != nil {
		return fmt.Errorf("failed to list apps: %w", err)
	}

	var appID string
	for _, a := range apps {
		if strings.EqualFold(a.Name, appDeleteName) {
			appID = a.ID
			break
		}
	}
	if appID == "" {
		return fmt.Errorf("app %q not found in project %q", appDeleteName, appDeleteProject)
	}

	// Confirm deletion
	if !appDeleteYes {
		fmt.Printf("Are you sure you want to delete app %q? This cannot be undone. [y/N]: ", appDeleteName)
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

	if err := vc.DeleteApp(projectID, appID); err != nil {
		return fmt.Errorf("failed to delete app: %w", err)
	}

	fmt.Printf("Deleted app %q.\n", appDeleteName)
	return nil
}
