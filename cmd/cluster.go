package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/kube"
	"github.com/spf13/cobra"
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
)

var clusterRegisterCmd = &cobra.Command{
	Use:   "register",
	Short: "Register a cluster OIDC issuer (idempotent)",
	Long: "Register a Kubernetes cluster's OIDC issuer with Kagi. Idempotent: if an issuer\n" +
		"with the same URL is already registered, it is left unchanged.\n\n" +
		"The issuer URL is auto-detected from the cluster via kubectl unless --issuer-url is\n" +
		"given. For a private cluster whose JWKS Kagi cannot fetch, pass --detect-jwks to read\n" +
		"it from the cluster, or --static-jwks-file to supply it from a file.",
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
		"  --enabled / --disable      trust or stop trusting the issuer for token exchange",
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
	clusterRegisterCmd.Flags().StringVar(&clusterRegisterName, "name", "", "Display name for the cluster issuer (defaults to the issuer URL)")
	clusterRegisterCmd.Flags().StringVar(&clusterRegisterJwksFile, "static-jwks-file", "", "Path to a static JWKS JSON file (for private clusters)")
	clusterRegisterCmd.Flags().BoolVar(&clusterRegisterDetectJwks, "detect-jwks", false, "Detect the JWKS from the cluster via kubectl (for private clusters)")
	clusterRegisterCmd.Flags().StringVar(&clusterRegisterContext, "context", "", "kubectl context to use for auto-detection")

	clusterUpdateCmd.Flags().StringVar(&clusterUpdateName, "name", "", "New display name for the cluster issuer")
	clusterUpdateCmd.Flags().StringVar(&clusterUpdateJwksFile, "static-jwks-file", "", "Path to a static JWKS JSON file to pin (for private clusters)")
	clusterUpdateCmd.Flags().BoolVar(&clusterUpdateDetectJwks, "detect-jwks", false, "Detect and pin the JWKS from the cluster via kubectl")
	clusterUpdateCmd.Flags().BoolVar(&clusterUpdateClearJwks, "clear-jwks", false, "Clear the pinned JWKS and revert to OIDC discovery")
	clusterUpdateCmd.Flags().StringVar(&clusterUpdateContext, "context", "", "kubectl context to use for JWKS detection")
	clusterUpdateCmd.Flags().BoolVar(&clusterUpdateEnable, "enabled", false, "Trust the issuer for token exchange")
	clusterUpdateCmd.Flags().BoolVar(&clusterUpdateDisable, "disable", false, "Stop trusting the issuer for token exchange")
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

// resolveClusterIssuerIdentity resolves the issuer's identity — its URL (auto-
// detected via kubectl when omitted) and display name (defaulting to the URL).
// The URL is the issuer's idempotency key, so it is resolved before matching an
// existing issuer; the JWKS, which is only needed on writes, is resolved
// separately via resolveIssuerJwks.
func resolveClusterIssuerIdentity(issuerURL, name, contextName string) (resolvedURL, resolvedName string, err error) {
	resolvedURL = issuerURL
	if resolvedURL == "" {
		detected, derr := kube.DetectIssuerURL(contextName)
		if derr != nil {
			return "", "", fmt.Errorf("could not auto-detect the cluster issuer URL: %w", derr)
		}
		resolvedURL = detected
	}

	resolvedName = name
	if resolvedName == "" {
		resolvedName = resolvedURL
	}
	return resolvedURL, resolvedName, nil
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
// kubectl auto-detection, plus an optional JWKS source) into the concrete issuer
// URL, JWKS, and display name to register. It composes identity and JWKS
// resolution for the `register` path, which always needs all three.
func resolveClusterIssuerInput(issuerURL, name, jwksFile string, detectJwks bool, contextName string) (resolvedURL, resolvedName, resolvedJwks string, err error) {
	resolvedURL, resolvedName, err = resolveClusterIssuerIdentity(issuerURL, name, contextName)
	if err != nil {
		return "", "", "", err
	}
	resolvedJwks, err = resolveIssuerJwks(jwksFile, detectJwks, contextName)
	if err != nil {
		return "", "", "", err
	}
	return resolvedURL, resolvedName, resolvedJwks, nil
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
func findOrCreateClusterIssuer(vc *client.KagiClient, issuerURL, name, jwks string) (issuer *client.ClusterIssuer, created bool, err error) {
	issuers, err := vc.ListClusterIssuers()
	if err != nil {
		return nil, false, fmt.Errorf("failed to list cluster issuers: %w", err)
	}
	if existing := matchIssuerByURL(issuers, issuerURL); existing != nil {
		return existing, false, nil
	}

	newIssuer, err := vc.CreateClusterIssuer(issuerURL, name, jwks)
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
// name, JWKS, and enabled flag should do: create when none exists, unchanged when
// the existing one already matches all three, otherwise update.
func decideIssuerAction(existing *client.ClusterIssuer, desiredName, desiredJwks string, desiredEnabled bool) issuerAction {
	if existing == nil {
		return issuerActionCreate
	}
	if existing.DisplayName == desiredName &&
		existing.StaticJwks == desiredJwks &&
		existing.Enabled == desiredEnabled {
		return issuerActionUnchanged
	}
	return issuerActionUpdate
}

func runClusterRegister(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	if clusterRegisterJwksFile != "" && clusterRegisterDetectJwks {
		return fmt.Errorf("use either --static-jwks-file or --detect-jwks, not both")
	}

	issuerURL, name, jwks, err := resolveClusterIssuerInput(
		clusterRegisterIssuerURL, clusterRegisterName, clusterRegisterJwksFile, clusterRegisterDetectJwks, clusterRegisterContext)
	if err != nil {
		return err
	}

	issuer, created, err := findOrCreateClusterIssuer(vc, issuerURL, name, jwks)
	if err != nil {
		return err
	}

	if created {
		fmt.Printf("Registered cluster issuer %q (%s).\n", issuer.DisplayName, issuer.IssuerURL)
	} else {
		fmt.Printf("Cluster issuer %q is already registered (%s) — unchanged.\n", issuer.DisplayName, issuer.IssuerURL)
	}
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
	fmt.Fprintln(w, "DISPLAY NAME\tISSUER URL\tENABLED\tJWKS\tID")
	for _, issuer := range issuers {
		jwks := "auto"
		if strings.TrimSpace(issuer.StaticJwks) != "" {
			jwks = "static"
		}
		fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%s\n", issuer.DisplayName, issuer.IssuerURL, issuer.Enabled, jwks, issuer.ID)
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

	if decideIssuerAction(issuer, desiredName, desiredJwks, desiredEnabled) == issuerActionUnchanged {
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

	updated, err := vc.UpdateClusterIssuer(issuer.ID, desiredName, desiredJwks, desiredEnabled)
	if err != nil {
		return fmt.Errorf("failed to update cluster issuer %q: %w", issuer.DisplayName, err)
	}

	fmt.Printf("Updated cluster issuer %q (%s).\n", updated.DisplayName, updated.IssuerURL)
	return nil
}
