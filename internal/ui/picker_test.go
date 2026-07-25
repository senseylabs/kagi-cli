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
