// Package ui is the single voice for the Kagi CLI's terminal output: payload
// streaming, human-facing messaging, interactive prompts, formatted output
// (table/json/yaml), and error presentation.
//
// Two rules run through the whole package:
//
//   - stdout carries data only (payloads a script can consume); everything a
//     human reads — status lines, prompts, warnings, diagnostics — goes to
//     stderr, so `kagi ... -o json | jq` stays clean.
//   - color is applied to status lines and table headers only, never to data,
//     and is suppressed for NO_COLOR, an explicit --no-color, or a non-TTY.
//
// A UI value carries the streams and presentation settings for one command
// run. Construct it once (typically in the root command) and thread it through.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// ColorMode selects how color is resolved for a UI.
type ColorMode int

const (
	// ColorAuto enables color only on a TTY with NO_COLOR unset. This is the
	// zero value so a bare Options{} behaves sensibly.
	ColorAuto ColorMode = iota
	// ColorAlways forces color on (e.g. a deliberate override).
	ColorAlways
	// ColorNever forces color off (honors an explicit --no-color intent).
	ColorNever
)

// Options configures a UI. All fields are optional; the zero value targets the
// process's standard streams with auto-detected width and color.
type Options struct {
	Out io.Writer // payload stream; defaults to os.Stdout
	Err io.Writer // messaging/prompt stream; defaults to os.Stderr
	In  io.Reader // prompt input; defaults to os.Stdin

	// Color selects color resolution. ColorAuto (the default) enables color
	// only on a TTY with NO_COLOR unset. Pass ColorNever for --no-color.
	Color ColorMode

	// Width, when > 0, sets the terminal width in columns and marks Out as a
	// TTY for layout purposes (tables truncate to fit). When 0, the width and
	// TTY-ness are auto-detected from Out if it is an *os.File; a non-TTY (a
	// pipe, a redirect, or a plain buffer) renders full, untruncated columns.
	// This field is the seam tests use to exercise truncation deterministically.
	Width int
}

// UI carries the streams and presentation settings for one command run.
type UI struct {
	out io.Writer
	err io.Writer
	in  *bufio.Reader
	// inFile/errFile are the underlying *os.File for the input and messaging
	// streams when they are real files (nil for buffers/pipes used in tests).
	// The interactive picker needs them to detect a terminal and switch stdin to
	// raw mode; when either is nil or not a TTY, the picker uses its line-based
	// fallback instead.
	inFile  *os.File
	errFile *os.File
	color   bool
	isTTY   bool
	width   int
}

// New builds a UI from opts, auto-detecting width, TTY-ness, and color where
// opts leaves them unset.
func New(opts Options) *UI {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	errW := opts.Err
	if errW == nil {
		errW = os.Stderr
	}
	inR := opts.In
	if inR == nil {
		inR = os.Stdin
	}

	isTTY := false
	width := 0
	switch {
	case opts.Width > 0:
		// Explicit width: treat Out as a fixed-width TTY (used by tests and by
		// callers that want to pin the layout).
		isTTY = true
		width = opts.Width
	default:
		if f, ok := out.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
				isTTY = true
				width = w
			}
		}
	}

	inFile, _ := inR.(*os.File)
	errFile, _ := errW.(*os.File)

	return &UI{
		out:     out,
		err:     errW,
		in:      bufio.NewReader(inR),
		inFile:  inFile,
		errFile: errFile,
		color:   resolveColor(opts.Color, isTTY),
		isTTY:   isTTY,
		width:   width,
	}
}

// Default returns a UI wired to the process's standard streams with
// auto-detected width and color.
func Default() *UI { return New(Options{}) }

// Out returns the payload stream (stdout). Use it for raw writes that don't fit
// Data/Print; never write status text here.
func (u *UI) Out() io.Writer { return u.out }

// Err returns the messaging stream (stderr).
func (u *UI) Err() io.Writer { return u.err }

// IsTTY reports whether the payload stream is an interactive terminal (or a
// pinned width was supplied).
func (u *UI) IsTTY() bool { return u.isTTY }

// Width returns the resolved terminal width in columns (0 when not a TTY).
func (u *UI) Width() int { return u.width }

// ColorEnabled reports whether status lines and headers will be colored.
func (u *UI) ColorEnabled() bool { return u.color }

// Data writes a payload line to stdout. Use it for the values a script wants:
// a secret, a resolved id, a single field.
func (u *UI) Data(args ...any) { fmt.Fprintln(u.out, args...) }

// Dataf writes a formatted payload to stdout with no implicit newline.
func (u *UI) Dataf(format string, args ...any) { fmt.Fprintf(u.out, format, args...) }

// Status prints a neutral progress line to stderr (no glyph).
func (u *UI) Status(format string, args ...any) {
	u.message("", "", format, args...)
}

// Info prints an informational line to stderr (no glyph).
func (u *UI) Info(format string, args ...any) {
	u.message("", "", format, args...)
}

// Success prints a success line to stderr, prefixed with a check glyph and
// colored green when color is enabled.
func (u *UI) Success(format string, args ...any) {
	u.message("✓ ", colorGreen, format, args...)
}

// Warn prints a warning line to stderr, prefixed with a bang glyph and colored
// yellow when color is enabled.
func (u *UI) Warn(format string, args ...any) {
	u.message("! ", colorYellow, format, args...)
}

// Error prints an error to stderr as a red "Error: <message>" block. A
// multi-line message (e.g. one carrying a "Run 'kagi ...'" hint) is colored line
// by line so the reset never spans a newline. Casing and punctuation are left
// as-is: error strings are already normalized by the ui error helpers.
func (u *UI) Error(err error) {
	if err == nil {
		return
	}
	lines := strings.Split(err.Error(), "\n")
	lines[0] = "Error: " + lines[0]
	for _, ln := range lines {
		fmt.Fprintln(u.err, u.paint(colorRed, ln))
	}
}

// message renders one stderr line following the house convention: sentence-cased
// caller text (the caller owns casing) with trailing-period drift stripped, an
// optional glyph, and optional color applied to the whole line (glyph + text).
func (u *UI) message(glyph, color, format string, args ...any) {
	line := glyph + normalizeMessage(fmt.Sprintf(format, args...))
	if u.color && color != "" {
		line = color + line + colorReset
	}
	fmt.Fprintln(u.err, line)
}
