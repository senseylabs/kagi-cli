package cmd

import (
	"fmt"
	"strings"

	kagi "github.com/senseylabs/kagi-sdk"

	"github.com/spf13/cobra"

	"github.com/senseylabs/kagi-cli/internal/auth"
	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/config"
	"github.com/senseylabs/kagi-cli/internal/ui"
)

var orgCmd = &cobra.Command{
	Use:   "org",
	Short: "Manage the active organization",
	Long: "List the organizations you belong to and choose which one the CLI acts in.\n" +
		"  kagi org list           list your organizations\n" +
		"  kagi org use <slug>     set the active organization\n" +
		"  kagi org current        show the active organization\n\n" +
		"Organization selection applies to human (JWT) login only. A KAGI_TOKEN\n" +
		"(Personal Access Token) is already bound to a single organization, so no\n" +
		"selection is needed or possible when KAGI_TOKEN is set.",
}

var orgListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the organizations you belong to",
	Args:  cobra.NoArgs,
	RunE:  runOrgList,
}

var orgUseCmd = &cobra.Command{
	Use:               "use <slug>",
	Short:             "Set the active organization by slug",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeOrgSlugs,
	RunE:              runOrgUse,
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

// rejectPATForOrgSelection blocks org list/use/current under PAT auth, where the
// org is bound to the token and cannot be chosen client-side. The backend does
// serve /kagi/organizations to a PAT — it answers with every org the token
// OWNER belongs to, not the one org the token can act in — so listing here would
// invite the caller to `kagi org use` an org the token can never reach, and the
// stored selection is not even sent (PAT requests omit X-Organization-ID).
func rejectPATForOrgSelection() error {
	if auth.StaticToken() != "" {
		return fmt.Errorf("organization selection does not apply when KAGI_TOKEN is set — a Personal Access Token is already bound to a single organization")
	}
	return nil
}

func runOrgList(cmd *cobra.Command, args []string) error {
	if err := rejectPATForOrgSelection(); err != nil {
		return err
	}
	u := newUI()
	format, err := outputFormat()
	if err != nil {
		return err
	}

	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	orgs, err := vc.ListOrganizations()
	if err != nil {
		return ui.Wrapf(err, "failed to list organizations")
	}

	if len(orgs) == 0 {
		u.Info("You do not belong to any organizations yet")
	}

	// The active marker reflects the effective (merged) selection, so a cwd
	// kagi.yaml pin is honored here just as it is when addressing resources.
	currentID := config.Load().OrganizationID

	table := ui.NewTable("ACTIVE", "SLUG", "NAME", "ID")
	table.SetTruncatable(3, 0)
	for _, o := range orgs {
		marker := ""
		if o.ID == currentID {
			marker = "*"
		}
		table.AddRow(marker, o.Slug, o.Name, o.ID)
	}
	return u.Print(format, orgs, table)
}

func runOrgUse(cmd *cobra.Command, args []string) error {
	if err := rejectPATForOrgSelection(); err != nil {
		return err
	}
	u := newUI()
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
		return ui.Wrapf(err, "failed to list organizations")
	}

	for _, o := range orgs {
		if strings.EqualFold(o.Slug, slug) {
			if err := config.SaveOrganization(o.Slug, o.ID); err != nil {
				return ui.Wrapf(err, "failed to save active organization")
			}
			u.Success("Active organization set to %s (%s)", o.Slug, o.Name)

			// A kagi.yaml in this directory takes precedence over the home
			// selection we just wrote, so warn that the success line above does
			// not apply here — this directory keeps using its pinned org.
			if cwdSlug, cwdID := config.CWDOrganization(); cwdID != "" && cwdID != o.ID {
				pinned := cwdSlug
				if pinned == "" {
					pinned = cwdID
				}
				u.Warn("a kagi.yaml in this directory pins organization %s, which overrides that selection here; commands run in this directory still use %s", pinned, pinned)
			}
			return nil
		}
	}

	available := make([]string, 0, len(orgs))
	for _, o := range orgs {
		available = append(available, o.Slug)
	}
	if len(available) == 0 {
		return ui.Errorf("you are not a member of any organization, so %q cannot be selected", slug)
	}
	return ui.Errorf("you are not a member of organization %q (available: %s)", slug, strings.Join(available, ", "))
}

func runOrgCurrent(cmd *cobra.Command, args []string) error {
	if err := rejectPATForOrgSelection(); err != nil {
		return err
	}
	u := newUI()
	format, err := outputFormat()
	if err != nil {
		return err
	}

	cfg := config.Load()
	if cfg.OrganizationID == "" {
		return kagi.ErrNoOrganizationSelected
	}

	// When a cwd kagi.yaml pins an org that differs from the home selection, the
	// effective org shown below comes from the pin — say so, so "current" is not
	// mistaken for the stored selection.
	cwdSlug, cwdID := config.CWDOrganization()
	homeSlug, homeID := config.HomeOrganization()
	if cwdID != "" && homeID != "" && cwdID != homeID {
		pinned := cwdSlug
		if pinned == "" {
			pinned = cwdID
		}
		selected := homeSlug
		if selected == "" {
			selected = homeID
		}
		u.Warn("a kagi.yaml in this directory pins organization %s, overriding your selected organization %s", pinned, selected)
	}

	table := ui.NewTable("SLUG", "ID").SetTruncatable(1, 0)
	table.AddRow(cfg.Organization, cfg.OrganizationID)
	payload := kagi.Organization{ID: cfg.OrganizationID, Slug: cfg.Organization}
	return u.Print(format, payload, table)
}

// completeOrgSlugs offers the slugs of the user's organizations for
// `org use <slug>` shell completion, reusing the SDK list call. Any failure
// (unauthenticated, network) yields no suggestions rather than an error.
func completeOrgSlugs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if err := requireAuth(); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	orgs, err := vc.ListOrganizations()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	slugs := make([]string, 0, len(orgs))
	for _, o := range orgs {
		slugs = append(slugs, o.Slug)
	}
	return slugs, cobra.ShellCompDirectiveNoFileComp
}
