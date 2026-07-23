package cmd

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/spf13/cobra"
)

var workloadCmd = &cobra.Command{
	Use:   "workload",
	Short: "Manage workload bindings that grant clusters access to app secrets",
	Long: "Bind a Kubernetes (namespace, service account) on a registered cluster to a set of\n" +
		"app environments, so that workload's projected tokens can read those secrets.\n\n" +
		"  kagi workload bind --issuer <id|url> --namespace app --service-account api \\\n" +
		"      --scope /village/kaizen:prod    grant a service account access to an app env\n" +
		"  kagi workload list                  list workload bindings\n" +
		"  kagi workload unbind <id>           remove a workload binding\n\n" +
		"A binding is keyed by (issuer, namespace, service account); binding the same triple\n" +
		"again replaces its scopes. Each scope's app must belong to the active organization.",
}

var (
	workloadBindIssuer string
	workloadBindNS     string
	workloadBindSA     string
	workloadBindScopes []string
	workloadBindApp    string
	workloadBindEnv    string
)

var workloadBindCmd = &cobra.Command{
	Use:   "bind",
	Short: "Bind a cluster service account to app environments (idempotent)",
	Long: "Create or update a workload binding. Idempotent: if a binding already exists for the\n" +
		"(issuer, namespace, service account) triple, its scopes are replaced with the ones given.\n\n" +
		"Scopes are given as repeatable --scope <app-path>:<env> flags and/or a single --app/--env\n" +
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
	Use:   "unbind <ID>",
	Short: "Remove a workload binding",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkloadUnbind,
}

func init() {
	workloadBindCmd.Flags().StringVar(&workloadBindIssuer, "issuer", "", "Cluster issuer id or URL (required)")
	workloadBindCmd.Flags().StringVar(&workloadBindNS, "namespace", "", "Kubernetes namespace (required)")
	workloadBindCmd.Flags().StringVar(&workloadBindSA, "service-account", "", "Kubernetes service account (required)")
	workloadBindCmd.Flags().StringArrayVar(&workloadBindScopes, "scope", nil, "Scope as <app-path>:<env> (repeatable)")
	workloadBindCmd.Flags().StringVar(&workloadBindApp, "app", "", "App path for a single scope (use with --env)")
	workloadBindCmd.Flags().StringVar(&workloadBindEnv, "env", "", "Environment slug for a single scope (use with --app)")
	_ = workloadBindCmd.MarkFlagRequired("issuer")
	_ = workloadBindCmd.MarkFlagRequired("namespace")
	_ = workloadBindCmd.MarkFlagRequired("service-account")

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

	// Collect scope inputs from the repeatable --scope flags and the single
	// --app/--env pair.
	inputs := make([]scopeInput, 0, len(workloadBindScopes)+1)
	for _, raw := range workloadBindScopes {
		parsed, err := parseScopeFlag(raw)
		if err != nil {
			return err
		}
		inputs = append(inputs, parsed)
	}
	if workloadBindApp != "" || workloadBindEnv != "" {
		if workloadBindApp == "" || workloadBindEnv == "" {
			return fmt.Errorf("--app and --env must be used together")
		}
		inputs = append(inputs, scopeInput{AppPath: workloadBindApp, Env: workloadBindEnv})
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
		fmt.Printf("Created workload binding %s/%s on issuer %q (%d scope(s), id %s).\n",
			created.Namespace, created.ServiceAccount, issuer.DisplayName, len(created.Scopes), created.ID)
	case actionUnchanged:
		fmt.Printf("Workload binding %s/%s on issuer %q is already up to date — unchanged.\n",
			existing.Namespace, existing.ServiceAccount, issuer.DisplayName)
	default: // actionUpdate
		updated, err := vc.UpdateWorkloadBinding(existing.ID, workloadBindNS, workloadBindSA, true, scopes)
		if err != nil {
			return fmt.Errorf("failed to update workload binding %s/%s: %w", workloadBindNS, workloadBindSA, err)
		}
		fmt.Printf("Updated workload binding %s/%s on issuer %q (%d scope(s), id %s).\n",
			updated.Namespace, updated.ServiceAccount, issuer.DisplayName, len(updated.Scopes), updated.ID)
	}
	return nil
}

func runWorkloadList(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
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

	if len(bindings) == 0 {
		fmt.Println("No workload bindings.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAMESPACE\tSERVICE ACCOUNT\tENABLED\tSCOPES\tISSUER ID\tID")
	for _, b := range bindings {
		fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%s\t%s\n",
			b.Namespace, b.ServiceAccount, b.Enabled, formatScopes(b.Scopes), b.ClusterIssuerID, b.ID)
	}
	return w.Flush()
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

	if !workloadUnbindYes {
		fmt.Printf("Are you sure you want to remove workload binding %s/%s (id %s)? [y/N]: ",
			target.Namespace, target.ServiceAccount, target.ID)
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

	if err := vc.DeleteWorkloadBinding(target.ID); err != nil {
		return fmt.Errorf("failed to remove workload binding %s/%s: %w", target.Namespace, target.ServiceAccount, err)
	}

	fmt.Printf("Removed workload binding %s/%s.\n", target.Namespace, target.ServiceAccount)
	return nil
}
