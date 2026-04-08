package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/spf13/cobra"
)

var appsProject string

var appsCmd = &cobra.Command{
	Use:   "apps",
	Short: "List all apps for a project",
	RunE:  runApps,
}

func init() {
	appsCmd.Flags().StringVarP(&appsProject, "project", "p", "", "Project name (required)")
	_ = appsCmd.MarkFlagRequired("project")
	rootCmd.AddCommand(appsCmd)
}

func runApps(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	// Resolve the project name from either the apps command or the app list command
	projectName := appsProject
	if projectName == "" {
		projectName = appListProject
	}

	// Find project by name
	proj, err := findProject(vc, projectName)
	if err != nil {
		return err
	}

	apps, err := vc.ListApps(proj.Slug)
	if err != nil {
		return fmt.Errorf("failed to list apps: %w", err)
	}

	if len(apps) == 0 {
		fmt.Println("No apps found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSLUG\tDESCRIPTION")
	for _, a := range apps {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.ID, a.Name, a.Slug, a.Description)
	}
	return w.Flush()
}
