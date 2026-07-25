package cmd

import (
	"runtime"

	"github.com/spf13/cobra"
)

// appCommit and appDate hold build metadata injected via -ldflags at build time
// (see the Makefile and .goreleaser.yaml). They are surfaced by `kagi version`.
// The plain `--version` output (rootCmd.Version) is deliberately left unchanged,
// as install.sh/install.ps1 parse it.
var (
	appCommit = "none"
	appDate   = "unknown"
)

// SetBuildInfo records the build commit and date injected at link time. Empty
// values are ignored so the "none"/"unknown" fallbacks survive a dev build.
func SetBuildInfo(commit, date string) {
	if commit != "" {
		appCommit = commit
	}
	if date != "" {
		appDate = date
	}
}

var versionCmd = &cobra.Command{
	Use:     "version",
	Short:   "Print the version, commit, and build date",
	Long:    "Print detailed build information: the version, the git commit it was built from, the build date, and the Go toolchain version.",
	Example: "  kagi version",
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		v := appVersion
		if v == "" {
			v = "dev"
		}
		u := newUI()
		u.Dataf("kagi %s\n", v)
		u.Dataf("commit: %s\n", appCommit)
		u.Dataf("date:   %s\n", appDate)
		u.Dataf("go:     %s\n", runtime.Version())
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
