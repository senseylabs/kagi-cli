package ui

import (
	"os"
	"strings"
	"unicode"
)

// ANSI SGR codes. Kept minimal on purpose: color is a signal on status lines
// and headers, not decoration on data.
const (
	colorReset  = "\x1b[0m"
	colorBold   = "\x1b[1m"
	colorGreen  = "\x1b[32m"
	colorYellow = "\x1b[33m"
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
