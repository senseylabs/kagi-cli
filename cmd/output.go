package cmd

import (
	"github.com/senseylabs/kagi-cli/internal/ui"
)

// outputValue holds the --output/-o flag: the requested payload format
// (table|json|yaml). It is parsed into a ui.Format by outputFormat().
var outputValue string

// noColorValue holds the --no-color flag. When set, newUI() forces color off
// (ui.ColorNever); otherwise color is auto-detected from the output stream.
var noColorValue bool

func init() {
	rootCmd.PersistentFlags().StringVarP(&outputValue, "output", "o", "table",
		"Output format for payloads: table, json, or yaml")
	rootCmd.PersistentFlags().BoolVar(&noColorValue, "no-color", false,
		"Disable colored output")
}

// outputFormat parses the --output flag value into a ui.Format, returning an
// error for an unrecognized format.
func outputFormat() (ui.Format, error) {
	return ui.ParseFormat(outputValue)
}

// newUI returns a UI wired to the process's standard streams, honoring
// --no-color by mapping it to ui.ColorNever.
func newUI() *ui.UI {
	opts := ui.Options{}
	if noColorValue {
		opts.Color = ui.ColorNever
	}
	return ui.New(opts)
}
