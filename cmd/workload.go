package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/ui"
	"github.com/spf13/cobra"
)

var workloadCmd = &cobra.Command{
	Use:   "workload",
	Short: "Manage workload bindings that grant clusters access to app secrets",
	Long: "Bind a Kubernetes (namespace, service account) on a registered cluster to a set of\n" +
		"app environments, so that workload's projected tokens can read those secrets.\n\n" +
		"  kagi workload create --issuer <id|url> --namespace app --service-account api \\\n" +
		"      --scope /village/kaizen:prod    grant a service account access to an app env\n" +
		"  kagi workload list                  list workload bindings\n" +
		"  kagi workload delete <id>           remove a workload binding\n\n" +
		"A binding is keyed by (issuer, namespace, service account); binding the same triple\n" +
		"again replaces its scopes. Each scope's app must belong to the active organization.",
}

var (
	workloadBindIssuer string
	workloadBindNS     string
	workloadBindSA     string
	workloadBindScopes []string
	workloadBindPath   string
	workloadBindEnv    string
)

var workloadBindCmd = &cobra.Command{
	Use:     "create",
	Aliases: []string{"bind"},
	Short:   "Bind a cluster service account to app environments (idempotent upsert)",
	Long: "Create or update a workload binding. Idempotent upsert: if a binding already exists\n" +
		"for the (issuer, namespace, service account) triple, its scopes are replaced with the\n" +
		"ones given.\n\n" +
		"Scopes are given as repeatable --scope <app-path>:<env> flags and/or a single --path/--env\n" +
		"pair. App paths are resolved to their stable app ID and the environment is validated.",
	Args: cobra.NoArgs,
	RunE: runWorkloadBind,
}

var workloadListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workload bindings",
	Args:  cobra.NoArgs,
	RunE:  runWorkloadList,
}

var workloadUnbindYes bool

var workloadUnbindCmd = &cobra.Command{
	Use:               "delete <ID>",
	Aliases:           []string{"unbind"},
	Short:             "Remove a workload binding",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeWorkloadBindings,
	RunE:              runWorkloadUnbind,
}

func init() {
	workloadBindCmd.Flags().StringVar(&workloadBindIssuer, "issuer", "", "Cluster issuer id or URL (required)")
	workloadBindCmd.Flags().StringVar(&workloadBindNS, "namespace", "", "Kubernetes namespace (required)")
	workloadBindCmd.Flags().StringVar(&workloadBindSA, "service-account", "", "Kubernetes service account (required)")
	workloadBindCmd.Flags().StringArrayVar(&workloadBindScopes, "scope", nil, "Scope as <app-path>:<env> (repeatable)")
	workloadBindCmd.Flags().StringVarP(&workloadBindPath, "path", "p", "", "App path for a single scope (use with --env)")
	workloadBindCmd.Flags().StringVarP(&workloadBindEnv, "env", "e", "", "Environment slug for a single scope (use with --path)")
	_ = workloadBindCmd.MarkFlagRequired("issuer")
	_ = workloadBindCmd.MarkFlagRequired("namespace")
	_ = workloadBindCmd.MarkFlagRequired("service-account")
	workloadBindCmd.MarkFlagsRequiredTogether("path", "env")

	workloadUnbindCmd.Flags().BoolVarP(&workloadUnbindYes, "yes", "y", false, "Skip confirmation prompt")

	workloadCmd.AddCommand(workloadBindCmd)
	workloadCmd.AddCommand(workloadListCmd)
	workloadCmd.AddCommand(workloadUnbindCmd)
	rootCmd.AddCommand(workloadCmd)
}

// scopeInput is an unresolved scope: a human app path plus an environment slug,
// as taken from the CLI or an apply file, before resolution to a stable app ID.
type scopeInput struct {
	AppPath string
	Env     string
}

// parseScopeFlag splits a --scope value of the form <app-path>:<env> into its
// parts. App paths contain slashes and may themselves have no colon; env slugs
// are [a-z0-9-] with no colon — so the split is on the LAST colon, letting an
// app path like /village/kaizen carry a :prod suffix unambiguously.
func parseScopeFlag(raw string) (scopeInput, error) {
	idx := strings.LastIndex(raw, ":")
	if idx < 0 {
		return scopeInput{}, fmt.Errorf("invalid --scope %q: expected <app-path>:<env>", raw)
	}
	appPath := strings.TrimSpace(raw[:idx])
	env := strings.TrimSpace(raw[idx+1:])
	if appPath == "" || env == "" {
		return scopeInput{}, fmt.Errorf("invalid --scope %q: expected <app-path>:<env>", raw)
	}
	return scopeInput{AppPath: appPath, Env: env}, nil
}

// resolveScopeInputs resolves a list of human scopes (app path + env) to backend
// BindingScopes with the stable app ID and canonical env slug, validating each
// app is reachable and each environment exists. The order of inputs is preserved.
func resolveScopeInputs(vc *client.KagiClient, inputs []scopeInput) ([]client.BindingScope, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("at least one scope is required (use --scope <app-path>:<env> or --app/--env)")
	}

	scopes := make([]client.BindingScope, 0, len(inputs))
	seen := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		appID, err := vc.ResolveApp(in.AppPath)
		if err != nil {
			return nil, classifyAppError(err, in.AppPath)
		}

		envs, err := vc.ListEnvironments(appID)
		if err != nil {
			return nil, classifyAppError(err, appLabel(in.AppPath, appID))
		}
		canonical, ok := matchEnvSlug(in.Env, envs)
		if !ok {
			slugs := make([]string, len(envs))
			for i, e := range envs {
				slugs[i] = e.Slug
			}
			return nil, fmt.Errorf("environment %q not found in app %s. Available: %s", in.Env, appLabel(in.AppPath, appID), strings.Join(slugs, ", "))
		}

		// Dedupe by (appId, envSlug): two inputs can resolve to the same scope
		// (e.g. distinct app paths for one app, or a repeated env). Keeping
		// duplicates would inflate the desired set so scopesEqual never matches
		// the server's deduped set, making every reconcile report "updated".
		key := appID + "\x00" + canonical
		if seen[key] {
			continue
		}
		seen[key] = true

		scopes = append(scopes, client.BindingScope{AppID: appID, EnvironmentSlug: canonical})
	}
	return scopes, nil
}

// findWorkloadBinding finds an existing binding for the (issuer, namespace,
// service account) triple within an already-fetched list. found reports whether
// a match exists; the triple is the backend's uniqueness key.
func findWorkloadBinding(bindings []client.WorkloadBinding, issuerID, namespace, serviceAccount string) (binding *client.WorkloadBinding, found bool) {
	for i := range bindings {
		if bindings[i].ClusterIssuerID == issuerID &&
			bindings[i].Namespace == namespace &&
			bindings[i].ServiceAccount == serviceAccount {
			return &bindings[i], true
		}
	}
	return nil, false
}

// scopesEqual reports whether two scope sets are equal as (appId, envSlug) sets,
// ignoring order and server-assigned scope IDs. It is the basis of the
// create/update/unchanged decision during reconciliation.
func scopesEqual(a, b []client.BindingScope) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(s client.BindingScope) string { return s.AppID + "\x00" + s.EnvironmentSlug }
	as := make([]string, len(a))
	bs := make([]string, len(b))
	for i := range a {
		as[i] = key(a[i])
	}
	for i := range b {
		bs[i] = key(b[i])
	}
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// bindingAction is the reconciliation verdict for a single workload binding.
type bindingAction string

const (
	actionCreate    bindingAction = "create"
	actionUpdate    bindingAction = "update"
	actionUnchanged bindingAction = "unchanged"
)

// decideBindingAction decides what reconciling a binding to desiredScopes should
// do: create when none exists, unchanged when the existing one is already enabled
// with the same scope set, otherwise update.
func decideBindingAction(existing *client.WorkloadBinding, desiredScopes []client.BindingScope) bindingAction {
	if existing == nil {
		return actionCreate
	}
	if existing.Enabled && scopesEqual(existing.Scopes, desiredScopes) {
		return actionUnchanged
	}
	return actionUpdate
}

func runWorkloadBind(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	u := newUI()

	// Collect scope inputs from the repeatable --scope flags and the single
	// --path/--env pair (cobra enforces that the pair is used together).
	inputs := make([]scopeInput, 0, len(workloadBindScopes)+1)
	for _, raw := range workloadBindScopes {
		parsed, err := parseScopeFlag(raw)
		if err != nil {
			return err
		}
		inputs = append(inputs, parsed)
	}
	if workloadBindPath != "" && workloadBindEnv != "" {
		inputs = append(inputs, scopeInput{AppPath: workloadBindPath, Env: workloadBindEnv})
	}

	issuer, err := findClusterIssuer(vc, workloadBindIssuer)
	if err != nil {
		return err
	}

	scopes, err := resolveScopeInputs(vc, inputs)
	if err != nil {
		return err
	}

	bindings, err := vc.ListWorkloadBindings()
	if err != nil {
		return fmt.Errorf("failed to list workload bindings: %w", err)
	}
	existing, _ := findWorkloadBinding(bindings, issuer.ID, workloadBindNS, workloadBindSA)

	switch decideBindingAction(existing, scopes) {
	case actionCreate:
		created, err := vc.CreateWorkloadBinding(issuer.ID, workloadBindNS, workloadBindSA, scopes)
		if err != nil {
			return fmt.Errorf("failed to create workload binding %s/%s: %w", workloadBindNS, workloadBindSA, err)
		}
		u.Success("Created workload binding %s/%s on issuer %q (%d scope(s), id %s).",
			created.Namespace, created.ServiceAccount, issuer.DisplayName, len(created.Scopes), created.ID)
	case actionUnchanged:
		u.Info("Workload binding %s/%s on issuer %q is already up to date — unchanged.",
			existing.Namespace, existing.ServiceAccount, issuer.DisplayName)
	default: // actionUpdate
		updated, err := vc.UpdateWorkloadBinding(existing.ID, workloadBindNS, workloadBindSA, true, scopes)
		if err != nil {
			return fmt.Errorf("failed to update workload binding %s/%s: %w", workloadBindNS, workloadBindSA, err)
		}
		u.Success("Updated workload binding %s/%s on issuer %q (%d scope(s), id %s).",
			updated.Namespace, updated.ServiceAccount, issuer.DisplayName, len(updated.Scopes), updated.ID)
	}
	return nil
}

func runWorkloadList(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	format, err := outputFormat()
	if err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	bindings, err := vc.ListWorkloadBindings()
	if err != nil {
		return fmt.Errorf("failed to list workload bindings: %w", err)
	}

	u := newUI()
	if len(bindings) == 0 && format == ui.FormatTable {
		u.Info("No workload bindings.")
		return nil
	}

	// The wide, low-value UUID columns — the issuer id and the binding id — are
	// marked truncatable so rows stay one line on a narrow terminal; the full
	// values are always available via -o json/yaml (piped output is never
	// truncated).
	table := ui.NewTable("NAMESPACE", "SERVICE ACCOUNT", "ENABLED", "SCOPES", "ISSUER ID", "ID")
	table.SetTruncatable(4, 0)
	table.SetTruncatable(5, 1)
	for _, b := range bindings {
		table.AddRow(b.Namespace, b.ServiceAccount, fmt.Sprintf("%t", b.Enabled),
			formatScopes(b.Scopes), b.ClusterIssuerID, b.ID)
	}

	return u.Print(format, bindings, table)
}

// formatScopes renders a binding's scopes as a compact appId:env list for table
// display, or "-" when empty.
func formatScopes(scopes []client.BindingScope) string {
	if len(scopes) == 0 {
		return "-"
	}
	parts := make([]string, len(scopes))
	for i, s := range scopes {
		parts[i] = s.AppID + ":" + s.EnvironmentSlug
	}
	return strings.Join(parts, ",")
}

func runWorkloadUnbind(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	bindingID := args[0]

	bindings, err := vc.ListWorkloadBindings()
	if err != nil {
		return fmt.Errorf("failed to list workload bindings: %w", err)
	}

	var target *client.WorkloadBinding
	for i := range bindings {
		if bindings[i].ID == bindingID || strings.HasPrefix(bindings[i].ID, bindingID) {
			if target != nil {
				return fmt.Errorf("workload binding reference %q is ambiguous — use the full id", bindingID)
			}
			target = &bindings[i]
		}
	}
	if target == nil {
		return fmt.Errorf("workload binding %q not found. List bindings with 'kagi workload list'", bindingID)
	}

	u := newUI()
	if !workloadUnbindYes {
		if !u.Confirm(fmt.Sprintf("Remove workload binding %s/%s (id %s)?",
			target.Namespace, target.ServiceAccount, target.ID)) {
			return nil
		}
	}

	if err := vc.DeleteWorkloadBinding(target.ID); err != nil {
		return fmt.Errorf("failed to remove workload binding %s/%s: %w", target.Namespace, target.ServiceAccount, err)
	}

	u.Success("Removed workload binding %s/%s.", target.Namespace, target.ServiceAccount)
	return nil
}

// completeWorkloadBindings is the dynamic shell-completion for a workload-binding
// id argument. It offers each binding's id with its namespace/service-account as
// the completion description; failures degrade to no completion.
func completeWorkloadBindings(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
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
	bindings, err := vc.ListWorkloadBindings()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]string, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, fmt.Sprintf("%s\t%s/%s", b.ID, b.Namespace, b.ServiceAccount))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
