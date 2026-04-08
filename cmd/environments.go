package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/spf13/cobra"
)

var envProject string

var environmentsCmd = &cobra.Command{
	Use:   "environments",
	Short: "List environments for a project",
	RunE:  runEnvironments,
}

func init() {
	environmentsCmd.Flags().StringVarP(&envProject, "project", "p", "", "Project name (required)")
	_ = environmentsCmd.MarkFlagRequired("project")
	rootCmd.AddCommand(environmentsCmd)
}

func runEnvironments(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	// Find project by name
	proj, err := findProject(vc, envProject)
	if err != nil {
		return err
	}

	envs, err := vc.ListEnvironments(proj.Slug)
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
