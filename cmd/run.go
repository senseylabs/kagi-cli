package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/senseylabs/kagi-cli/internal/client"
	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:                "run -- <command> [args...]",
	Short:              "Run a command with secrets injected as environment variables",
	Long:               "Fetches secrets from Kagi and injects them as environment variables into the specified command.",
	DisableFlagParsing: false,
	RunE:               runRun,
}

func init() {
	addSecretFlags(runCmd)
	rootCmd.AddCommand(runCmd)
}

func runRun(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}

	// Find the command args after "--"
	cmdArgs := args
	if cmd.ArgsLenAtDash() >= 0 {
		cmdArgs = args[cmd.ArgsLenAtDash():]
	}

	if len(cmdArgs) == 0 {
		return fmt.Errorf("no command specified. Usage: kagi run -- <command> [args...]")
	}

	vc, err := client.NewKagiClient(cfgAPIURL, cfgIssuer)
	if err != nil {
		return err
	}

	// run opts into the personal fallback: if --personal is passed but the app
	// has no personal environment, fall back to the kagi.yaml environment.
	ctx, err := resolveAppEnvWith(cmd, vc, resolveOpts{allowPersonalFallback: true})
	if err != nil {
		return err
	}

	// Fetch all secrets
	secrets, err := vc.FetchSecrets(ctx.AppID, ctx.EnvSlug)
	if err != nil {
		return fmt.Errorf("failed to fetch secrets: %w", err)
	}

	// Build environment: current env + fetched secrets (secrets override)
	env := os.Environ()
	for k, v := range secrets {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// Execute the command
	childCmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	childCmd.Env = env
	childCmd.Stdin = os.Stdin
	childCmd.Stdout = os.Stdout
	childCmd.Stderr = os.Stderr

	// Forward signals to the child process
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for sig := range sigCh {
			if childCmd.Process != nil {
				_ = childCmd.Process.Signal(sig)
			}
		}
	}()

	if err := childCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("failed to run command: %w", err)
	}

	return nil
}
