package cmd

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/kube"
	"github.com/senseylabs/kagi-cli/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var clusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Register Kubernetes clusters with Kagi workload identity",
	Long: "Register a Kubernetes cluster's OIDC issuer with Kagi so its workloads can\n" +
		"authenticate to Kagi with their projected service-account tokens.\n\n" +
		"  kagi cluster create --context prod      register the current/selected cluster (auto-detects the issuer URL)\n" +
		"  kagi cluster list                       list registered cluster issuers\n" +
		"  kagi cluster update <id|url> --name x   update a cluster issuer's name, JWKS, or enabled flag\n" +
		"  kagi cluster delete <id|url>            remove a cluster issuer\n" +
		"  kagi cluster apply -f trust.yaml        reconcile issuers + workload bindings declaratively\n\n" +
		"Credential: log in with 'kagi login' as a user in an org ADMIN/OWNER role. These\n" +
		"writes require an active org: run 'kagi org use <slug>' first.",
}

var (
	clusterRegisterIssuerURL  string
	clusterRegisterName       string
	clusterRegisterJwksFile   string
	clusterRegisterDetectJwks bool
	clusterRegisterContext    string
	clusterRegisterType       string
)

var clusterRegisterCmd = &cobra.Command{
	Use:     "create",
	Aliases: []string{"register"},
	Short:   "Register a cluster OIDC issuer (idempotent upsert)",
	Long: "Register a Kubernetes cluster's OIDC issuer with Kagi. Idempotent upsert: if an\n" +
		"issuer with the same URL is already registered, it is left unchanged.\n\n" +
		"Run with no flags on an interactive terminal to pick a kubeconfig context from a\n" +
		"list; the issuer URL and cluster type are then auto-detected for you. Passing any\n" +
		"flag (or running non-interactively, e.g. in CI) uses the flag-driven flow instead.\n\n" +
		"The issuer URL is auto-detected from the cluster via kubectl unless --issuer-url is\n" +
		"given. The cluster type is auto-detected from the issuer URL unless --type is given.\n" +
		"For a private cluster whose JWKS Kagi cannot fetch, pass --detect-jwks to read it\n" +
		"from the cluster, or --static-jwks-file to supply it from a file.",
	Args: cobra.NoArgs,
	RunE: runClusterRegister,
}

var clusterListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered cluster issuers",
	Args:  cobra.NoArgs,
	RunE:  runClusterList,
}

var (
	clusterUpdateName       string
	clusterUpdateJwksFile   string
	clusterUpdateDetectJwks bool
	clusterUpdateContext    string
	clusterUpdateEnable     bool
	clusterUpdateDisable    bool
	clusterUpdateYes        bool
	clusterUpdateType       string
)

var clusterUpdateCmd = &cobra.Command{
	Use:   "update <ID_OR_URL>",
	Short: "Update a cluster issuer's display name, JWKS, or enabled flag",
	Long: "Update a registered cluster issuer. Only the fields you pass as flags change;\n" +
		"the rest keep their current values. The issuer URL is immutable — re-point an\n" +
		"issuer by removing and re-registering it.\n\n" +
		"  --name <name>              set a new display name\n" +
		"  --static-jwks-file <path>  pin a static JWKS from a file (private clusters)\n" +
		"  --detect-jwks              pin the JWKS detected from the cluster via kubectl\n" +
		"  --clear-jwks               clear the pinned JWKS and revert to OIDC discovery\n" +
		"  --enable / --disable       trust or stop trusting the issuer for token exchange\n" +
		"  --type <platform>          set the platform (AKS, EKS, GKE, OPENSHIFT, K3S, GENERIC)",
	Args: cobra.ExactArgs(1),
	RunE: runClusterUpdate,
}

var clusterUpdateClearJwks bool

var clusterRmYes bool

var clusterRmCmd = &cobra.Command{
	Use:               "delete <ID_OR_URL>",
	Aliases:           []string{"rm"},
	Short:             "Remove a cluster issuer",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeClusterIssuers,
	RunE:              runClusterRm,
}

var (
	clusterApplyFile    string
	clusterApplyContext string
	clusterApplyPrune   bool
	clusterApplyYes     bool
)

var clusterApplyCmd = &cobra.Command{
	Use:   "apply -f <file>",
	Short: "Reconcile cluster issuers and workload bindings from a YAML file",
	Long: "Declaratively reconcile a cluster issuer and its workload bindings from a YAML\n" +
		"file. Idempotent: existing resources are matched and updated in place, missing\n" +
		"ones are created, and unchanged ones are left alone.\n\n" +
		"With --prune, workload bindings on this issuer that are absent from the file are\n" +
		"deleted (each deletion is logged). Without it (the default) nothing is removed.",
	Args: cobra.NoArgs,
	RunE: runClusterApply,
}

func init() {
	clusterRegisterCmd.Flags().StringVar(&clusterRegisterIssuerURL, "issuer-url", "", "Cluster OIDC issuer URL (auto-detected via kubectl if omitted)")
	clusterRegisterCmd.Flags().StringVar(&clusterRegisterName, "name", "", "Display name for the cluster issuer (defaults to the kubectl context name)")
	clusterRegisterCmd.Flags().StringVar(&clusterRegisterJwksFile, "static-jwks-file", "", "Path to a static JWKS JSON file (for private clusters)")
	clusterRegisterCmd.Flags().BoolVar(&clusterRegisterDetectJwks, "detect-jwks", false, "Detect the JWKS from the cluster via kubectl (for private clusters)")
	clusterRegisterCmd.Flags().StringVar(&clusterRegisterContext, "context", "", "kubectl context to use for auto-detection")
	clusterRegisterCmd.Flags().StringVar(&clusterRegisterType, "type", "", "Cluster platform: AKS, EKS, GKE, OPENSHIFT, K3S, or GENERIC (auto-detected from the issuer URL if omitted)")
	clusterRegisterCmd.MarkFlagsMutuallyExclusive("static-jwks-file", "detect-jwks")

	clusterUpdateCmd.Flags().StringVar(&clusterUpdateName, "name", "", "New display name for the cluster issuer")
	clusterUpdateCmd.Flags().StringVar(&clusterUpdateJwksFile, "static-jwks-file", "", "Path to a static JWKS JSON file to pin (for private clusters)")
	clusterUpdateCmd.Flags().BoolVar(&clusterUpdateDetectJwks, "detect-jwks", false, "Detect and pin the JWKS from the cluster via kubectl")
	clusterUpdateCmd.Flags().BoolVar(&clusterUpdateClearJwks, "clear-jwks", false, "Clear the pinned JWKS and revert to OIDC discovery")
	clusterUpdateCmd.Flags().StringVar(&clusterUpdateContext, "context", "", "kubectl context to use for JWKS detection")
	clusterUpdateCmd.Flags().BoolVar(&clusterUpdateEnable, "enable", false, "Trust the issuer for token exchange")
	clusterUpdateCmd.Flags().BoolVar(&clusterUpdateDisable, "disable", false, "Stop trusting the issuer for token exchange")
	clusterUpdateCmd.Flags().StringVar(&clusterUpdateType, "type", "", "Set the cluster platform: AKS, EKS, GKE, OPENSHIFT, K3S, or GENERIC")
	clusterUpdateCmd.Flags().BoolVarP(&clusterUpdateYes, "yes", "y", false, "Skip confirmation prompt")
	clusterUpdateCmd.MarkFlagsMutuallyExclusive("static-jwks-file", "detect-jwks", "clear-jwks")
	clusterUpdateCmd.MarkFlagsMutuallyExclusive("enable", "disable")
	clusterUpdateCmd.ValidArgsFunction = completeClusterIssuers

	clusterRmCmd.Flags().BoolVarP(&clusterRmYes, "yes", "y", false, "Skip confirmation prompt")

	clusterApplyCmd.Flags().StringVarP(&clusterApplyFile, "file", "f", "", "Path to the trust YAML file (required)")
	clusterApplyCmd.Flags().StringVar(&clusterApplyContext, "context", "", "kubectl context to use for auto-detection")
	clusterApplyCmd.Flags().BoolVar(&clusterApplyPrune, "prune", false, "Delete this issuer's workload bindings that are absent from the file")
	clusterApplyCmd.Flags().BoolVarP(&clusterApplyYes, "yes", "y", false, "Skip the confirmation prompt before pruning")
	_ = clusterApplyCmd.MarkFlagRequired("file")

	clusterCmd.AddCommand(clusterRegisterCmd)
	clusterCmd.AddCommand(clusterListCmd)
	clusterCmd.AddCommand(clusterUpdateCmd)
	clusterCmd.AddCommand(clusterRmCmd)
	clusterCmd.AddCommand(clusterApplyCmd)
	rootCmd.AddCommand(clusterCmd)
}

// findClusterIssuer resolves a cluster-issuer reference — an id (exact or prefix)
// or an issuer URL — to the registered issuer. The backend has no find-by-url
// route, so matching is done client-side over the list. It is the shared lookup
// used by `cluster rm`, `workload bind`, and `cluster apply`.
func findClusterIssuer(vc *client.KagiClient, ref string) (*client.ClusterIssuer, error) {
	issuers, err := vc.ListClusterIssuers()
	if err != nil {
		return nil, fmt.Errorf("failed to list cluster issuers: %w", err)
	}
	return matchClusterIssuer(issuers, ref)
}

// matchClusterIssuer is the pure matching half of findClusterIssuer: it resolves
// ref against an already-fetched issuer list. It matches, in order, an exact id,
// an exact issuer URL (case-insensitive), then an unambiguous id prefix.
func matchClusterIssuer(issuers []client.ClusterIssuer, ref string) (*client.ClusterIssuer, error) {
	for i := range issuers {
		if issuers[i].ID == ref {
			return &issuers[i], nil
		}
	}
	for i := range issuers {
		if strings.EqualFold(issuers[i].IssuerURL, ref) {
			return &issuers[i], nil
		}
	}
	var match *client.ClusterIssuer
	for i := range issuers {
		if strings.HasPrefix(issuers[i].ID, ref) {
			if match != nil {
				return nil, fmt.Errorf("cluster issuer reference %q is ambiguous — it matches more than one id. Use the full id or the issuer URL", ref)
			}
			match = &issuers[i]
		}
	}
	if match != nil {
		return match, nil
	}
	return nil, fmt.Errorf("cluster issuer %q not found. List registered issuers with 'kagi cluster list'", ref)
}

// clusterTypeOptions is the fixed, ordered set of valid cluster platform values,
// reused by --type validation and the interactive type prompt.
var clusterTypeOptions = []string{
	string(kube.ClusterTypeAKS),
	string(kube.ClusterTypeEKS),
	string(kube.ClusterTypeGKE),
	string(kube.ClusterTypeOpenShift),
	string(kube.ClusterTypeK3s),
	string(kube.ClusterTypeGeneric),
}

// parseClusterType upper-cases and validates a raw --type value against the six
// known platforms, returning an actionable error listing the valid values. It
// runs before any network call so an invalid value fails fast.
func parseClusterType(raw string) (kube.ClusterType, error) {
	normalized := strings.ToUpper(strings.TrimSpace(raw))
	if slices.Contains(clusterTypeOptions, normalized) {
		return kube.ClusterType(normalized), nil
	}
	return "", fmt.Errorf("unknown --type %q. Valid values: %s", raw, strings.Join(clusterTypeOptions, ", "))
}

// resolveClusterIssuerIdentity resolves the issuer's identity — its URL (auto-
// detected via kubectl when omitted), display name (defaulting to the kubectl
// context name, never the URL, when omitted), and platform type (an explicit
// value wins, otherwise it is detected from the resolved URL). The URL is the
// issuer's idempotency key, so it is resolved before matching an existing issuer;
// the JWKS, which is only needed on writes, is resolved separately via
// resolveIssuerJwks.
func resolveClusterIssuerIdentity(issuerURL, name, contextName string, explicitType kube.ClusterType) (resolvedURL, resolvedName string, resolvedType kube.ClusterType, err error) {
	autoDetected := issuerURL == ""
	resolvedURL = issuerURL
	if resolvedURL == "" {
		detected, derr := kube.DetectIssuerURL(contextName)
		if derr != nil {
			return "", "", "", fmt.Errorf("could not auto-detect the cluster issuer URL: %w", derr)
		}
		resolvedURL = detected
	}

	resolvedName = name
	if resolvedName == "" {
		resolvedName = defaultIssuerName(contextName, resolvedURL, autoDetected)
	}

	resolvedType = explicitType
	if resolvedType == "" {
		resolvedType = kube.DetectClusterType(resolvedURL)
	}
	return resolvedURL, resolvedName, resolvedType, nil
}

// defaultIssuerName picks the default display name when --name is omitted: the
// explicit context name if one was given; otherwise, when the URL was auto-
// detected on the current context, that current context's name; falling back to
// the resolved URL only when no context is in play (e.g. an explicit --issuer-url
// with no cluster access). It never returns the issuer URL when a context name is
// available.
func defaultIssuerName(contextName, resolvedURL string, autoDetected bool) string {
	if contextName != "" {
		return contextName
	}
	if autoDetected {
		if current, cerr := kube.CurrentContext(); cerr == nil && current != "" {
			return current
		}
	}
	return resolvedURL
}

// resolveIssuerJwks resolves the static JWKS document for an issuer write from
// either a file (jwksFile) or the cluster itself (detectJwks via kubectl). An
// empty result means "no pinned JWKS" (a public cluster whose JWKS Kagi fetches).
// It is kept separate from identity resolution so callers can resolve it lazily —
// only when a write actually needs it, not on every reconcile pass.
func resolveIssuerJwks(jwksFile string, detectJwks bool, contextName string) (string, error) {
	switch {
	case jwksFile != "":
		data, err := os.ReadFile(jwksFile)
		if err != nil {
			return "", fmt.Errorf("failed to read static JWKS file %s: %w", jwksFile, err)
		}
		return string(data), nil
	case detectJwks:
		detected, err := kube.DetectJWKS(contextName)
		if err != nil {
			return "", fmt.Errorf("could not auto-detect the cluster JWKS: %w", err)
		}
		return detected, nil
	default:
		return "", nil
	}
}

// resolveClusterIssuerInput turns register-time inputs (an explicit issuer URL or
// kubectl auto-detection, plus an optional JWKS source and platform type) into the
// concrete issuer URL, display name, JWKS, and type to register. It composes
// identity and JWKS resolution for the `register` path, which always needs all
// four.
func resolveClusterIssuerInput(issuerURL, name, jwksFile string, detectJwks bool, contextName string, explicitType kube.ClusterType) (resolvedURL, resolvedName, resolvedJwks string, resolvedType kube.ClusterType, err error) {
	resolvedURL, resolvedName, resolvedType, err = resolveClusterIssuerIdentity(issuerURL, name, contextName, explicitType)
	if err != nil {
		return "", "", "", "", err
	}
	resolvedJwks, err = resolveIssuerJwks(jwksFile, detectJwks, contextName)
	if err != nil {
		return "", "", "", "", err
	}
	return resolvedURL, resolvedName, resolvedJwks, resolvedType, nil
}

// matchIssuerByURL returns the registered issuer whose URL matches issuerURL
// (case-insensitively), or nil when none does. The issuer URL is the backend's
// idempotency key — there is no find-by-url route, so matching is client-side.
func matchIssuerByURL(issuers []client.ClusterIssuer, issuerURL string) *client.ClusterIssuer {
	for i := range issuers {
		if strings.EqualFold(issuers[i].IssuerURL, issuerURL) {
			return &issuers[i]
		}
	}
	return nil
}

// findOrCreateClusterIssuer registers an issuer idempotently: it returns the
// existing issuer (created=false) when one already matches the URL, otherwise it
// creates and returns a new one (created=true).
func findOrCreateClusterIssuer(vc *client.KagiClient, issuerURL, name, jwks string, clusterType kube.ClusterType) (issuer *client.ClusterIssuer, created bool, err error) {
	issuers, err := vc.ListClusterIssuers()
	if err != nil {
		return nil, false, fmt.Errorf("failed to list cluster issuers: %w", err)
	}
	if existing := matchIssuerByURL(issuers, issuerURL); existing != nil {
		return existing, false, nil
	}

	newIssuer, err := vc.CreateClusterIssuer(issuerURL, name, jwks, string(clusterType))
	if err != nil {
		return nil, false, fmt.Errorf("failed to register cluster issuer %q: %w", issuerURL, err)
	}
	return newIssuer, true, nil
}

// issuerAction is the reconciliation verdict for a single cluster issuer.
type issuerAction string

const (
	issuerActionCreate    issuerAction = "create"
	issuerActionUpdate    issuerAction = "update"
	issuerActionUnchanged issuerAction = "unchanged"
)

// decideIssuerAction decides what reconciling an issuer to the desired display
// name, JWKS, enabled flag, and platform type should do: create when none exists,
// unchanged when the existing one already matches all four, otherwise update.
func decideIssuerAction(existing *client.ClusterIssuer, desiredName, desiredJwks string, desiredEnabled bool, desiredType string) issuerAction {
	if existing == nil {
		return issuerActionCreate
	}
	if existing.DisplayName == desiredName &&
		existing.StaticJwks == desiredJwks &&
		existing.Enabled == desiredEnabled &&
		existing.Type == desiredType {
		return issuerActionUnchanged
	}
	return issuerActionUpdate
}

// registerFlagNames are the register flags whose presence switches off the
// interactive picker. Checked via cmd.Flags().Changed so an explicit empty-string
// value still counts as "the flag was passed".
var registerFlagNames = []string{"issuer-url", "name", "static-jwks-file", "detect-jwks", "context", "type"}

// registerFlagsProvided reports whether any register flag was set on the command
// line, in which case the flag-driven flow is used instead of the picker.
func registerFlagsProvided(cmd *cobra.Command) bool {
	for _, name := range registerFlagNames {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

// printRegisterResult reports the outcome of an idempotent register to stderr.
func printRegisterResult(u *ui.UI, issuer *client.ClusterIssuer, created bool) {
	if created {
		u.Success("Registered cluster issuer %q (%s).", issuer.DisplayName, issuer.IssuerURL)
	} else {
		u.Info("Cluster issuer %q is already registered (%s) — unchanged.", issuer.DisplayName, issuer.IssuerURL)
	}
}

// issuerShortLabel returns a compact, human identifier for a cluster issuer —
// its display name when set, otherwise the issuer URL's host — so confirmation
// prompts don't echo the full OIDC URL.
func issuerShortLabel(issuer *client.ClusterIssuer) string {
	if strings.TrimSpace(issuer.DisplayName) != "" {
		return issuer.DisplayName
	}
	if u, err := url.Parse(issuer.IssuerURL); err == nil && u.Host != "" {
		return u.Host
	}
	return issuer.IssuerURL
}

// completeClusterIssuers is the dynamic shell-completion for a cluster-issuer
// reference argument (id or URL). It offers each registered issuer's id with its
// display name as the completion description; failures degrade to no completion.
func completeClusterIssuers(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
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
	issuers, err := vc.ListClusterIssuers()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]string, 0, len(issuers))
	for _, is := range issuers {
		if is.DisplayName != "" {
			out = append(out, fmt.Sprintf("%s\t%s", is.ID, is.DisplayName))
		} else {
			out = append(out, is.ID)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func runClusterRegister(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	// Validate --type up front (before any network call) so an invalid value fails
	// fast with an actionable message.
	var explicitType kube.ClusterType
	if clusterRegisterType != "" {
		parsed, perr := parseClusterType(clusterRegisterType)
		if perr != nil {
			return perr
		}
		explicitType = parsed
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	u := newUI()

	// Interactive picker: the default when run on a TTY with no register flags set.
	// Any flag present, or a non-TTY stdin (pipe/CI), keeps the flag-driven flow —
	// backward compatible and CI-safe.
	if !registerFlagsProvided(cmd) && term.IsTerminal(int(os.Stdin.Fd())) {
		return runClusterRegisterInteractive(u, vc)
	}

	issuerURL, name, jwks, resolvedType, err := resolveClusterIssuerInput(
		clusterRegisterIssuerURL, clusterRegisterName, clusterRegisterJwksFile,
		clusterRegisterDetectJwks, clusterRegisterContext, explicitType)
	if err != nil {
		return err
	}

	issuer, created, err := findOrCreateClusterIssuer(vc, issuerURL, name, jwks, resolvedType)
	if err != nil {
		return err
	}

	printRegisterResult(u, issuer, created)
	return nil
}

// runClusterRegisterInteractive drives the no-flags-on-a-TTY registration flow:
// pick a kubeconfig context, auto-detect the issuer URL and type for it, confirm
// or edit the display name (defaulting to the context name) and type, then
// register idempotently. If context enumeration is unavailable (no kubectl, empty
// kubeconfig) it prints an actionable message and falls back to the flag-driven
// auto-detect-on-current-context flow rather than hard-failing.
func runClusterRegisterInteractive(u *ui.UI, vc *client.KagiClient) error {
	contexts, err := kube.ListContexts()
	if err != nil {
		u.Warn("Interactive registration unavailable: %v", err)
		u.Info("Falling back to auto-detection on the current kubectl context.")

		issuerURL, name, jwks, resolvedType, rerr := resolveClusterIssuerInput(
			clusterRegisterIssuerURL, clusterRegisterName, clusterRegisterJwksFile,
			clusterRegisterDetectJwks, clusterRegisterContext, "")
		if rerr != nil {
			return rerr
		}
		issuer, created, rerr := findOrCreateClusterIssuer(vc, issuerURL, name, jwks, resolvedType)
		if rerr != nil {
			return rerr
		}
		printRegisterResult(u, issuer, created)
		return nil
	}

	// 1. Pick a context, defaulting the cursor to the current context when present.
	selectedContext := ""
	contextPrompt := &survey.Select{
		Message: "Select a kubeconfig context:",
		Options: contexts,
	}
	if current, cerr := kube.CurrentContext(); cerr == nil && current != "" && slices.Contains(contexts, current) {
		contextPrompt.Default = current
	}
	if err := survey.AskOne(contextPrompt, &selectedContext); err != nil {
		// Ctrl-C / Esc surfaces here — return the error for a clean non-zero exit
		// with no partial registration.
		return err
	}

	// 2. Auto-detect the issuer URL for the picked context. Hard error on failure —
	// never fall back to a guessed URL or register a partial issuer.
	issuerURL, err := kube.DetectIssuerURL(selectedContext)
	if err != nil {
		return fmt.Errorf("could not auto-detect the cluster issuer URL for context %q: %w", selectedContext, err)
	}

	// 3. Detected type is the default answer to the type prompt.
	detectedType := kube.DetectClusterType(issuerURL)

	// 4. Confirm/edit the display name — the default is the context name, never the URL.
	displayName := selectedContext
	namePrompt := &survey.Input{
		Message: "Display name:",
		Default: selectedContext,
	}
	if err := survey.AskOne(namePrompt, &displayName); err != nil {
		return err
	}

	// 5. Confirm/edit the type.
	selectedType := string(detectedType)
	typePrompt := &survey.Select{
		Message: "Cluster type:",
		Options: clusterTypeOptions,
		Default: string(detectedType),
	}
	if err := survey.AskOne(typePrompt, &selectedType); err != nil {
		return err
	}

	// JWKS is intentionally left empty in the interactive flow (out of scope);
	// users can pin it afterwards via `kagi cluster update --detect-jwks`.
	issuer, created, err := findOrCreateClusterIssuer(vc, issuerURL, displayName, "", kube.ClusterType(selectedType))
	if err != nil {
		return err
	}

	printRegisterResult(u, issuer, created)
	return nil
}

func runClusterList(cmd *cobra.Command, args []string) error {
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

	issuers, err := vc.ListClusterIssuers()
	if err != nil {
		return fmt.Errorf("failed to list cluster issuers: %w", err)
	}

	u := newUI()
	if len(issuers) == 0 && format == ui.FormatTable {
		u.Info("No cluster issuers registered.")
		return nil
	}

	// The wide, low-value columns — the issuer URL and the UUID id — are marked
	// truncatable so rows stay one line on a narrow terminal; the full values are
	// always available via -o json/yaml (piped output is never truncated).
	table := ui.NewTable("DISPLAY NAME", "ISSUER URL", "TYPE", "ENABLED", "JWKS", "ID")
	table.SetTruncatable(1, 0)
	table.SetTruncatable(5, 1)
	for _, issuer := range issuers {
		jwks := "auto"
		if strings.TrimSpace(issuer.StaticJwks) != "" {
			jwks = "static"
		}
		clusterType := issuer.Type
		if clusterType == "" {
			clusterType = string(kube.ClusterTypeGeneric)
		}
		table.AddRow(issuer.DisplayName, issuer.IssuerURL, clusterType, fmt.Sprintf("%t", issuer.Enabled), jwks, issuer.ID)
	}

	return u.Print(format, issuers, table)
}

func runClusterRm(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	issuer, err := findClusterIssuer(vc, args[0])
	if err != nil {
		return err
	}

	u := newUI()
	if !clusterRmYes {
		if !u.Confirm(fmt.Sprintf("Remove cluster issuer %q?", issuerShortLabel(issuer))) {
			return nil
		}
	}

	if err := vc.DeleteClusterIssuer(issuer.ID); err != nil {
		return fmt.Errorf("failed to remove cluster issuer %q: %w", issuer.DisplayName, err)
	}

	u.Success("Removed cluster issuer %q.", issuer.DisplayName)
	return nil
}

func runClusterUpdate(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	// Validate --type up front (before any network call) so an invalid value fails
	// fast with an actionable message.
	var explicitType kube.ClusterType
	if clusterUpdateType != "" {
		parsed, perr := parseClusterType(clusterUpdateType)
		if perr != nil {
			return perr
		}
		explicitType = parsed
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	issuer, err := findClusterIssuer(vc, args[0])
	if err != nil {
		return err
	}

	// Start from the issuer's current state, then overlay only the fields the
	// caller supplied. A cluster issuer update is a full replace on the backend,
	// so unspecified fields must carry their existing values.
	desiredName := issuer.DisplayName
	if clusterUpdateName != "" {
		desiredName = clusterUpdateName
	}

	desiredJwks := issuer.StaticJwks
	switch {
	case clusterUpdateClearJwks:
		desiredJwks = ""
	case clusterUpdateJwksFile != "" || clusterUpdateDetectJwks:
		resolved, jerr := resolveIssuerJwks(clusterUpdateJwksFile, clusterUpdateDetectJwks, clusterUpdateContext)
		if jerr != nil {
			return jerr
		}
		desiredJwks = resolved
	}

	desiredEnabled := issuer.Enabled
	switch {
	case clusterUpdateEnable:
		desiredEnabled = true
	case clusterUpdateDisable:
		desiredEnabled = false
	}

	// The type is a full-update field: it keeps its current value unless --type is
	// given. Backend requires it on update, so it is always sent through.
	desiredType := issuer.Type
	if explicitType != "" {
		desiredType = string(explicitType)
	}

	u := newUI()
	if decideIssuerAction(issuer, desiredName, desiredJwks, desiredEnabled, desiredType) == issuerActionUnchanged {
		u.Info("Cluster issuer %q is already up to date — unchanged.", issuer.DisplayName)
		return nil
	}

	if !clusterUpdateYes {
		if !u.Confirm(fmt.Sprintf("Update cluster issuer %q?", issuerShortLabel(issuer))) {
			return nil
		}
	}

	updated, err := vc.UpdateClusterIssuer(issuer.ID, desiredName, desiredJwks, desiredEnabled, desiredType)
	if err != nil {
		return fmt.Errorf("failed to update cluster issuer %q: %w", issuer.DisplayName, err)
	}

	u.Success("Updated cluster issuer %q (%s).", updated.DisplayName, updated.IssuerURL)
	return nil
}
