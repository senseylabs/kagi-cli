package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// newTestUI builds a UI over buffers with color off and an explicit width. A
// width > 0 marks the output as a fixed-width TTY (so tables truncate); width 0
// leaves it a non-TTY buffer (full, untruncated columns).
func newTestUI(t *testing.T, width int, stdin string) (*UI, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	u := New(Options{
		Out:   out,
		Err:   errBuf,
		In:    strings.NewReader(stdin),
		Color: ColorNever,
		Width: width,
	})
	return u, out, errBuf
}

func lines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// --- Table truncation --------------------------------------------------------

func TestTableTruncatesToWidth(t *testing.T) {
	const width = 40
	u, out, _ := newTestUI(t, width, "")

	tbl := NewTable("NAME", "ISSUER URL", "ID")
	tbl.SetTruncatable(1, 0) // URL shrinks first
	tbl.SetTruncatable(2, 1)
	longURL := "https://auth.example.com/realms/kagi/.well-known/openid-configuration"
	tbl.AddRow("prod-cluster", longURL, "3f2504e0-4f89-41d3-9a0c-0305e82c3301")

	if err := u.Render(tbl); err != nil {
		t.Fatalf("Render: %v", err)
	}

	got := out.String()
	rows := lines(got)
	if len(rows) != 2 { // header + one data row
		t.Fatalf("expected 2 lines, got %d:\n%s", len(rows), got)
	}
	for _, ln := range rows {
		if n := runeLen(ln); n > width {
			t.Errorf("line exceeds width %d (got %d): %q", width, n, ln)
		}
	}
	if !strings.Contains(got, "…") {
		t.Errorf("expected an ellipsis from truncation, got:\n%s", got)
	}
	if strings.Contains(got, longURL) {
		t.Errorf("full URL should have been truncated, got:\n%s", got)
	}
	if !strings.Contains(rows[0], "NAME") || !strings.Contains(rows[0], "ISSUER URL") {
		t.Errorf("headers not preserved: %q", rows[0])
	}
}

func TestTableFullWhenNonTTY(t *testing.T) {
	u, out, _ := newTestUI(t, 0, "") // width 0 + buffer => non-TTY

	tbl := NewTable("NAME", "ISSUER URL", "ID")
	tbl.SetTruncatable(1, 0)
	longURL := "https://auth.example.com/realms/kagi/.well-known/openid-configuration"
	tbl.AddRow("prod-cluster", longURL, "3f2504e0-4f89-41d3-9a0c-0305e82c3301")

	if err := u.Render(tbl); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, longURL) {
		t.Errorf("non-TTY output must be untruncated, got:\n%s", got)
	}
	if strings.Contains(got, "…") {
		t.Errorf("non-TTY output must not contain an ellipsis, got:\n%s", got)
	}
}

func TestTablePriorityShrinksFirst(t *testing.T) {
	// A=1, LOW=20, HIGH=20 → lineWidth = 1+20+20 + 2*2 = 45; width 40 → shave 5,
	// which fits entirely inside the lower-priority (LOW) column.
	const width = 40
	u, out, _ := newTestUI(t, width, "")

	low := strings.Repeat("L", 20)
	high := strings.Repeat("H", 20)
	tbl := NewTable("A", "LOW", "HIGH")
	tbl.SetTruncatable(1, 0)  // shrinks first
	tbl.SetTruncatable(2, 10) // shrinks later
	tbl.AddRow("x", low, high)

	if err := u.Render(tbl); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, high) {
		t.Errorf("high-priority column should be untouched, got:\n%s", got)
	}
	if strings.Contains(got, low) {
		t.Errorf("low-priority column should have been truncated, got:\n%s", got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("expected an ellipsis, got:\n%s", got)
	}
}

func TestTableHeaderColorOnlyOnHeader(t *testing.T) {
	out := &bytes.Buffer{}
	u := New(Options{Out: out, Err: &bytes.Buffer{}, Color: ColorAlways, Width: 80})

	tbl := NewTable("NAME", "VALUE")
	tbl.AddRow("secret", "s3cr3t")
	if err := u.Render(tbl); err != nil {
		t.Fatalf("Render: %v", err)
	}
	rows := lines(out.String())
	if len(rows) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(rows))
	}
	if !strings.Contains(rows[0], colorBoldCyan) {
		t.Errorf("header should be colored: %q", rows[0])
	}
	if strings.Contains(rows[1], "\x1b[") {
		t.Errorf("data row must never be colored: %q", rows[1])
	}
}

func TestErrorRendersRedBlock(t *testing.T) {
	errBuf := &bytes.Buffer{}
	u := New(Options{Out: &bytes.Buffer{}, Err: errBuf, Color: ColorAlways, Width: 80})

	u.Error(WithHint(errors.New("session expired"), "kagi login"))

	got := errBuf.String()
	rows := lines(got)
	if len(rows) != 2 {
		t.Fatalf("expected 2 lines (message + hint), got %d:\n%q", len(rows), got)
	}
	if !strings.HasPrefix(stripSGR(rows[0]), "Error: session expired") {
		t.Errorf("first line should be the Error block: %q", rows[0])
	}
	if !strings.Contains(stripSGR(rows[1]), "Run 'kagi login'") {
		t.Errorf("hint line missing: %q", rows[1])
	}
	// Each line must be independently colored red (reset never spans a newline).
	for i, ln := range rows {
		if !strings.HasPrefix(ln, colorRed) || !strings.HasSuffix(ln, colorReset) {
			t.Errorf("line %d not wrapped in red: %q", i, ln)
		}
	}
}

func TestErrorNoColorWhenDisabled(t *testing.T) {
	errBuf := &bytes.Buffer{}
	u := New(Options{Out: &bytes.Buffer{}, Err: errBuf, Color: ColorNever, Width: 80})
	u.Error(errors.New("boom"))
	if got := errBuf.String(); got != "Error: boom\n" {
		t.Errorf("plain error mismatch: %q", got)
	}
}

func TestErrorNilIsNoop(t *testing.T) {
	errBuf := &bytes.Buffer{}
	u := New(Options{Out: &bytes.Buffer{}, Err: errBuf, Color: ColorAlways, Width: 80})
	u.Error(nil)
	if errBuf.Len() != 0 {
		t.Errorf("nil error should print nothing, got %q", errBuf.String())
	}
}

// stripSGR removes ANSI SGR sequences so assertions can match on visible text.
func stripSGR(s string) string {
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+j+1:]
	}
}

// --- Format switching --------------------------------------------------------

func TestPrintFormatSwitching(t *testing.T) {
	type payload struct {
		Name    string `json:"name" yaml:"name"`
		Enabled bool   `json:"enabled" yaml:"enabled"`
	}
	data := payload{Name: "prod", Enabled: true}
	tbl := NewTable("NAME", "ENABLED").AddRow("prod", "true")

	tests := []struct {
		format Format
		want   []string
	}{
		{FormatJSON, []string{`"name": "prod"`, `"enabled": true`}},
		{FormatYAML, []string{"name: prod", "enabled: true"}},
		{FormatTable, []string{"NAME", "prod"}},
	}
	for _, tc := range tests {
		u, out, errBuf := newTestUI(t, 80, "")
		if err := u.Print(tc.format, data, tbl); err != nil {
			t.Fatalf("Print(%s): %v", tc.format, err)
		}
		got := out.String()
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("Print(%s) stdout = %q, want substring %q", tc.format, got, want)
			}
		}
		if errBuf.Len() != 0 {
			t.Errorf("Print(%s) wrote to stderr: %q", tc.format, errBuf.String())
		}
	}
}

func TestParseFormat(t *testing.T) {
	ok := map[string]Format{
		"":      FormatTable,
		"table": FormatTable,
		"JSON":  FormatJSON,
		" yaml": FormatYAML,
	}
	for in, want := range ok {
		got, err := ParseFormat(in)
		if err != nil {
			t.Errorf("ParseFormat(%q) error: %v", in, err)
		}
		if got != want {
			t.Errorf("ParseFormat(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Error("ParseFormat(xml) should error")
	}
}

// --- Confirm -----------------------------------------------------------------

func TestConfirm(t *testing.T) {
	tests := []struct {
		in       string
		want     bool
		wantErrS string
	}{
		{"y\n", true, ""},
		{"Y\n", true, ""},
		{"yes\n", true, ""},
		{"YES\n", true, ""},
		{"  yes  \n", true, ""},
		{"y", true, ""}, // piped, no trailing newline (EOF alongside data)
		{"n\n", false, "Aborted."},
		{"\n", false, "Aborted."},
		{"nope\n", false, "Aborted."},
		{"", false, "Aborted."}, // empty stdin / EOF
	}
	for _, tc := range tests {
		u, out, errBuf := newTestUI(t, 0, tc.in)
		got := u.Confirm("Delete secret foo?")
		if got != tc.want {
			t.Errorf("Confirm(in=%q) = %v, want %v", tc.in, got, tc.want)
		}
		if !strings.Contains(errBuf.String(), "[y/N]:") {
			t.Errorf("Confirm(in=%q) prompt missing from stderr: %q", tc.in, errBuf.String())
		}
		if tc.wantErrS != "" && !strings.Contains(errBuf.String(), tc.wantErrS) {
			t.Errorf("Confirm(in=%q) stderr = %q, want %q", tc.in, errBuf.String(), tc.wantErrS)
		}
		if out.Len() != 0 {
			t.Errorf("Confirm(in=%q) wrote to stdout: %q", tc.in, out.String())
		}
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("device not ready") }

func TestConfirmReadError(t *testing.T) {
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	u := New(Options{Out: out, Err: errBuf, In: errReader{}, Color: ColorNever})

	if u.Confirm("Proceed?") {
		t.Error("Confirm should be false on a read error")
	}
	if !strings.Contains(errBuf.String(), "could not read confirmation") {
		t.Errorf("read error not surfaced: %q", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "Aborted.") {
		t.Errorf("expected Aborted. after read error: %q", errBuf.String())
	}
}

// --- Messaging ---------------------------------------------------------------

func TestMessagingRoutingAndConvention(t *testing.T) {
	u, out, errBuf := newTestUI(t, 0, "")

	u.Data("payload-value")
	if got := out.String(); got != "payload-value\n" {
		t.Errorf("Data stdout = %q", got)
	}
	if errBuf.Len() != 0 {
		t.Errorf("Data leaked to stderr: %q", errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	u.Success("Secret created.") // trailing period must be stripped
	u.Warn("Using plaintext store.")
	u.Info("Nothing to do.")
	if out.Len() != 0 {
		t.Errorf("messaging leaked to stdout: %q", out.String())
	}
	got := errBuf.String()
	for _, want := range []string{"✓ Secret created\n", "! Using plaintext store\n", "Nothing to do\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "created.\n") || strings.Contains(got, "store.\n") {
		t.Errorf("trailing period not stripped: %q", got)
	}
}

func TestMessagingColor(t *testing.T) {
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	u := New(Options{Out: out, Err: errBuf, Color: ColorAlways})
	u.Success("done")
	got := errBuf.String()
	if !strings.HasPrefix(got, colorGreen) || !strings.Contains(got, colorReset) {
		t.Errorf("Success not green: %q", got)
	}
}

// --- Color resolution --------------------------------------------------------

func TestResolveColor(t *testing.T) {
	if resolveColor(ColorAlways, false) != true {
		t.Error("ColorAlways should force on")
	}
	if resolveColor(ColorNever, true) != false {
		t.Error("ColorNever should force off")
	}
	if resolveColor(ColorAuto, false) != false {
		t.Error("ColorAuto off a TTY should be off")
	}
	t.Setenv("NO_COLOR", "1")
	if resolveColor(ColorAuto, true) != false {
		t.Error("NO_COLOR must disable ColorAuto even on a TTY")
	}
}

// --- Error helpers -----------------------------------------------------------

func TestErrorfConvention(t *testing.T) {
	err := Errorf("Failed to reach backend.")
	if got := err.Error(); got != "failed to reach backend" {
		t.Errorf("Errorf normalize = %q", got)
	}
	// Acronym-leading messages keep their case.
	if got := Errorf("OIDC discovery failed.").Error(); got != "OIDC discovery failed" {
		t.Errorf("acronym message = %q", got)
	}
}

func TestWrapfUnwraps(t *testing.T) {
	sentinel := errors.New("connection refused")
	err := Wrapf(sentinel, "Reach backend")
	if !errors.Is(err, sentinel) {
		t.Error("Wrapf must keep the cause unwrappable")
	}
	if got := err.Error(); got != "reach backend: connection refused" {
		t.Errorf("Wrapf message = %q", got)
	}
	if Wrapf(nil, "x") != nil {
		t.Error("Wrapf(nil) should be nil")
	}
}

func TestWithHint(t *testing.T) {
	base := Errorf("not authenticated")
	err := WithHint(base, "kagi login")
	if !errors.Is(err, base) {
		t.Error("WithHint must keep the cause unwrappable")
	}
	if got := err.Error(); got != "not authenticated\nRun 'kagi login'." {
		t.Errorf("WithHint message = %q", got)
	}
	if WithHint(nil, "kagi login") != nil {
		t.Error("WithHint(nil) should be nil")
	}
}
