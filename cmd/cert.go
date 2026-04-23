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
	Use:   "cert",
	Short: "Manage certificates",
}

var certListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all certificates",
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

func runCertList(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	certs, err := vc.ListCertificates()
	if err != nil {
		return fmt.Errorf("failed to list certificates: %w", err)
	}

	if len(certs) == 0 {
		fmt.Println("No certificates found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSUBJECT\tDOMAINS\tEXPIRES\tSOURCE")
	for _, c := range certs {
		domains := parseSANs(c.SANs)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Name, c.Subject, domains, c.NotAfter, c.Source)
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

	cert, err := findCertificate(vc, args[0])
	if err != nil {
		return err
	}

	detail, err := vc.GetCertificateDetail(cert.ID)
	if err != nil {
		return fmt.Errorf("failed to get certificate details: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Name:\t%s\n", detail.Name)
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

	cert, err := findCertificate(vc, args[0])
	if err != nil {
		return err
	}

	revealed, err := vc.RevealCertificate(cert.ID)
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

	cert, err := findCertificate(vc, args[0])
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

	updated, err := vc.UpdateCertificate(cert.ID, string(certContent), keyContent)
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

	cert, err := findCertificate(vc, args[0])
	if err != nil {
		return err
	}

	// Confirm deletion
	if !certDeleteYes {
		fmt.Printf("Are you sure you want to delete certificate %q? This cannot be undone. [y/N]: ", cert.Name)
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

	if err := vc.DeleteCertificate(cert.ID); err != nil {
		return fmt.Errorf("failed to delete certificate: %w", err)
	}

	fmt.Printf("Deleted certificate %q.\n", cert.Name)
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

	cert, err := findCertificate(vc, args[0])
	if err != nil {
		return err
	}

	history, err := vc.GetCertificateHistory(cert.ID)
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
