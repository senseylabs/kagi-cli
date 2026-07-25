package ui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The error convention (Go-idiomatic, enforced by the helpers here):
//   - lowercase first letter, unless the first word is an acronym (OIDC, HTTP)
//   - no trailing period
//   - wrap the cause with %w so errors.Is/As keep working up the stack
//   - an optional actionable "Run 'kagi ...'" hint on its own line

// Errorf builds a convention-conforming error. Use it in place of fmt.Errorf so
// the message is normalized (lowercase-first, no trailing period). It supports
// %w exactly like fmt.Errorf, so pass the cause with %w to keep it wrapped.
func Errorf(format string, args ...any) error {
	err := fmt.Errorf(format, args...)
	return &normalizedError{err: err, msg: normalizeErrorMessage(err.Error())}
}

// Wrapf wraps cause with a normalized context message. The result is
// "<context>: <cause>" and unwraps to cause.
func Wrapf(cause error, format string, args ...any) error {
	if cause == nil {
		return nil
	}
	ctx := normalizeErrorMessage(fmt.Sprintf(format, args...))
	return &normalizedError{err: fmt.Errorf("%s: %w", ctx, cause), msg: ctx + ": " + cause.Error()}
}

// WithHint attaches an actionable hint to err, rendered on its own line as
// "Run '<runCommand>'." (e.g. WithHint(err, "kagi login")). The hint is
// presentation only; errors.Is/As continue to see the wrapped cause.
func WithHint(err error, runCommand string) error {
	if err == nil {
		return nil
	}
	return &hintedError{err: err, run: strings.TrimSpace(runCommand)}
}

// normalizedError carries a pre-normalized message while still unwrapping to the
// original (possibly %w-wrapped) error for errors.Is/As.
type normalizedError struct {
	err error
	msg string
}

func (e *normalizedError) Error() string { return e.msg }
func (e *normalizedError) Unwrap() error { return e.err }

// hintedError appends a "Run '...'" line to its wrapped error's message.
type hintedError struct {
	err error
	run string
}

func (e *hintedError) Error() string {
	if e.run == "" {
		return e.err.Error()
	}
	return fmt.Sprintf("%s\nRun '%s'.", e.err.Error(), e.run)
}

func (e *hintedError) Unwrap() error { return e.err }

// normalizeErrorMessage lowercases the first letter (unless the first word is an
// acronym) and strips a trailing period and trailing whitespace.
func normalizeErrorMessage(s string) string {
	s = strings.TrimRight(s, " \t.")
	if s == "" {
		return s
	}
	if !firstWordIsAcronym(s) {
		r, size := utf8.DecodeRuneInString(s)
		if unicode.IsUpper(r) {
			s = string(unicode.ToLower(r)) + s[size:]
		}
	}
	return s
}
