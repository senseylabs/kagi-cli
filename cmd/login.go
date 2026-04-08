package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/senseylabs/kagi-cli/internal/auth"
	"github.com/spf13/cobra"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with Keycloak via Device Authorization Grant",
	Long:  "Starts a Keycloak Device Authorization Grant flow. Opens your browser to complete authentication.",
	RunE:  runLogin,
}

func init() {
	rootCmd.AddCommand(loginCmd)
}

func runLogin(cmd *cobra.Command, args []string) error {
	if cfgDevMode {
		fmt.Println("Using local development URLs")
	}

	deviceFlow := auth.NewDeviceFlow(cfgIssuer, "kagi-cli", "openid")

	// Step 1: Discover OIDC endpoints
	fmt.Println("Discovering Keycloak endpoints...")
	endpoints, err := deviceFlow.DiscoverEndpoints()
	if err != nil {
		return fmt.Errorf("failed to discover OIDC endpoints: %w", err)
	}

	// Step 2: Request device authorization
	deviceResp, err := deviceFlow.RequestDeviceAuthorization(endpoints.DeviceAuthorizationEndpoint)
	if err != nil {
		return fmt.Errorf("failed to start device authorization: %w", err)
	}

	// Step 3: Display instructions and try to open browser
	fmt.Println()
	fmt.Printf("Open this URL in your browser: %s\n", deviceResp.VerificationURIComplete)
	fmt.Printf("Enter code: %s\n", deviceResp.UserCode)
	fmt.Println()

	if deviceResp.VerificationURIComplete != "" {
		openBrowser(deviceResp.VerificationURIComplete)
	}

	// Step 4: Poll for token
	fmt.Println("Waiting for authentication...")
	interval := time.Duration(deviceResp.Interval) * time.Second
	if interval == 0 {
		interval = 5 * time.Second
	}
	expiresAt := time.Now().Add(time.Duration(deviceResp.ExpiresIn) * time.Second)

	tokenResp, err := deviceFlow.PollForToken(endpoints.TokenEndpoint, deviceResp.DeviceCode, interval, expiresAt)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	fmt.Println("Authentication successful!")

	// Step 5: Store credentials
	store := auth.NewTokenStore()
	creds := auth.Credentials{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second),
		IssuerURL:    cfgIssuer,
		APIURL:       cfgAPIURL,
		DevMode:      cfgDevMode,
	}

	if err := store.Save(creds); err != nil {
		return fmt.Errorf("failed to store credentials: %w", err)
	}

	fmt.Println()
	fmt.Println("Login successful!")
	fmt.Printf("API: %s\n", cfgAPIURL)

	return nil
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Could not open browser automatically. Please open the URL manually.\n")
	}
}
