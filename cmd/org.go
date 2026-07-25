package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	kagi "github.com/senseylabs/kagi-sdk"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/spf13/cobra"
)

var orgCmd = &cobra.Command{
	Use:   "org",
	Short: "Manage the active organization",
	Long: "List the organizations you belong to and choose which one the CLI acts in.\n" +
		"  kagi org list           list your organizations\n" +
		"  kagi org use <slug>     set the active organization\n" +
		"  kagi org current        show the active organization",
}

var orgListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the organizations you belong to",
	Args:  cobra.NoArgs,
	RunE:  runOrgList,
}

var orgUseCmd = &cobra.Command{
	Use:   "use <slug>",
	Short: "Set the active organization by slug",
	Args:  cobra.ExactArgs(1),
	RunE:  runOrgUse,
}

var orgCurrentCmd = &cobra.Command{
	Use:   "current",
	Short: "Show the active organization",
	Args:  cobra.NoArgs,
	RunE:  runOrgCurrent,
}

func init() {
	orgCmd.AddCommand(orgListCmd, orgUseCmd, orgCurrentCmd)
	rootCmd.AddCommand(orgCmd)
}

func runOrgList(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	orgs, err := vc.ListOrganizations()
	if err != nil {
		return fmt.Errorf("failed to list organizations: %w", err)
	}

	if len(orgs) == 0 {
		fmt.Println("You do not belong to any organizations yet.")
		return nil
	}

	currentID := config.Load().OrganizationID

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ACTIVE\tSLUG\tNAME\tID")
	for _, o := range orgs {
		marker := ""
		if o.ID == currentID {
			marker = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", marker, o.Slug, o.Name, o.ID)
	}
	return w.Flush()
}

func runOrgUse(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	slug := args[0]

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	orgs, err := vc.ListOrganizations()
	if err != nil {
		return fmt.Errorf("failed to list organizations: %w", err)
	}

	for _, o := range orgs {
		if strings.EqualFold(o.Slug, slug) {
			if err := config.SaveOrganization(o.Slug, o.ID); err != nil {
				return fmt.Errorf("failed to save active organization: %w", err)
			}
			fmt.Printf("Active organization set to %q (%s).\n", o.Slug, o.Name)
			return nil
		}
	}

	available := make([]string, 0, len(orgs))
	for _, o := range orgs {
		available = append(available, o.Slug)
	}
	if len(available) == 0 {
		return fmt.Errorf("you are not a member of any organization, so %q cannot be selected", slug)
	}
	return fmt.Errorf("you are not a member of organization %q. Available: %s", slug, strings.Join(available, ", "))
}

func runOrgCurrent(cmd *cobra.Command, args []string) error {
	cfg := config.Load()
	if cfg.OrganizationID == "" {
		return kagi.ErrNoOrganizationSelected
	}

	if cfg.Organization != "" {
		fmt.Printf("%s (%s)\n", cfg.Organization, cfg.OrganizationID)
	} else {
		fmt.Println(cfg.OrganizationID)
	}
	return nil
}
