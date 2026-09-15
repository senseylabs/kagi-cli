// github.com/senseylabs/kagi-cli no longer carries Go source. The Kagi CLI is
// developed in a private repository; this module path only distributes release
// binaries: https://github.com/senseylabs/kagi-cli/releases
module github.com/senseylabs/kagi-cli

go 1.25

// Source moved to a private repository. Install the CLI with
// `brew install senseylabs/tap/kagi-cli` or from the GitHub release assets.
retract [v0.0.0, v1.999.0]
