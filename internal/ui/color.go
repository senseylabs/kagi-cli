package ui

import (
	"os"
	"strings"
	"unicode"
)

// ANSI SGR codes. We deliberately use the basic 16-color set (30–37) and the
// standard attributes rather than 256-color or truecolor escapes: the basic
// codes are remapped by the terminal to the user's chosen palette, so output
// respects whatever light/dark theme is in effect instead of hard-coding RGB
// values that clash with it. Color is a signal (status, headers, folders,
// selection), never decoration on the data a script consumes.
const (
	colorReset     = "\x1b[0m"
	colorBold      = "\x1b[1m"
	colorDim       = "\x1b[2m"
	colorReverse   = "\x1b[7m"
	colorRed       = "\x1b[31m"
	colorGreen     = "\x1b[32m"
	colorYellow    = "\x1b[33m"
	colorBlue      = "\x1b[34m"
	colorCyan      = "\x1b[36m"
	colorBoldCyan  = "\x1b[1;36m"
	colorBoldGreen = "\x1b[1;32m"
)

// resolveColor turns a ColorMode plus TTY-ness into a concrete decision,
// honoring the NO_COLOR convention (https://no-color.org) under ColorAuto.
func resolveColor(mode ColorMode, isTTY bool) bool {
	switch mode {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	default: // ColorAuto
		if _, ok := os.LookupEnv("NO_COLOR"); ok {
			return false
		}
		return isTTY
	}
}

// paint wraps s in an SGR code when color is enabled, else returns s unchanged.
func (u *UI) paint(code, s string) string {
	if !u.color || code == "" {
		return s
	}
	return code + s + colorReset
}

// Styling helpers used by the interactive picker and tables. Each is a no-op
// when color is disabled, so callers never need to branch on u.color.

// styleFolder renders a folder name so it reads as navigable (bold cyan).
func (u *UI) styleFolder(s string) string { return u.paint(colorBoldCyan, s) }

// styleDim renders secondary/supporting text (paths, hints) faintly.
func (u *UI) styleDim(s string) string { return u.paint(colorDim, s) }

// styleSelected renders the highlighted picker row as a reverse-video bar,
// which inverts against the terminal's own fg/bg and so tracks its theme.
func (u *UI) styleSelected(s string) string { return u.paint(colorReverse, s) }

// normalizeMessage enforces the status-line convention: no trailing period and
// no trailing whitespace, so callers can't drift into "Done." vs "Done" vs
// "Done ". Casing is left to the caller (auto-lowercasing would mangle proper
// nouns like OIDC or Keycloak); the convention is sentence case.
func normalizeMessage(s string) string {
	return strings.TrimRight(s, " \t.")
}

// firstWordIsAcronym reports whether the first whitespace-delimited word of s
// is all uppercase letters (e.g. "OIDC", "HTTP"), which must not be lowercased.
func firstWordIsAcronym(s string) bool {
	i := strings.IndexFunc(s, unicode.IsSpace)
	word := s
	if i >= 0 {
		word = s[:i]
	}
	hasLetter := false
	for _, r := range word {
		if unicode.IsLetter(r) {
			hasLetter = true
			if !unicode.IsUpper(r) {
				return false
			}
		}
	}
	return hasLetter
}
