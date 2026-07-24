package cmd

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/AlecAivazis/survey/v2"
	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/kube"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var clusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Register Kubernetes clusters with Kagi workload identity",
	Long: "Register a Kubernetes cluster's OIDC issuer with Kagi so its workloads can\n" +
		"authenticate to Kagi with their projected service-account tokens.\n\n" +
		"  kagi cluster register --context prod    register the current/selected cluster (auto-detects the issuer URL)\n" +
		"  kagi cluster list                       list registered cluster issuers\n" +
		"  kagi cluster update <id|url> --name x   update a cluster issuer's name, JWKS, or enabled flag\n" +
		"  kagi cluster rm <id|url>                remove a cluster issuer\n" +
		"  kagi cluster apply -f trust.yaml        reconcile issuers + workload bindings declaratively\n\n" +
		"Credential: prefer 'kagi login' (a user in an org ADMIN/OWNER role). A KAGI_TOKEN\n" +
		"PAT works too but is org-admin-equivalent for these writes — use a short-lived one\n" +
		"in CI only. JWT writes require an active org: run 'kagi org use <slug>' first.",
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
	Use:   "register",
	Short: "Register a cluster OIDC issuer (idempotent)",
	Long: "Register a Kubernetes cluster's OIDC issuer with Kagi. Idempotent: if an issuer\n" +
		"with the same URL is already registered, it is left unchanged.\n\n" +
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
		"  --enabled / --disable      trust or stop trusting the issuer for token exchange\n" +
		"  --type <platform>          set the platform (AKS, EKS, GKE, OPENSHIFT, K3S, GENERIC)",
	Args: cobra.ExactArgs(1),
	RunE: runClusterUpdate,
}

var clusterUpdateClearJwks bool

var clusterRmYes bool

var clusterRmCmd = &cobra.Command{
	Use:   "rm <ID_OR_URL>",
	Short: "Remove a cluster issuer",
	Args:  cobra.ExactArgs(1),
	RunE:  runClusterRm,
}

var (
	clusterApplyFile    string
	clusterApplyContext string
	clusterApplyPrune   bool
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

	clusterUpdateCmd.Flags().StringVar(&clusterUpdateName, "name", "", "New display name for the cluster issuer")
	clusterUpdateCmd.Flags().StringVar(&clusterUpdateJwksFile, "static-jwks-file", "", "Path to a static JWKS JSON file to pin (for private clusters)")
	clusterUpdateCmd.Flags().BoolVar(&clusterUpdateDetectJwks, "detect-jwks", false, "Detect and pin the JWKS from the cluster via kubectl")
	clusterUpdateCmd.Flags().BoolVar(&clusterUpdateClearJwks, "clear-jwks", false, "Clear the pinned JWKS and revert to OIDC discovery")
	clusterUpdateCmd.Flags().StringVar(&clusterUpdateContext, "context", "", "kubectl context to use for JWKS detection")
	clusterUpdateCmd.Flags().BoolVar(&clusterUpdateEnable, "enabled", false, "Trust the issuer for token exchange")
	clusterUpdateCmd.Flags().BoolVar(&clusterUpdateDisable, "disable", false, "Stop trusting the issuer for token exchange")
	clusterUpdateCmd.Flags().StringVar(&clusterUpdateType, "type", "", "Set the cluster platform: AKS, EKS, GKE, OPENSHIFT, K3S, or GENERIC")
	clusterUpdateCmd.Flags().BoolVarP(&clusterUpdateYes, "yes", "y", false, "Skip confirmation prompt")

	clusterRmCmd.Flags().BoolVarP(&clusterRmYes, "yes", "y", false, "Skip confirmation prompt")

	clusterApplyCmd.Flags().StringVarP(&clusterApplyFile, "file", "f", "", "Path to the trust YAML file (required)")
	clusterApplyCmd.Flags().StringVar(&clusterApplyContext, "context", "", "kubectl context to use for auto-detection")
	clusterApplyCmd.Flags().BoolVar(&clusterApplyPrune, "prune", false, "Delete this issuer's workload bindings that are absent from the file")
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

// printRegisterResult reports the outcome of an idempotent register.
func printRegisterResult(issuer *client.ClusterIssuer, created bool) {
	if created {
		fmt.Printf("Registered cluster issuer %q (%s).\n", issuer.DisplayName, issuer.IssuerURL)
	} else {
		fmt.Printf("Cluster issuer %q is already registered (%s) — unchanged.\n", issuer.DisplayName, issuer.IssuerURL)
	}
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

	if clusterRegisterJwksFile != "" && clusterRegisterDetectJwks {
		return fmt.Errorf("use either --static-jwks-file or --detect-jwks, not both")
	}

	// Interactive picker: the default when run on a TTY with no register flags set.
	// Any flag present, or a non-TTY stdin (pipe/CI), keeps the flag-driven flow —
	// backward compatible and CI-safe.
	if !registerFlagsProvided(cmd) && term.IsTerminal(int(os.Stdin.Fd())) {
		return runClusterRegisterInteractive(vc)
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

	printRegisterResult(issuer, created)
	return nil
}

// runClusterRegisterInteractive drives the no-flags-on-a-TTY registration flow:
// pick a kubeconfig context, auto-detect the issuer URL and type for it, confirm
// or edit the display name (defaulting to the context name) and type, then
// register idempotently. If context enumeration is unavailable (no kubectl, empty
// kubeconfig) it prints an actionable message and falls back to the flag-driven
// auto-detect-on-current-context flow rather than hard-failing.
func runClusterRegisterInteractive(vc *client.KagiClient) error {
	contexts, err := kube.ListContexts()
	if err != nil {
		fmt.Fprintf(os.Stderr, "interactive registration unavailable: %v\n", err)
		fmt.Fprintln(os.Stderr, "Falling back to auto-detection on the current kubectl context.")

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
		printRegisterResult(issuer, created)
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

	printRegisterResult(issuer, created)
	return nil
}

func runClusterList(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
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

	if len(issuers) == 0 {
		fmt.Println("No cluster issuers registered.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DISPLAY NAME\tISSUER URL\tTYPE\tENABLED\tJWKS\tID")
	for _, issuer := range issuers {
		jwks := "auto"
		if strings.TrimSpace(issuer.StaticJwks) != "" {
			jwks = "static"
		}
		clusterType := issuer.Type
		if clusterType == "" {
			clusterType = string(kube.ClusterTypeGeneric)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%s\t%s\n", issuer.DisplayName, issuer.IssuerURL, clusterType, issuer.Enabled, jwks, issuer.ID)
	}
	return w.Flush()
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

	if !clusterRmYes {
		fmt.Printf("Are you sure you want to remove cluster issuer %q (%s)? [y/N]: ", issuer.DisplayName, issuer.IssuerURL)
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

	if err := vc.DeleteClusterIssuer(issuer.ID); err != nil {
		return fmt.Errorf("failed to remove cluster issuer %q: %w", issuer.DisplayName, err)
	}

	fmt.Printf("Removed cluster issuer %q.\n", issuer.DisplayName)
	return nil
}

func runClusterUpdate(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	// Reject conflicting JWKS sources and enabled flags up front, mirroring the
	// register command's mutual-exclusion checks.
	jwksSources := 0
	if clusterUpdateJwksFile != "" {
		jwksSources++
	}
	if clusterUpdateDetectJwks {
		jwksSources++
	}
	if clusterUpdateClearJwks {
		jwksSources++
	}
	if jwksSources > 1 {
		return fmt.Errorf("use only one of --static-jwks-file, --detect-jwks, or --clear-jwks")
	}
	if clusterUpdateEnable && clusterUpdateDisable {
		return fmt.Errorf("use either --enabled or --disable, not both")
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

	if decideIssuerAction(issuer, desiredName, desiredJwks, desiredEnabled, desiredType) == issuerActionUnchanged {
		fmt.Printf("Cluster issuer %q is already up to date — unchanged.\n", issuer.DisplayName)
		return nil
	}

	if !clusterUpdateYes {
		fmt.Printf("Update cluster issuer %q (%s)? [y/N]: ", issuer.DisplayName, issuer.IssuerURL)
		reader := bufio.NewReader(os.Stdin)
		input, rerr := reader.ReadString('\n')
		if rerr != nil {
			return fmt.Errorf("failed to read input: %w", rerr)
		}
		input = strings.TrimSpace(strings.ToLower(input))
		if input != "y" && input != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	updated, err := vc.UpdateClusterIssuer(issuer.ID, desiredName, desiredJwks, desiredEnabled, desiredType)
	if err != nil {
		return fmt.Errorf("failed to update cluster issuer %q: %w", issuer.DisplayName, err)
	}

	fmt.Printf("Updated cluster issuer %q (%s).\n", updated.DisplayName, updated.IssuerURL)
	return nil
}
