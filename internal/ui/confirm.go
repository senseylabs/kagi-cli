package ui

import (
	"fmt"
	"io"
	"strings"
)

// Confirm asks a yes/no question and reports whether the user affirmed. It
// accepts "y" or "yes" case-insensitively; anything else — including an empty
// line (the safe default) — is a no. The prompt is written to stderr with a
// "[y/N]: " suffix, and a declined or aborted prompt prints "Aborted." to
// stderr, matching the existing house wording.
//
// A genuine read failure (something other than a clean EOF) is surfaced on
// stderr rather than swallowed, then treated as a no. A piped answer with no
// trailing newline (which returns io.EOF alongside the data) is still honored.
func (u *UI) Confirm(prompt string) bool {
	fmt.Fprintf(u.err, "%s [y/N]: ", normalizeMessage(prompt))

	line, err := u.in.ReadString('\n')
	if err != nil && err != io.EOF {
		fmt.Fprintf(u.err, "could not read confirmation: %v\n", err)
		fmt.Fprintln(u.err, "Aborted.")
		return false
	}

	switch strings.TrimSpace(strings.ToLower(line)) {
	case "y", "yes":
		return true
	}

	fmt.Fprintln(u.err, "Aborted.")
	return false
}
