package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/spf13/cobra"
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
	RunE:  runCertList,
}

var (
	certCreateName     string
	certCreateCertFile string
	certCreateKeyFile  string
)

var certCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new certificate",
	RunE:  runCertCreate,
}

var certGetCmd = &cobra.Command{
	Use:   "get <NAME_OR_ID>",
	Short: "Get certificate details",
	Args:  cobra.ExactArgs(1),
	RunE:  runCertGet,
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
	// Try exact name match (case-insensitive)
	for i, c := range certs {
		if strings.EqualFold(c.Name, nameOrID) {
			return &certs[i], nil
		}
	}
	// Try slug match
	for i, c := range certs {
		if strings.EqualFold(c.Slug, nameOrID) {
			return &certs[i], nil
		}
	}
	// Try ID prefix match
	for i, c := range certs {
		if strings.HasPrefix(c.ID, nameOrID) {
			return &certs[i], nil
		}
	}
	return nil, fmt.Errorf("certificate %q not found", nameOrID)
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

func runCertList(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

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
		fmt.Println("No certificates found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPATH\tDOMAINS\tEXPIRES\tSOURCE")
	for _, e := range entries {
		domains := parseSANs(e.cert.SANs)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.cert.Name, e.path, domains, e.cert.NotAfter, e.cert.Source)
	}
	return w.Flush()
}

func parseSANs(sans string) string {
	if sans == "" {
		return ""
	}
	var domains []string
	if err := json.Unmarshal([]byte(sans), &domains); err != nil {
		return sans
	}
	return strings.Join(domains, ",")
}

func runCertCreate(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

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

	cert, err := vc.CreateCertificate(certCreateName, string(certContent), keyContent)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	fmt.Printf("Created certificate %q (thumbprint: %s).\n", cert.Name, cert.Thumbprint)
	return nil
}

func runCertGet(cmd *cobra.Command, args []string) error {
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

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Name:\t%s\n", detail.Name)
	fmt.Fprintf(w, "ID:\t%s\n", detail.ID)
	if certPath != "" {
		fmt.Fprintf(w, "Path:\t%s\n", certPath)
	}
	fmt.Fprintf(w, "Subject:\t%s\n", detail.Subject)
	fmt.Fprintf(w, "Issuer:\t%s\n", detail.Issuer)
	fmt.Fprintf(w, "SANs:\t%s\n", parseSANs(detail.SANs))
	fmt.Fprintf(w, "Thumbprint:\t%s\n", detail.Thumbprint)
	fmt.Fprintf(w, "Serial Number:\t%s\n", detail.SerialNumber)
	fmt.Fprintf(w, "Not Before:\t%s\n", detail.NotBefore)
	fmt.Fprintf(w, "Not After:\t%s\n", detail.NotAfter)
	fmt.Fprintf(w, "Content Type:\t%s\n", detail.ContentType)
	fmt.Fprintf(w, "Source:\t%s\n", detail.Source)
	fmt.Fprintf(w, "Created At:\t%s\n", detail.CreatedAt)
	fmt.Fprintf(w, "Updated At:\t%s\n", detail.UpdatedAt)
	return w.Flush()
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
