package cmd

import (
	"fmt"
	"strings"

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
	format, err := ui.ParseFormat(outputValue)
	if err != nil {
		return "", enrichFormatError(outputValue, err, false)
	}
	return format, nil
}

// looksLikeFilePath reports whether an --output value reads as a filename or
// path rather than a format name. Any directory separator counts, as does a dot
// anywhere in the final segment — which covers both "secrets.env" and the bare
// ".env" that no valid format name resembles.
func looksLikeFilePath(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	if strings.ContainsAny(v, `/\`) {
		return true
	}
	return strings.Contains(v, ".")
}

// enrichFormatError replaces the generic "invalid output format" error with a
// pointed one when the value is plainly a file path.
//
// Before v0.20.0, `--output` on `kagi pull` named an output FILE. It now names
// the payload FORMAT on every command, and the file flag is `--out-file`. An
// old invocation carried forward therefore fails on a value like
// "/tmp/app.env", and the bare format error does not explain why. hasOutFile
// tells the caller whether the command actually offers --out-file (only `pull`
// does); everywhere else the answer is to redirect stdout.
func enrichFormatError(value string, err error, hasOutFile bool) error {
	if !looksLikeFilePath(value) {
		return err
	}
	if hasOutFile {
		return fmt.Errorf(
			"--output/-o selects the output format (env, json, or yaml), not a file to write. "+
				"Since v0.20.0 the file flag is --out-file, so use: --out-file %s", value)
	}
	return fmt.Errorf(
		"--output/-o selects the output format (table, json, or yaml), not a file to write. "+
			"Redirect stdout instead, e.g. -o json > %s", value)
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
