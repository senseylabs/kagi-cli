package main

import "github.com/senseylabs/kagi-cli/cmd"

// version is the "not a release build" sentinel. Release builds override it via
// -ldflags "-X main.version=..." (goreleaser injects the git tag).
var version = "dev"

func main() {
	cmd.SetVersion(version)
	cmd.Execute()
}
