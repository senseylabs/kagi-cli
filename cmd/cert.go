package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/senseylabs/kagi-cli/internal/ui"
)

var certCmd = &cobra.Command{
	Use:   "cert [path]",
	Short: "Browse certificate folders and manage certificates",
	Long: "Browse the certificates folder tree and manage certificates.\n" +
		"  kagi cert                                          browse the certificates root (folders + certificates)\n" +
		"  kagi cert /sensey                                  browse a folder\n" +
		"  kagi cert list                                     list every certificate (flat) with its folder path\n" +
		"  kagi cert get /sensey/sensey-io-cloudflare-cert    show a certificate by its node path\n" +
		"  kagi cert reveal sensey-io-cloudflare-cert         reveal by name, slug, id, or /folder/cert path\n\n" +
		"Certificates live inside certificate folders. A leading-slash argument is a node path\n" +
		"(folder segments then the certificate slug); anything else matches by name, slug, or id.",
	Args: cobra.MaximumNArgs(1),
	RunE: runCertBrowse,
}

var certListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all certificates with their folder paths",
	Args:  cobra.NoArgs,
	RunE:  runCertList,
}

var (
	certCreateName     string
	certCreateCertFile string
	certCreateKeyFile  string
	certCreatePath     string
)

var certCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new certificate",
	Args:  cobra.NoArgs,
	RunE:  runCertCreate,
}

var certGetCmd = &cobra.Command{
	Use:               "get <NAME_OR_ID>",
	Short:             "Get certificate details",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeCertRefs,
	RunE:              runCertGet,
}

var certRevealCmd = &cobra.Command{
	Use:   "reveal <NAME_OR_ID>",
	Short: "Reveal certificate and private key PEM content",
	Args:  cobra.ExactArgs(1),
	RunE:  runCertReveal,
}

var (
	certUpdateCertFile string
	certUpdateKeyFile  string
)

var certUpdateCmd = &cobra.Command{
	Use:   "update <NAME_OR_ID>",
	Short: "Update a certificate",
	Args:  cobra.ExactArgs(1),
	RunE:  runCertUpdate,
}

var certDeleteYes bool

var certDeleteCmd = &cobra.Command{
	Use:   "delete <NAME_OR_ID>",
	Short: "Delete a certificate",
	Args:  cobra.ExactArgs(1),
	RunE:  runCertDelete,
}

var certHistoryCmd = &cobra.Command{
	Use:   "history <NAME_OR_ID>",
	Short: "Show certificate audit history",
	Args:  cobra.ExactArgs(1),
	RunE:  runCertHistory,
}

func init() {
	certCreateCmd.Flags().StringVar(&certCreateName, "name", "", "Certificate name (required)")
	certCreateCmd.Flags().StringVar(&certCreateCertFile, "cert-file", "", "Path to PEM certificate file (required)")
	certCreateCmd.Flags().StringVar(&certCreateKeyFile, "key-file", "", "Path to PEM private key file")
	certCreateCmd.Flags().StringVarP(&certCreatePath, "path", "p", "/", "Certificate folder path to create the certificate in")
	_ = certCreateCmd.MarkFlagRequired("name")
	_ = certCreateCmd.MarkFlagRequired("cert-file")

	certUpdateCmd.Flags().StringVar(&certUpdateCertFile, "cert-file", "", "Path to PEM certificate file (required)")
	certUpdateCmd.Flags().StringVar(&certUpdateKeyFile, "key-file", "", "Path to PEM private key file")
	_ = certUpdateCmd.MarkFlagRequired("cert-file")

	certDeleteCmd.Flags().BoolVarP(&certDeleteYes, "yes", "y", false, "Skip confirmation prompt")

	certCmd.AddCommand(certListCmd)
	certCmd.AddCommand(certCreateCmd)
	certCmd.AddCommand(certGetCmd)
	certCmd.AddCommand(certRevealCmd)
	certCmd.AddCommand(certUpdateCmd)
	certCmd.AddCommand(certDeleteCmd)
	certCmd.AddCommand(certHistoryCmd)
	rootCmd.AddCommand(certCmd)
}

func findCertificate(vc *client.KagiClient, nameOrID string) (*client.CertificateListItem, error) {
	certs, err := vc.ListCertificates()
	if err != nil {
		return nil, err
	}

	// Match in tiers, most specific first. Certificate names and slugs are unique
	// only WITHIN a folder, so the flat list can hold several certs sharing a name
	// or slug across different folders. Collect every match in a tier and treat
	// more than one as ambiguous rather than silently taking the first.
	matchTier := func(pred func(client.CertificateListItem) bool) []int {
		var idx []int
		for i := range certs {
			if pred(certs[i]) {
				idx = append(idx, i)
			}
		}
		return idx
	}

	if m := matchTier(func(c client.CertificateListItem) bool { return strings.EqualFold(c.Name, nameOrID) }); len(m) > 0 {
		return resolveCertMatches(vc, certs, m, nameOrID)
	}
	if m := matchTier(func(c client.CertificateListItem) bool { return strings.EqualFold(c.Slug, nameOrID) }); len(m) > 0 {
		return resolveCertMatches(vc, certs, m, nameOrID)
	}
	if m := matchTier(func(c client.CertificateListItem) bool { return strings.HasPrefix(c.ID, nameOrID) }); len(m) > 0 {
		return resolveCertMatches(vc, certs, m, nameOrID)
	}
	return nil, fmt.Errorf("certificate %q not found", nameOrID)
}

// resolveCertMatches turns a tier of flat-list matches into a single certificate.
// A lone match is returned directly. Multiple matches are ambiguous — names and
// slugs are unique only within a folder — so it errors, listing each candidate's
// full node path (discovered via a tree walk) so the caller can re-run with an
// unambiguous /folder/cert path.
func resolveCertMatches(vc *client.KagiClient, certs []client.CertificateListItem, idx []int, ref string) (*client.CertificateListItem, error) {
	if len(idx) == 1 {
		return &certs[idx[0]], nil
	}

	// Best-effort map from certificate id to its full node path for an actionable
	// error. If the tree walk fails, fall back to the name and id.
	pathByID := map[string]string{}
	var entries []certPathEntry
	if err := walkCertTree(vc, "/", &entries); err == nil {
		for _, e := range entries {
			pathByID[e.cert.ID] = e.path
		}
	}

	candidates := make([]string, 0, len(idx))
	for _, i := range idx {
		if p := pathByID[certs[i].ID]; p != "" {
			candidates = append(candidates, p)
		} else {
			candidates = append(candidates, fmt.Sprintf("%s (id %s)", certs[i].Name, certs[i].ID))
		}
	}
	sort.Strings(candidates)
	return nil, fmt.Errorf("certificate %q is ambiguous — it matches %d certificates:\n  %s\nUse the /folder/cert node path to select one",
		ref, len(idx), strings.Join(candidates, "\n  "))
}

// completeCertRefs provides dynamic shell completion for a certificate reference
// argument, offering the full node path of every certificate (reusing the same
// tree walk that backs `cert list`). It stays silent on any error so completion
// never blocks the shell.
func completeCertRefs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
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
	var entries []certPathEntry
	if err := walkCertTree(vc, "/", &entries); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	comps := make([]string, 0, len(entries))
	for _, e := range entries {
		comps = append(comps, e.path)
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

// certPathEntry pairs a certificate leaf with its full node path (folder
// segments then the certificate slug).
type certPathEntry struct {
	path string
	cert client.CertificateFolderItem
}

// walkCertTree walks the certificate folder tree from path (inclusive of its
// leaf certificates), depth-first, appending every certificate it finds with its
// full node path to out. It mirrors how a certificate is addressed in the folder
// model: certificates live inside folders, so the path is the containing folder
// path plus the certificate slug.
func walkCertTree(vc *client.KagiClient, path string, out *[]certPathEntry) error {
	certs, err := vc.ListCertificatesInFolder(path)
	if err != nil {
		return err
	}
	base := strings.TrimRight(path, "/")
	for _, c := range certs {
		*out = append(*out, certPathEntry{path: base + "/" + c.Slug, cert: c})
	}

	children, err := vc.ListCertificateFolderChildren(path)
	if err != nil {
		return err
	}
	for _, f := range children.Folders {
		if err := walkCertTree(vc, base+"/"+f.Slug, out); err != nil {
			return err
		}
	}
	return nil
}

// runCertBrowse handles bare `kagi cert [path]` — it browses the CERTIFICATES
// folder tree at the given path (root when omitted), listing the child folders
// and the certificates directly under it. Mirrors `kagi secrets [path]`, but the
// certificate leaves are fetched from the dedicated /items endpoint because the
// certificates children listing carries folders only.
func runCertBrowse(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	path := "/"
	if len(args) == 1 {
		path = args[0]
	}

	children, err := vc.ListCertificateFolderChildren(path)
	if err != nil {
		return fmt.Errorf("failed to browse %q: %w", path, err)
	}
	certs, err := vc.ListCertificatesInFolder(path)
	if err != nil {
		return fmt.Errorf("failed to list certificates under %q: %w", path, err)
	}

	if len(children.Folders) == 0 && len(certs) == 0 {
		fmt.Printf("No folders or certificates under %q.\n", path)
		return nil
	}

	base := strings.TrimRight(path, "/")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TYPE\tNAME\tSLUG\tPATH\tEXPIRES")
	for _, f := range children.Folders {
		fmt.Fprintf(w, "folder\t%s\t%s\t%s\t%s\n", f.Name, f.Slug, base+"/"+f.Slug, "")
	}
	for _, c := range certs {
		fmt.Fprintf(w, "cert\t%s\t%s\t%s\t%s\n", c.Name, c.Slug, base+"/"+c.Slug, c.NotAfter)
	}
	return w.Flush()
}

// resolveCertRef turns a CLI argument into a certificate id and display name. A
// leading-slash argument is a certificate node path, resolved through the
// resolve endpoint (the machine path-to-id contract); anything else is matched
// by name, slug, or id prefix against the flat certificate list.
func resolveCertRef(vc *client.KagiClient, arg string) (id string, name string, err error) {
	if strings.HasPrefix(arg, "/") {
		resolved, err := vc.ResolveCertificate(arg)
		if err != nil {
			return "", "", err
		}
		return resolved.CertificateID, resolved.Name, nil
	}

	cert, err := findCertificate(vc, arg)
	if err != nil {
		return "", "", err
	}
	return cert.ID, cert.Name, nil
}

// lookupCertPath finds the full node path of a certificate by walking the
// certificate folder tree and matching its id. It is best-effort path
// enrichment for display: if the tree walk fails (e.g. a partially inaccessible
// tree) the error is surfaced on stderr and an empty path is returned so the
// primary command still succeeds.
func lookupCertPath(vc *client.KagiClient, certID string) string {
	var entries []certPathEntry
	if err := walkCertTree(vc, "/", &entries); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not resolve certificate path: %v\n", err)
		return ""
	}
	for _, e := range entries {
		if e.cert.ID == certID {
			return e.path
		}
	}
	return ""
}

// certListEntry is the per-certificate payload for `cert list` in json/yaml mode,
// pairing the certificate's flat metadata with the folder node path it lives in.
type certListEntry struct {
	Name    string   `json:"name" yaml:"name"`
	Path    string   `json:"path" yaml:"path"`
	Domains []string `json:"domains" yaml:"domains"`
	Expires string   `json:"expires" yaml:"expires"`
	Source  string   `json:"source" yaml:"source"`
	ID      string   `json:"id" yaml:"id"`
}

func runCertList(cmd *cobra.Command, args []string) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	if err := requireAuth(); err != nil {
		return err
	}
	u := newUI()

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	// Walk the certificate folder tree so every certificate is listed with the
	// folder path it lives in — the flat certificate list carries no path.
	var entries []certPathEntry
	if err := walkCertTree(vc, "/", &entries); err != nil {
		return fmt.Errorf("failed to list certificates: %w", err)
	}

	if len(entries) == 0 {
		if format == ui.FormatTable {
			u.Info("No certificates found.")
			return nil
		}
		return u.Print(format, []certListEntry{}, nil)
	}

	payload := make([]certListEntry, 0, len(entries))
	table := ui.NewTable("NAME", "PATH", "DOMAINS", "EXPIRES", "SOURCE")
	table.SetTruncatable(2, 0)
	for _, e := range entries {
		payload = append(payload, certListEntry{
			Name:    e.cert.Name,
			Path:    e.path,
			Domains: parseSANList(e.cert.SANs),
			Expires: e.cert.NotAfter,
			Source:  e.cert.Source,
			ID:      e.cert.ID,
		})
		table.AddRow(e.cert.Name, e.path, parseSANs(e.cert.SANs), e.cert.NotAfter, e.cert.Source)
	}
	return u.Print(format, payload, table)
}

// parseSANList decodes the JSON-encoded SANs field into a slice of domains,
// falling back to the raw value when it is not JSON.
func parseSANList(sans string) []string {
	if sans == "" {
		return nil
	}
	var domains []string
	if err := json.Unmarshal([]byte(sans), &domains); err != nil {
		return []string{sans}
	}
	return domains
}

// parseSANs renders the SANs field as a comma-separated string for table cells.
func parseSANs(sans string) string {
	return strings.Join(parseSANList(sans), ",")
}

func runCertCreate(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	u := newUI()

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	certContent, err := os.ReadFile(certCreateCertFile)
	if err != nil {
		return fmt.Errorf("failed to read certificate file %s: %w", certCreateCertFile, err)
	}

	var keyContent string
	if certCreateKeyFile != "" {
		keyBytes, err := os.ReadFile(certCreateKeyFile)
		if err != nil {
			return fmt.Errorf("failed to read key file %s: %w", certCreateKeyFile, err)
		}
		keyContent = string(keyBytes)
	}

	// Route creation through the folder-aware endpoint so the certificate lands in
	// the requested folder (root by default), which also lets a folder contributor
	// — not just an org admin — create it.
	folderPath := certCreatePath
	if folderPath == "" {
		folderPath = "/"
	}

	cert, err := vc.CreateCertificateInFolder(folderPath, certCreateName, string(certContent), keyContent)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	u.Success("Created certificate %q (thumbprint: %s).", cert.Name, cert.Thumbprint)
	return nil
}

// certGetPayload is the `cert get` payload for json/yaml mode: the full
// certificate detail flattened, plus the folder node path it lives in.
type certGetPayload struct {
	client.CertificateDetail `json:",inline" yaml:",inline"`
	Path                     string `json:"path,omitempty" yaml:"path,omitempty"`
}

func runCertGet(cmd *cobra.Command, args []string) error {
	format, err := outputFormat()
	if err != nil {
		return err
	}
	if err := requireAuth(); err != nil {
		return err
	}
	u := newUI()

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	certID, _, err := resolveCertRef(vc, args[0])
	if err != nil {
		return err
	}

	detail, err := vc.GetCertificateDetail(certID)
	if err != nil {
		return fmt.Errorf("failed to get certificate details: %w", err)
	}

	// Surface the folder path the certificate lives in. When addressed by path
	// it is known directly; when addressed by name/id it is discovered via a
	// tree walk.
	certPath := ""
	if strings.HasPrefix(args[0], "/") {
		certPath = "/" + strings.Trim(args[0], "/")
	} else {
		certPath = lookupCertPath(vc, certID)
	}

	table := ui.NewTable("FIELD", "VALUE")
	table.SetTruncatable(1, 0)
	table.AddRow("Name", detail.Name)
	table.AddRow("ID", detail.ID)
	if certPath != "" {
		table.AddRow("Path", certPath)
	}
	table.AddRow("Subject", detail.Subject)
	table.AddRow("Issuer", detail.Issuer)
	table.AddRow("SANs", parseSANs(detail.SANs))
	table.AddRow("Thumbprint", detail.Thumbprint)
	table.AddRow("Serial Number", detail.SerialNumber)
	table.AddRow("Not Before", detail.NotBefore)
	table.AddRow("Not After", detail.NotAfter)
	table.AddRow("Content Type", detail.ContentType)
	table.AddRow("Source", detail.Source)
	table.AddRow("Created At", detail.CreatedAt)
	table.AddRow("Updated At", detail.UpdatedAt)

	payload := certGetPayload{CertificateDetail: *detail, Path: certPath}
	return u.Print(format, payload, table)
}

func runCertReveal(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	certID, _, err := resolveCertRef(vc, args[0])
	if err != nil {
		return err
	}

	revealed, err := vc.RevealCertificate(certID)
	if err != nil {
		return fmt.Errorf("failed to reveal certificate: %w", err)
	}

	fmt.Print(revealed.CertificateContent)
	if revealed.PrivateKeyContent != "" {
		fmt.Print(revealed.PrivateKeyContent)
	}
	return nil
}

func runCertUpdate(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	certID, _, err := resolveCertRef(vc, args[0])
	if err != nil {
		return err
	}

	certContent, err := os.ReadFile(certUpdateCertFile)
	if err != nil {
		return fmt.Errorf("failed to read certificate file %s: %w", certUpdateCertFile, err)
	}

	var keyContent string
	if certUpdateKeyFile != "" {
		keyBytes, err := os.ReadFile(certUpdateKeyFile)
		if err != nil {
			return fmt.Errorf("failed to read key file %s: %w", certUpdateKeyFile, err)
		}
		keyContent = string(keyBytes)
	}

	updated, err := vc.UpdateCertificate(certID, string(certContent), keyContent)
	if err != nil {
		return fmt.Errorf("failed to update certificate: %w", err)
	}

	fmt.Printf("Updated certificate %q (thumbprint: %s).\n", updated.Name, updated.Thumbprint)
	return nil
}

func runCertDelete(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	certID, certName, err := resolveCertRef(vc, args[0])
	if err != nil {
		return err
	}

	// Confirm deletion
	if !certDeleteYes {
		fmt.Printf("Are you sure you want to delete certificate %q? This cannot be undone. [y/N]: ", certName)
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

	if err := vc.DeleteCertificate(certID); err != nil {
		return fmt.Errorf("failed to delete certificate: %w", err)
	}

	fmt.Printf("Deleted certificate %q.\n", certName)
	return nil
}

func runCertHistory(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	certID, _, err := resolveCertRef(vc, args[0])
	if err != nil {
		return err
	}

	history, err := vc.GetCertificateHistory(certID)
	if err != nil {
		return fmt.Errorf("failed to get certificate history: %w", err)
	}

	if len(history) == 0 {
		fmt.Println("No history found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DATE\tCHANGE TYPE\tTHUMBPRINT\tEXPIRES\tCHANGED BY")
	for _, h := range history {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", h.CreatedAt, h.ChangeType, h.Thumbprint, h.NotAfter, h.ChangedBy)
	}
	return w.Flush()
}
