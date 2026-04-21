package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/spf13/cobra"
)

var certBindingCmd = &cobra.Command{
	Use:   "binding",
	Short: "Manage certificate bindings",
}

var certBindingListCmd = &cobra.Command{
	Use:   "list <CERT_NAME_OR_ID>",
	Short: "List bindings for a certificate",
	Args:  cobra.ExactArgs(1),
	RunE:  runCertBindingList,
}

var certBindingCreateCmd = &cobra.Command{
	Use:   "create <CERT_NAME_OR_ID>",
	Short: "Bind a certificate to a project/app/environment",
	Args:  cobra.ExactArgs(1),
	RunE:  runCertBindingCreate,
}

var certBindingDeleteYes bool

var certBindingDeleteCmd = &cobra.Command{
	Use:   "delete <CERT_NAME_OR_ID> <BINDING_ID>",
	Short: "Delete a certificate binding",
	Args:  cobra.ExactArgs(2),
	RunE:  runCertBindingDelete,
}

func init() {
	addProjectAppEnvFlags(certBindingCreateCmd)

	certBindingDeleteCmd.Flags().BoolVarP(&certBindingDeleteYes, "yes", "y", false, "Skip confirmation prompt")

	certBindingCmd.AddCommand(certBindingListCmd)
	certBindingCmd.AddCommand(certBindingCreateCmd)
	certBindingCmd.AddCommand(certBindingDeleteCmd)
	certCmd.AddCommand(certBindingCmd)
}

func runCertBindingList(cmd *cobra.Command, args []string) error {
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

	bindings, err := vc.ListCertificateBindings(cert.ID)
	if err != nil {
		return fmt.Errorf("failed to list certificate bindings: %w", err)
	}

	if len(bindings) == 0 {
		fmt.Println("No bindings found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tAPP\tENVIRONMENT")
	for _, b := range bindings {
		fmt.Fprintf(w, "%s\t%s\t%s\n", b.ProjectName, b.AppName, b.EnvironmentName)
	}
	return w.Flush()
}

func runCertBindingCreate(cmd *cobra.Command, args []string) error {
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

	ctx, err := resolveProjectAppEnv(cmd, vc)
	if err != nil {
		return err
	}

	binding, err := vc.CreateCertificateBinding(cert.ID, ctx.ProjectID, ctx.AppID, ctx.EnvID)
	if err != nil {
		return fmt.Errorf("failed to create certificate binding: %w", err)
	}

	fmt.Printf("Bound certificate %q to %s/%s/%s.\n", cert.Name, binding.ProjectName, binding.AppName, binding.EnvironmentName)
	return nil
}

func runCertBindingDelete(cmd *cobra.Command, args []string) error {
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

	bindingID := args[1]

	// Confirm deletion
	if !certBindingDeleteYes {
		fmt.Printf("Are you sure you want to delete binding %q from certificate %q? [y/N]: ", bindingID, cert.Name)
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

	if err := vc.DeleteCertificateBinding(cert.ID, bindingID); err != nil {
		return fmt.Errorf("failed to delete certificate binding: %w", err)
	}

	fmt.Printf("Deleted binding %q from certificate %q.\n", bindingID, cert.Name)
	return nil
}
