package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Format is the output shape for read/list/get commands, selected by the global
// --output/-o flag.
type Format string

const (
	// FormatTable is the human-facing default: aligned, terminal-width-aware
	// columns.
	FormatTable Format = "table"
	// FormatJSON marshals the payload with json.MarshalIndent (map keys sorted).
	FormatJSON Format = "json"
	// FormatYAML marshals the payload with gopkg.in/yaml.v3.
	FormatYAML Format = "yaml"
)

// Formats lists the valid output formats (useful for flag completion).
func Formats() []string { return []string{string(FormatTable), string(FormatJSON), string(FormatYAML)} }

// ParseFormat validates and normalizes an --output value. An empty string
// defaults to FormatTable.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(FormatTable):
		return FormatTable, nil
	case string(FormatJSON):
		return FormatJSON, nil
	case string(FormatYAML):
		return FormatYAML, nil
	default:
		return "", Errorf("invalid output format %q (want table, json, or yaml)", s)
	}
}

// Column describes one table column. Header is the (short) column title.
// Truncatable marks a column that may be shrunk with an ellipsis when the table
// is too wide for the terminal — set it on low-value wide columns (URLs, UUIDs)
// so they give up space before meaningful columns. Among truncatable columns,
// the lowest Priority shrinks first.
type Column struct {
	Header      string
	Truncatable bool
	Priority    int
}

// Table is a set of string rows to render as aligned columns. Build it and hand
// it to Print with FormatTable (or render directly with Render).
type Table struct {
	Columns []Column
	Rows    [][]string
}

// NewTable creates a Table with the given column headers, none truncatable.
// Use SetTruncatable to mark columns that may shrink.
func NewTable(headers ...string) *Table {
	cols := make([]Column, len(headers))
	for i, h := range headers {
		cols[i] = Column{Header: h}
	}
	return &Table{Columns: cols}
}

// SetTruncatable marks the column at index i as shrinkable with the given
// priority (lower shrinks first). It is a no-op for an out-of-range index.
func (t *Table) SetTruncatable(i, priority int) *Table {
	if i >= 0 && i < len(t.Columns) {
		t.Columns[i].Truncatable = true
		t.Columns[i].Priority = priority
	}
	return t
}

// AddRow appends a row. Missing trailing cells render blank; extra cells are
// ignored.
func (t *Table) AddRow(cells ...string) *Table {
	t.Rows = append(t.Rows, cells)
	return t
}

// Print renders data to stdout in the requested format. For FormatTable it
// renders table (which may be nil only for the other formats); for JSON/YAML it
// marshals data. Status text and empty-state notices are the caller's job and
// belong on stderr.
func (u *UI) Print(format Format, data any, table *Table) error {
	switch format {
	case FormatJSON:
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return Wrapf(err, "encode json")
		}
		fmt.Fprintln(u.out, string(b))
		return nil
	case FormatYAML:
		b, err := yaml.Marshal(data)
		if err != nil {
			return Wrapf(err, "encode yaml")
		}
		u.out.Write(b)
		return nil
	case FormatTable, "":
		return u.Render(table)
	default:
		return Errorf("invalid output format %q", format)
	}
}

// Render writes table to stdout as aligned columns. The layout is built on
// text/tabwriter but clamped to the terminal width: on a TTY, cells are
// truncated with an ellipsis so every row stays on one line — no wrapping, no
// staircasing — with truncatable columns giving up width first. When stdout is
// not a TTY (piped or redirected), full untruncated columns are emitted so
// downstream tools see complete values. The header row is bolded when color is
// enabled; data is never colored.
func (u *UI) Render(table *Table) error {
	if table == nil || len(table.Columns) == 0 {
		return nil
	}
	n := len(table.Columns)

	headers := make([]string, n)
	widths := make([]int, n)
	for i, c := range table.Columns {
		headers[i] = c.Header
		widths[i] = runeLen(c.Header)
	}
	for _, row := range table.Rows {
		for i := 0; i < n && i < len(row); i++ {
			if l := runeLen(row[i]); l > widths[i] {
				widths[i] = l
			}
		}
	}

	// On a TTY, shrink to fit the terminal. Off a TTY, keep natural widths so
	// piped output is complete and script-friendly.
	truncate := u.isTTY
	if truncate && lineWidth(widths) > u.width {
		shrinkToFit(widths, headers, table.Columns, u.width)
	}

	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	writeRow(tw, headers, widths, truncate)
	for _, row := range table.Rows {
		cells := make([]string, n)
		for i := 0; i < n; i++ {
			if i < len(row) {
				cells[i] = row[i]
			}
		}
		writeRow(tw, cells, widths, truncate)
	}
	if err := tw.Flush(); err != nil {
		return Wrapf(err, "render table")
	}

	// Colorize the header line (the first line) as a whole so ANSI codes never
	// land inside a tabwriter cell and skew its width accounting.
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if u.color && len(lines) > 0 {
		lines[0] = u.paint(colorBold, lines[0])
	}
	for _, ln := range lines {
		fmt.Fprintln(u.out, ln)
	}
	return nil
}

// writeRow emits one tab-separated row, truncating each cell to its column
// width when truncate is set. tabwriter re-pads to align.
func writeRow(w *tabwriter.Writer, cells []string, widths []int, truncate bool) {
	out := make([]string, len(widths))
	for i := range widths {
		var c string
		if i < len(cells) {
			c = cells[i]
		}
		if truncate {
			c = truncateCell(c, widths[i])
		}
		out[i] = c
	}
	fmt.Fprintln(w, strings.Join(out, "\t"))
}

// shrinkToFit reduces column widths in place until the row fits avail columns.
// Truncatable columns yield first, lowest Priority (then widest) leading; if
// that isn't enough, any column is shaved down to a floor that never drops
// below its header, so headers are never truncated. A residual overflow (when
// even the headers don't fit) is accepted rather than wrapping.
func shrinkToFit(widths []int, headers []string, cols []Column, avail int) {
	n := len(widths)
	floor := make([]int, n)
	for i := range widths {
		f := runeLen(headers[i])
		if f < minCellWidth {
			f = minCellWidth
		}
		if f > widths[i] {
			f = widths[i] // can't floor above the natural width
		}
		floor[i] = f
	}

	overflow := lineWidth(widths) - avail

	// Pass 1: truncatable columns, lowest Priority first, widest breaking ties.
	order := make([]int, 0, n)
	for i, c := range cols {
		if c.Truncatable {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if cols[ia].Priority != cols[ib].Priority {
			return cols[ia].Priority < cols[ib].Priority
		}
		return widths[ia] > widths[ib]
	})
	overflow = shave(widths, floor, order, overflow)

	// Pass 2 (last resort): shave the widest shrinkable column of any kind so
	// the row still fits on one line.
	for overflow > 0 {
		idx := -1
		for i := 0; i < n; i++ {
			if widths[i] > floor[i] && (idx < 0 || widths[i] > widths[idx]) {
				idx = i
			}
		}
		if idx < 0 {
			break
		}
		widths[idx]--
		overflow--
	}
}

// shave reduces the given columns toward their floors, consuming overflow, and
// returns the remaining overflow.
func shave(widths, floor, order []int, overflow int) int {
	for _, i := range order {
		if overflow <= 0 {
			break
		}
		can := widths[i] - floor[i]
		if can <= 0 {
			continue
		}
		take := can
		if take > overflow {
			take = overflow
		}
		widths[i] -= take
		overflow -= take
	}
	return overflow
}

// minCellWidth is the smallest width a shrinkable column is allowed, leaving
// room for at least one character plus the ellipsis.
const minCellWidth = 3

// lineWidth is the rendered width of a row: the sum of column widths plus the
// 2-space padding tabwriter inserts between each pair of columns.
func lineWidth(widths []int) int {
	total := 0
	for _, w := range widths {
		total += w
	}
	if len(widths) > 1 {
		total += 2 * (len(widths) - 1)
	}
	return total
}

// truncateCell clips s to at most max runes, using a trailing ellipsis when it
// must cut. It counts runes, not bytes, so multibyte content and the ellipsis
// stay one column each.
func truncateCell(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if runeLen(s) <= max {
		return s
	}
	r := []rune(s)
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }
