package ui

import (
	"strings"
	"testing"
)

// sampleItems returns a mixed folder/leaf set (deliberately out of folders-first
// order) so tests exercise the stable folders-first reordering too.
func sampleItems() []PickItem {
	return []PickItem{
		{Label: "app-web", Secondary: "/shared/app-web"},
		{Label: "alpha", IsFolder: true},
		{Label: "app-db", Secondary: "/shared/app-db"},
		{Label: "beta", IsFolder: true},
		{Label: "widget", Secondary: "/shared/widget"},
	}
}

func TestPickFilterThenNumberSelects(t *testing.T) {
	// Ordered folders-first: [alpha, beta, app-web, app-db, widget].
	// Filter "app" -> [app-web, app-db] renumbered 1,2; "2" picks app-db.
	u, out, _ := newTestUI(t, 80, "app\n2\n")

	res, err := u.Pick("/shared", sampleItems(), PickOptions{AllowUp: true})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if res.Kind != PickSelected {
		t.Fatalf("Kind = %v, want PickSelected", res.Kind)
	}
	if res.Item.Label != "app-db" {
		t.Errorf("selected %q, want app-db", res.Item.Label)
	}
	if out.Len() != 0 {
		t.Errorf("Pick wrote to stdout: %q", out.String())
	}
}

func TestPickFilterRenumbers(t *testing.T) {
	// Filter "db" narrows to a single leaf, which must become item 1.
	u, _, _ := newTestUI(t, 80, "db\n1\n")

	res, err := u.Pick("/shared", sampleItems(), PickOptions{})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if res.Kind != PickSelected || res.Item.Label != "app-db" {
		t.Fatalf("got (%v,%q), want (PickSelected,app-db)", res.Kind, res.Item.Label)
	}
}

func TestPickBlankClearsFilter(t *testing.T) {
	// Filter "app" narrows to [app-web, app-db]; a blank line clears the filter
	// back to the full folders-first list [alpha, beta, app-web, app-db, widget];
	// "1" then selects alpha (the first folder), proving the list was restored.
	u, _, _ := newTestUI(t, 80, "app\n\n1\n")

	res, err := u.Pick("/shared", sampleItems(), PickOptions{})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if res.Kind != PickSelected || res.Item.Label != "alpha" {
		t.Fatalf("got (%v,%q), want (PickSelected,alpha) — blank should clear the filter", res.Kind, res.Item.Label)
	}
}

func TestPickNumberSelectsFolderFirst(t *testing.T) {
	// With no filter, item 1 is the first folder after folders-first ordering.
	u, _, _ := newTestUI(t, 80, "1\n")

	res, err := u.Pick("/shared", sampleItems(), PickOptions{})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if res.Kind != PickSelected || !res.Item.IsFolder || res.Item.Label != "alpha" {
		t.Fatalf("got (%v,%q,folder=%v), want alpha folder", res.Kind, res.Item.Label, res.Item.IsFolder)
	}
}

func TestPickGoUp(t *testing.T) {
	u, _, _ := newTestUI(t, 80, "..\n")

	res, err := u.Pick("/shared", sampleItems(), PickOptions{AllowUp: true})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if res.Kind != PickGoUp {
		t.Errorf("Kind = %v, want PickGoUp", res.Kind)
	}
}

func TestPickGoUpDisabledFallsToFilter(t *testing.T) {
	// With AllowUp false, ".." is not a command; it becomes a filter that matches
	// nothing, so the next "q" quits.
	u, _, errBuf := newTestUI(t, 80, "..\nq\n")

	res, err := u.Pick("/shared", sampleItems(), PickOptions{AllowUp: false})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if res.Kind != PickQuit {
		t.Errorf("Kind = %v, want PickQuit", res.Kind)
	}
	if !strings.Contains(errBuf.String(), "(no matches)") {
		t.Errorf("expected a no-matches render after filtering by %q, got:\n%s", "..", errBuf.String())
	}
}

func TestPickQuit(t *testing.T) {
	u, _, _ := newTestUI(t, 80, "q\n")

	res, err := u.Pick("/shared", sampleItems(), PickOptions{})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if res.Kind != PickQuit {
		t.Errorf("Kind = %v, want PickQuit", res.Kind)
	}
}

func TestPickEOFQuits(t *testing.T) {
	// Empty stdin => immediate EOF => quit.
	u, _, _ := newTestUI(t, 80, "")

	res, err := u.Pick("/shared", sampleItems(), PickOptions{})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if res.Kind != PickQuit {
		t.Errorf("Kind = %v, want PickQuit", res.Kind)
	}
}

func TestPickEmptyLineReRenders(t *testing.T) {
	// A blank line re-renders (does not quit); the following "2" then selects.
	u, _, errBuf := newTestUI(t, 80, "\n2\n")

	res, err := u.Pick("/shared", sampleItems(), PickOptions{})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if res.Kind != PickSelected || res.Item.Label != "beta" {
		t.Fatalf("got (%v,%q), want (PickSelected,beta)", res.Kind, res.Item.Label)
	}
	// The list (its title) must have been rendered at least twice.
	if got := strings.Count(errBuf.String(), "/shared"); got < 2 {
		t.Errorf("title rendered %d times, want >= 2 (a re-render):\n%s", got, errBuf.String())
	}
}

func TestPickReadErrorSurfaced(t *testing.T) {
	// errReader is defined in ui_test.go (same package): every Read fails.
	out := &strings.Builder{}
	errBuf := &strings.Builder{}
	u := New(Options{Out: out, Err: errBuf, In: errReader{}, Color: ColorNever, Width: 80})

	_, err := u.Pick("/shared", sampleItems(), PickOptions{})
	if err == nil {
		t.Fatal("Pick should return the stdin read error, got nil")
	}
	if !strings.Contains(err.Error(), "device not ready") {
		t.Errorf("read error not surfaced: %v", err)
	}
}

// --- Interactive-picker pure helpers ----------------------------------------

func TestScrollWindow(t *testing.T) {
	cases := []struct {
		name               string
		cursor, n, size    int
		wantStart, wantEnd int
	}{
		{"fits entirely", 0, 3, 10, 0, 3},
		{"cursor at top scrolled", 0, 20, 6, 0, 6},
		{"cursor centered", 10, 20, 6, 7, 13},
		{"cursor at bottom clamps end", 19, 20, 6, 14, 20},
		{"exact fit", 4, 6, 6, 0, 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end := scrollWindow(c.cursor, c.n, c.size)
			if start != c.wantStart || end != c.wantEnd {
				t.Errorf("scrollWindow(%d,%d,%d) = (%d,%d), want (%d,%d)",
					c.cursor, c.n, c.size, start, end, c.wantStart, c.wantEnd)
			}
			// The window must always contain the cursor and never exceed size.
			if end-start > c.size {
				t.Errorf("window %d wider than size %d", end-start, c.size)
			}
			if c.n > 0 && (c.cursor < start || c.cursor >= end) {
				t.Errorf("cursor %d not in window [%d,%d)", c.cursor, start, end)
			}
		})
	}
}

func TestClamp(t *testing.T) {
	if got := clamp(5, 0, 3); got != 3 {
		t.Errorf("clamp(5,0,3) = %d, want 3", got)
	}
	if got := clamp(-1, 0, 3); got != 0 {
		t.Errorf("clamp(-1,0,3) = %d, want 0", got)
	}
	if got := clamp(2, 0, 3); got != 2 {
		t.Errorf("clamp(2,0,3) = %d, want 2", got)
	}
	// Empty list (hi < lo) pins to lo, so an empty filtered list never indexes.
	if got := clamp(0, 0, -1); got != 0 {
		t.Errorf("clamp on empty list = %d, want 0", got)
	}
}

func TestTrimLastRune(t *testing.T) {
	if got := trimLastRune("abc"); got != "ab" {
		t.Errorf("trimLastRune(abc) = %q, want ab", got)
	}
	if got := trimLastRune(""); got != "" {
		t.Errorf("trimLastRune(empty) = %q, want empty", got)
	}
	// Multibyte: must drop one rune, not one byte.
	if got := trimLastRune("café"); got != "caf" {
		t.Errorf("trimLastRune(café) = %q, want caf", got)
	}
}

func TestClipToWidth(t *testing.T) {
	if got := clipToWidth("hello", 3); got != "hel" {
		t.Errorf("clipToWidth(hello,3) = %q, want hel", got)
	}
	if got := clipToWidth("hi", 5); got != "hi" {
		t.Errorf("clipToWidth(hi,5) = %q, want hi", got)
	}
	if got := clipToWidth("hi", 0); got != "" {
		t.Errorf("clipToWidth(hi,0) = %q, want empty", got)
	}
}

func TestPickerHint(t *testing.T) {
	// Nav mode with go-up available: lists the vim keys and the h-up legend.
	up := pickerHint(false, true)
	if !strings.Contains(up, "j/k move") || !strings.Contains(up, "/ filter") {
		t.Errorf("nav hint missing vim/filter legend: %q", up)
	}
	if !strings.Contains(up, "h up") {
		t.Errorf("nav hint with AllowUp missing up legend: %q", up)
	}
	// Nav mode without go-up: no up legend.
	noUp := pickerHint(false, false)
	if strings.Contains(noUp, "up") {
		t.Errorf("nav hint without AllowUp should not mention up: %q", noUp)
	}
	if !strings.Contains(noUp, "q quit") {
		t.Errorf("nav hint missing quit legend: %q", noUp)
	}
	// Filter mode: explains apply/cancel regardless of AllowUp.
	filt := pickerHint(true, true)
	if !strings.Contains(filt, "apply") || !strings.Contains(filt, "cancel") {
		t.Errorf("filter hint missing apply/cancel legend: %q", filt)
	}
}

func TestReadEscapeFinal(t *testing.T) {
	// The leading Esc is assumed already consumed; the string is what follows.
	cases := map[string]byte{
		"[A":    'A', // up
		"[B":    'B', // down
		"[C":    'C', // right
		"[D":    'D', // left
		"[3~":   '~', // Delete: parameter then final '~' (not leaked as filter)
		"[1;5A": 'A', // Ctrl-Up: parameters then final 'A'
		"[6~":   '~', // PgDn
		"OA":    'A', // SS3 up
	}
	for seq, want := range cases {
		u, _, _ := newTestUI(t, 80, seq)
		if got := u.readEscapeFinal(); got != want {
			t.Errorf("readEscapeFinal(Esc %q) = %q, want %q", seq, got, want)
		}
	}
}

func TestReadFilterRune(t *testing.T) {
	// ASCII printable passes through.
	u, _, _ := newTestUI(t, 80, "")
	if r, ok := u.readFilterRune('a'); !ok || r != 'a' {
		t.Errorf("ascii: got (%q,%v), want ('a',true)", r, ok)
	}
	// A control byte is dropped.
	if _, ok := u.readFilterRune(0x01); ok {
		t.Errorf("control byte should be dropped")
	}
	// UTF-8 'é' = 0xC3 0xA9: lead byte passed in, continuation read from stdin.
	u2, _, _ := newTestUI(t, 80, "\xa9")
	if r, ok := u2.readFilterRune(0xC3); !ok || r != 'é' {
		t.Errorf("utf8 é: got (%q,%v), want ('é',true)", r, ok)
	}
	// UTF-8 'ş' = 0xC5 0x9F (a Turkish folder-name character).
	u3, _, _ := newTestUI(t, 80, "\x9f")
	if r, ok := u3.readFilterRune(0xC5); !ok || r != 'ş' {
		t.Errorf("utf8 ş: got (%q,%v), want ('ş',true)", r, ok)
	}
}

func TestPickerRowClipsToWidth(t *testing.T) {
	// Color off (ColorNever) so runeLen reflects visible width with no SGR codes.
	u, _, _ := newTestUI(t, 20, "")
	long := PickItem{Label: strings.Repeat("x", 100), IsFolder: true}

	if row := u.pickerRow(long, false, 20); runeLen(row) > 20 {
		t.Errorf("unselected row exceeds width 20 (got %d): %q", runeLen(row), row)
	}
	// A selected row is padded to exactly the width (the highlight bar).
	if row := u.pickerRow(long, true, 20); runeLen(row) != 20 {
		t.Errorf("selected row width = %d, want exactly 20: %q", runeLen(row), row)
	}
	// A row with a long Secondary must also stay within width.
	leaf := PickItem{Label: "db", Secondary: strings.Repeat("/seg", 40)}
	if row := u.pickerRow(leaf, false, 20); runeLen(row) > 20 {
		t.Errorf("row with long secondary exceeds width 20 (got %d): %q", runeLen(row), row)
	}
}

func TestPickerPromptKeepsTailWithinWidth(t *testing.T) {
	u, _, _ := newTestUI(t, 15, "")
	p := u.pickerPrompt(strings.Repeat("y", 50), 15)
	if runeLen(p) > 15 {
		t.Errorf("prompt exceeds width 15 (got %d): %q", runeLen(p), p)
	}
	// The most recently typed characters (the tail) must remain visible.
	if !strings.HasSuffix(p, "y") {
		t.Errorf("prompt should keep the filter tail visible: %q", p)
	}
}

func TestPickEllipsizesSecondaryToWidth(t *testing.T) {
	long := "/very/long/secondary/path/" + strings.Repeat("segment/", 20)
	items := []PickItem{{Label: "x", Secondary: long}}
	u, _, errBuf := newTestUI(t, 40, "q\n")

	if _, err := u.Pick("", items, PickOptions{}); err != nil {
		t.Fatalf("Pick: %v", err)
	}
	for _, ln := range strings.Split(strings.TrimRight(errBuf.String(), "\n"), "\n") {
		if ln == "filter> " {
			continue
		}
		if n := runeLen(ln); n > 40 {
			t.Errorf("row exceeds width 40 (got %d): %q", n, ln)
		}
	}
	if !strings.Contains(errBuf.String(), "…") {
		t.Errorf("expected an ellipsis from truncation, got:\n%s", errBuf.String())
	}
}
