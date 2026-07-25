package main

import "github.com/senseylabs/kagi-cli/cmd"

// Build metadata injected via -ldflags at build time (goreleaser and the
// Makefile set these). version is the "not a release build" sentinel; commit and
// date default to placeholders overridden by -X main.commit / -X main.date.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cmd.SetVersion(version)
	cmd.SetBuildInfo(commit, date)
	cmd.Execute()
}
