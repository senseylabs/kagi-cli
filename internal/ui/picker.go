package ui

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// PickItem is one selectable row in a Pick list. Label is the primary display
// text; Secondary is dimmer supporting text (a path, a URL, an expiry) that is
// ellipsized to keep the row on one line. IsFolder marks a folder so folders
// render first and can be styled with a trailing separator. Value carries an
// arbitrary caller payload (e.g. a BrowseNode) returned untouched on selection.
type PickItem struct {
	Label     string
	Secondary string
	IsFolder  bool
	Value     any
}

// PickKind distinguishes the three outcomes of a Pick prompt.
type PickKind int

const (
	// PickSelected means the user chose an item; PickResult.Item is that item.
	PickSelected PickKind = iota
	// PickGoUp means the user asked to go up a level (entered ".."); only
	// possible when PickOptions.AllowUp is set.
	PickGoUp
	// PickQuit means the user quit (entered "q" or sent EOF/Ctrl-D).
	PickQuit
)

// PickResult is the outcome of a Pick prompt. Item is meaningful only when Kind
// is PickSelected.
type PickResult struct {
	Kind PickKind
	Item PickItem
}

// PickOptions tunes a Pick prompt. AllowUp enables the ".." go-up command,
// surfaced as PickGoUp; leave it false at the root of a browse.
type PickOptions struct {
	AllowUp bool
}

// Pick runs a dependency-free, line-based filterable picker over items and
// returns the user's choice. It is a plain REPL on stderr and stdin — it never
// puts the terminal in raw mode — so it works over pipes and dumb terminals and
// keeps stdout clean for payload data.
//
// Each round it renders the title, a numbered list (folders first, Secondary
// ellipsized to the UI width so every row stays one line), then a "filter> "
// prompt. Input rules:
//
//   - a number selects that item from the currently shown (filtered) list;
//   - "q" quits (PickQuit); EOF/Ctrl-D quits too;
//   - ".." goes up a level (PickGoUp) when AllowUp is set;
//   - an empty line re-renders unchanged;
//   - any other text becomes a case-insensitive substring filter (matched
//     against Label and Secondary), narrowing and renumbering the list.
//
// A genuine stdin read error (anything other than a clean EOF) is returned
// rather than swallowed.
func (u *UI) Pick(title string, items []PickItem, opts PickOptions) (PickResult, error) {
	ordered := foldersFirst(items)
	filter := ""

	for {
		filtered := filterPickItems(ordered, filter)
		u.renderPickList(title, filtered)

		line, err := u.in.ReadString('\n')
		if err != nil && err != io.EOF {
			return PickResult{}, Wrapf(err, "read selection")
		}
		input := strings.TrimSpace(line)

		if input == "" {
			// A clean EOF with no pending data is a quit (Ctrl-D on a TTY, end of
			// a pipe otherwise). An empty line just re-renders.
			if err == io.EOF {
				return PickResult{Kind: PickQuit}, nil
			}
			continue
		}

		switch {
		case strings.EqualFold(input, "q"):
			return PickResult{Kind: PickQuit}, nil
		case input == ".." && opts.AllowUp:
			return PickResult{Kind: PickGoUp}, nil
		}

		// A pure integer is a selection against the currently shown list. An
		// out-of-range number falls through to a re-render rather than erroring.
		if n, convErr := strconv.Atoi(input); convErr == nil {
			if n >= 1 && n <= len(filtered) {
				return PickResult{Kind: PickSelected, Item: filtered[n-1]}, nil
			}
			continue
		}

		// Anything else narrows the list.
		filter = input
	}
}

// foldersFirst returns items reordered with folders before leaves, preserving
// the original relative order within each group (a stable partition).
func foldersFirst(items []PickItem) []PickItem {
	out := make([]PickItem, 0, len(items))
	for _, it := range items {
		if it.IsFolder {
			out = append(out, it)
		}
	}
	for _, it := range items {
		if !it.IsFolder {
			out = append(out, it)
		}
	}
	return out
}

// filterPickItems keeps the items whose Label or Secondary contains filter,
// case-insensitively. An empty filter returns items unchanged.
func filterPickItems(items []PickItem, filter string) []PickItem {
	if filter == "" {
		return items
	}
	needle := strings.ToLower(filter)
	out := make([]PickItem, 0, len(items))
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.Label), needle) ||
			strings.Contains(strings.ToLower(it.Secondary), needle) {
			out = append(out, it)
		}
	}
	return out
}

// renderPickList writes the title, the numbered (already filtered/ordered) rows,
// and the "filter> " prompt to stderr. The prompt has no trailing newline so the
// user's input sits on the same line.
func (u *UI) renderPickList(title string, filtered []PickItem) {
	if title != "" {
		fmt.Fprintln(u.err, title)
	}
	if len(filtered) == 0 {
		fmt.Fprintln(u.err, "  (no matches)")
	}
	for i, it := range filtered {
		u.renderPickRow(i+1, it)
	}
	fmt.Fprint(u.err, "filter> ")
}

// renderPickRow writes a single numbered row. Folders get a trailing "/" on the
// label. When a width is known the Secondary text is ellipsized (or dropped) so
// the whole row fits on one line; off a fixed width it is printed in full.
func (u *UI) renderPickRow(n int, it PickItem) {
	label := it.Label
	if it.IsFolder {
		label += "/"
	}
	line := fmt.Sprintf("%d. %s", n, label)

	if it.Secondary != "" {
		const gap = "  "
		if u.width > 0 {
			avail := u.width - runeLen(line) - runeLen(gap)
			if avail >= minCellWidth {
				line += gap + truncateCell(it.Secondary, avail)
			}
			// Too little room left: drop Secondary rather than wrap the row.
		} else {
			line += gap + it.Secondary
		}
	}

	fmt.Fprintln(u.err, line)
}
