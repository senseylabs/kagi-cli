package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/term"
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
	// PickGoUp means the user asked to go up a level; only possible when
	// PickOptions.AllowUp is set.
	PickGoUp
	// PickQuit means the user quit (q/Esc/Ctrl-C or EOF).
	PickQuit
)

// PickResult is the outcome of a Pick prompt. Item is meaningful only when Kind
// is PickSelected.
type PickResult struct {
	Kind PickKind
	Item PickItem
}

// PickOptions tunes a Pick prompt. AllowUp enables the go-up command, surfaced
// as PickGoUp; leave it false at the root of a browse.
type PickOptions struct {
	AllowUp bool
}

// errRawUnsupported signals that raw-terminal setup failed, so Pick should fall
// back to the line-based prompt. It never escapes the package.
var errRawUnsupported = errors.New("raw terminal mode unavailable")

// Pick presents items and returns the user's choice, folders first.
//
// On an interactive terminal it runs a full-screen-region picker: arrow keys
// (or Ctrl-P/Ctrl-N) move a highlighted selection, typing filters the list
// incrementally, Enter opens the selection, and the whole picker is redrawn in
// place — no scrolling trail. Off a terminal (a pipe, a redirect, a test
// buffer) it degrades to a plain numbered line prompt so scripts and dumb
// terminals still work and stdout stays clean.
func (u *UI) Pick(title string, items []PickItem, opts PickOptions) (PickResult, error) {
	ordered := foldersFirst(items)
	if u.canInteract() {
		res, err := u.pickInteractive(title, ordered, opts)
		if !errors.Is(err, errRawUnsupported) {
			return res, err
		}
		// Raw mode couldn't be established; fall through to the line prompt.
	}
	return u.pickLine(title, ordered, opts)
}

// canInteract reports whether both the input and messaging streams are real
// terminals, the precondition for the raw-mode picker.
func (u *UI) canInteract() bool {
	return u.inFile != nil && u.errFile != nil &&
		term.IsTerminal(int(u.inFile.Fd())) && term.IsTerminal(int(u.errFile.Fd()))
}

// Interactive reports whether Pick will run its interactive terminal UI (both
// stdin and stderr are TTYs). When false, Pick uses the line-based fallback, and
// a PickQuit there typically means end-of-input rather than a deliberate quit —
// callers that require a selection (e.g. setup) use this to fail loudly instead
// of treating an exhausted pipe as a clean abort.
func (u *UI) Interactive() bool { return u.canInteract() }

// pickerSize returns the messaging terminal's width and height, falling back to
// a sane 80x24 when the size can't be read.
func (u *UI) pickerSize() (w, h int) {
	if u.errFile != nil {
		if cw, ch, err := term.GetSize(int(u.errFile.Fd())); err == nil && cw > 0 && ch > 0 {
			return cw, ch
		}
	}
	if u.width > 0 {
		return u.width, 24
	}
	return 80, 24
}

// pickInteractive runs the raw-mode picker. It puts stdin in raw mode (so single
// keypresses arrive without Enter and Ctrl-C is delivered as a byte we handle),
// draws to stderr, and always both restores the terminal and erases its own
// output region before returning, so the caller's next output starts on a clean
// line.
func (u *UI) pickInteractive(title string, ordered []PickItem, opts PickOptions) (PickResult, error) {
	fd := int(u.inFile.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return PickResult{}, errRawUnsupported
	}
	// Hide the cursor while we own the screen region; restore terminal + cursor
	// on the way out no matter how we leave (return or panic).
	fmt.Fprint(u.err, "\x1b[?25l")
	defer func() {
		fmt.Fprint(u.err, "\x1b[?25h")
		_ = term.Restore(fd, oldState)
	}()

	// Restore the terminal on an external termination too. Raw mode disables ISIG,
	// so Ctrl-C arrives as a byte we handle; but a SIGTERM (e.g. `kill <pid>`)
	// would otherwise leave the terminal raw with a hidden cursor. The goroutine
	// exits via stopSig on the normal path, so it never leaks.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	stopSig := make(chan struct{})
	defer func() { signal.Stop(sigCh); close(stopSig) }()
	go func() {
		select {
		case <-sigCh:
			fmt.Fprint(u.err, "\x1b[?25h")
			_ = term.Restore(fd, oldState)
			os.Exit(1)
		case <-stopSig:
		}
	}()

	filter := ""
	filterBefore := "" // filter snapshot taken when entering filter mode, restored on Esc
	cursor := 0
	filtering := false // false = nav mode (vim keys); true = filter mode (typing)
	prevAbove := -1    // -1 => nothing drawn yet

	erase := func() {
		if prevAbove >= 0 {
			fmt.Fprintf(u.err, "\x1b[%dA\r\x1b[J", prevAbove)
			prevAbove = -1
		}
	}
	// done erases the picker region and returns the result; every exit path uses
	// it so no picker chrome is left behind.
	done := func(kind PickKind, item PickItem) (PickResult, error) {
		erase()
		return PickResult{Kind: kind, Item: item}, nil
	}

	for {
		filtered := filterPickItems(ordered, filter)
		cursor = clamp(cursor, 0, len(filtered)-1)
		prevAbove = u.drawPicker(title, filtered, cursor, filter, filtering, opts.AllowUp, prevAbove)

		// Half-page step, recomputed each frame so it tracks terminal resizes and
		// scales to the visible item window rather than the whole screen.
		_, height := u.pickerSize()
		halfPage := (height - 4) / 2
		if halfPage < 1 {
			halfPage = 1
		}

		b, err := u.in.ReadByte()
		if err != nil { // EOF (closed stdin)
			return done(PickQuit, PickItem{})
		}

		open := func() (PickResult, error, bool) {
			if len(filtered) > 0 {
				r, e := done(PickSelected, filtered[cursor])
				return r, e, true
			}
			return PickResult{}, nil, false
		}

		if filtering {
			// FILTER MODE: keystrokes build the filter; Enter applies it and Esc
			// restores the filter as it was on entering the mode (so refining an
			// applied filter and then canceling keeps the applied one). Arrows move.
			switch b {
			case ctrlC:
				return done(PickQuit, PickItem{})
			case keyCR, keyLF:
				filtering = false // apply the filter, back to nav
			case ctrlU:
				filter = "" // clear the typed filter, stay in filter mode
				cursor = 0
			case keyBackspace, keyDEL:
				if filter != "" {
					filter = trimLastRune(filter)
					cursor = 0
				} else {
					filtering = false // backspace on an empty filter exits filter mode
				}
			case ctrlN:
				cursor++
			case ctrlP:
				cursor--
			case keyEsc:
				if u.in.Buffered() == 0 {
					// Esc cancels the edit: restore the filter to what it was on
					// entering the mode and return to nav.
					filter = filterBefore
					filtering = false
					cursor = 0
				} else {
					switch u.readEscapeFinal() {
					case 'A':
						cursor--
					case 'B':
						cursor++
					}
				}
			default:
				if r, ok := u.readFilterRune(b); ok {
					filter += string(r)
					cursor = 0
				}
			}
			continue
		}

		// NAV MODE: vim-style movement; `/` enters filter mode.
		switch b {
		case ctrlC, 'q':
			return done(PickQuit, PickItem{})
		case '/':
			filterBefore = filter // so Esc can restore it if the edit is canceled
			filtering = true
			cursor = 0
		case 'j', ctrlN:
			cursor++
		case 'k', ctrlP:
			cursor--
		case 'g':
			cursor = 0
		case 'G':
			cursor = len(filtered) - 1
		case ctrlD:
			cursor += halfPage
		case ctrlU:
			cursor -= halfPage
		case 'h':
			if opts.AllowUp {
				return done(PickGoUp, PickItem{})
			}
		case 'l', keyCR, keyLF:
			if r, e, ok := open(); ok {
				return r, e
			}
		case keyBackspace, keyDEL:
			if filter != "" { // clear an applied filter
				filter = ""
				cursor = 0
			}
		case keyEsc:
			// A lone Esc quits; an Esc that begins a CSI/SS3 sequence (arrow keys)
			// arrives with its remaining bytes already buffered, so a zero buffer
			// means the key really was Esc.
			if u.in.Buffered() == 0 {
				return done(PickQuit, PickItem{})
			}
			switch u.readEscapeFinal() {
			case 'A': // up
				cursor--
			case 'B': // down
				cursor++
			case 'C': // right => open
				if r, e, ok := open(); ok {
					return r, e
				}
			case 'D': // left => up a level
				if opts.AllowUp {
					return done(PickGoUp, PickItem{})
				}
			}
		}
	}
}

// Control bytes read from the raw terminal.
const (
	ctrlC        = 0x03
	ctrlD        = 0x04 // half-page down (nav mode)
	ctrlN        = 0x0e // down
	ctrlP        = 0x10 // up
	ctrlU        = 0x15 // half-page up (nav mode)
	keyBackspace = 0x08
	keyLF        = 0x0a
	keyCR        = 0x0d
	keyEsc       = 0x1b
	keyDEL       = 0x7f
)

// readEscapeFinal consumes the rest of an escape sequence whose leading Esc has
// already been read and returns its final byte for dispatch (0 for an
// unrecognized sequence). It handles CSI ("Esc [" with optional numeric
// parameters/intermediates) and SS3 ("Esc O"), reading until a final byte in the
// 0x40–0x7E range — so parameterized keys (Delete "Esc [ 3 ~", PgUp/Dn, modified
// arrows "Esc [ 1 ; 5 A") don't spill their tail bytes into the filter. The whole
// sequence is already buffered, so these reads never block.
func (u *UI) readEscapeFinal() byte {
	b2, err := u.in.ReadByte()
	if err != nil {
		return 0
	}
	switch b2 {
	case '[':
		for {
			c, err := u.in.ReadByte()
			if err != nil {
				return 0
			}
			if c >= 0x40 && c <= 0x7E { // final byte
				return c
			}
		}
	case 'O': // SS3: a single final byte follows
		c, err := u.in.ReadByte()
		if err != nil {
			return 0
		}
		return c
	default:
		return 0
	}
}

// readFilterRune turns a typed byte into a rune to append to the filter. ASCII
// printables pass through; a UTF-8 lead byte (>= 0x80) is completed by reading
// its continuation bytes (buffered — the terminal delivers the character
// atomically) and decoding the whole rune, so typing "é" or "ş" filters
// correctly instead of inserting mojibake. Control bytes and invalid sequences
// are dropped.
func (u *UI) readFilterRune(first byte) (rune, bool) {
	if first < 0x20 || first == keyDEL {
		return 0, false
	}
	if first < 0x80 {
		return rune(first), true
	}
	var n int
	switch {
	case first&0xE0 == 0xC0:
		n = 2
	case first&0xF0 == 0xE0:
		n = 3
	case first&0xF8 == 0xF0:
		n = 4
	default:
		return 0, false // invalid lead byte
	}
	buf := make([]byte, 1, 4)
	buf[0] = first
	for i := 1; i < n; i++ {
		c, err := u.in.ReadByte()
		if err != nil {
			return 0, false
		}
		buf = append(buf, c)
	}
	r, size := utf8.DecodeRune(buf)
	if r == utf8.RuneError || size != n {
		return 0, false
	}
	return r, true
}

// drawPicker renders one frame of the interactive picker to stderr and returns
// the number of lines printed above the final (cursor) line, which the next
// frame uses to move back up and redraw in place. prevAbove is that count from
// the previous frame (-1 on the first draw).
func (u *UI) drawPicker(title string, filtered []PickItem, cursor int, filter string, filtering, allowUp bool, prevAbove int) int {
	width, height := u.pickerSize()

	// Reserve rows for the title, the hint line, the status/prompt line, and
	// possible scroll indicators; the rest show items.
	reserved := 3 // hint + status/prompt + one scroll indicator margin
	if title != "" {
		reserved++
	}
	maxItems := height - reserved - 1
	if maxItems < 1 {
		maxItems = 1
	}

	start, end := scrollWindow(cursor, len(filtered), maxItems)

	// Every row is clipped to the terminal width so nothing wraps onto a second
	// physical line — a wrapped line would desync the in-place redraw math
	// (prevAbove counts logical rows) and corrupt the screen. Single-style rows
	// (title, indicators, hint) clip their plain text before styling; item rows
	// clip internally; the filter prompt keeps its tail visible.
	rows := make([]string, 0, maxItems+4)
	if title != "" {
		rows = append(rows, u.paint(colorBold, truncateCell(title, width)))
	}
	if len(filtered) == 0 {
		rows = append(rows, u.styleDim(truncateCell("  (no matches)", width)))
	}
	if start > 0 {
		rows = append(rows, u.styleDim(truncateCell(fmt.Sprintf("  ↑ %d more", start), width)))
	}
	for i := start; i < end; i++ {
		rows = append(rows, u.pickerRow(filtered[i], i == cursor, width))
	}
	if end < len(filtered) {
		rows = append(rows, u.styleDim(truncateCell(fmt.Sprintf("  ↓ %d more", len(filtered)-end), width)))
	}
	rows = append(rows, u.styleDim(truncateCell("  "+pickerHint(filtering, allowUp), width)))
	if filtering {
		// The prompt is the last line so the terminal cursor lands after the typed
		// text; the cursor is shown (below) for a natural typing experience.
		rows = append(rows, u.pickerPrompt(filter, width))
	} else if filter != "" {
		// Nav mode with an applied filter: show it as a status line, no prompt.
		rows = append(rows, u.styleDim(truncateCell(fmt.Sprintf("  filter: %s  (⌫ clears)", filter), width)))
	} else {
		// Nav mode, no filter: keep the frame height stable with a blank line so
		// entering/leaving filter mode doesn't jump the layout.
		rows = append(rows, "")
	}

	var b strings.Builder
	if prevAbove >= 0 {
		fmt.Fprintf(&b, "\x1b[%dA", prevAbove) // move to the top of the prior frame
	}
	b.WriteString("\r\x1b[J") // column 0, clear everything below
	b.WriteString(strings.Join(rows, "\r\n"))
	// Show the cursor only while typing a filter; hide it during navigation so the
	// highlighted row is the sole focus indicator.
	if filtering {
		b.WriteString("\x1b[?25h")
	} else {
		b.WriteString("\x1b[?25l")
	}
	fmt.Fprint(u.err, b.String())

	return len(rows) - 1
}

// pickerRow renders a single item row, styled and clipped to width. A folder is
// shown bold-cyan with a trailing "/"; the selected row is drawn as a
// reverse-video bar spanning the full width so it reads as a highlight.
func (u *UI) pickerRow(it PickItem, selected bool, width int) string {
	marker := "  "
	if selected {
		marker = "❯ "
	}

	label := it.Label
	if it.IsFolder {
		label += "/"
	}

	if selected {
		// Build the plain text first, clip to width, pad, then invert the whole
		// line — nesting other SGR codes inside a reverse span renders messily.
		text := marker + label
		if it.Secondary != "" {
			text += "  " + it.Secondary
		}
		text = clipToWidth(text, width)
		if pad := width - runeLen(text); pad > 0 {
			text += strings.Repeat(" ", pad)
		}
		return u.styleSelected(text)
	}

	// Clip the label so marker+label never exceeds the width (which would wrap).
	labelBudget := width - runeLen(marker)
	if labelBudget < 1 {
		labelBudget = 1
	}
	if runeLen(label) > labelBudget {
		label = truncateCell(label, labelBudget)
	}

	styledLabel := label
	if it.IsFolder {
		styledLabel = u.styleFolder(label)
	}
	line := marker + styledLabel
	used := runeLen(marker) + runeLen(label)

	if it.Secondary != "" {
		const gap = "  "
		avail := width - used - runeLen(gap)
		if avail >= minCellWidth {
			line += gap + u.styleDim(truncateCell(it.Secondary, avail))
		}
	}
	return line
}

// pickerPrompt renders the filter input line (the last line of the frame, where
// the user's typing appears). When the filter grows past the width, the tail is
// kept visible (so the just-typed characters show) rather than letting the line
// wrap and desync the redraw.
func (u *UI) pickerPrompt(filter string, width int) string {
	const prompt = "filter> "
	budget := width - runeLen(prompt)
	if budget < 0 {
		budget = 0
	}
	if runeLen(filter) > budget {
		r := []rune(filter)
		filter = string(r[len(r)-budget:])
	}
	return u.styleDim(prompt) + filter
}

// pickerHint returns the one-line key legend for the current mode. Filter mode
// explains how to apply/cancel the filter; nav mode lists the vim movement keys
// (and the up-a-level key only when that's available).
func pickerHint(filtering, allowUp bool) string {
	if filtering {
		return "type to filter · ⏎ apply · esc cancel"
	}
	hint := "j/k move · / filter · ⏎ open"
	if allowUp {
		hint += " · h up"
	}
	return hint + " · q quit"
}

// scrollWindow returns the [start,end) slice of an n-item list to show so that
// the cursor stays visible within a window of at most size rows.
func scrollWindow(cursor, n, size int) (int, int) {
	if n <= size {
		return 0, n
	}
	start := cursor - size/2
	if start < 0 {
		start = 0
	}
	if start+size > n {
		start = n - size
	}
	return start, start + size
}

// clamp constrains v to [lo, hi]; when hi < lo (an empty list) it returns lo.
func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clipToWidth clips s to at most width runes (no ellipsis), used when padding a
// selected row to the terminal width.
func clipToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width])
}

// trimLastRune returns s without its final rune (correct for multibyte input).
func trimLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

// pickLine is the line-based fallback picker used when stdin/stderr aren't
// terminals (pipes, redirects, tests). It is a plain REPL on stderr and stdin,
// never touching raw mode, so it works over pipes and dumb terminals and keeps
// stdout clean for payload data.
//
// Each round it renders the title, a numbered list (folders first, Secondary
// ellipsized to the UI width), then a "filter> " prompt. Input rules:
//
//   - a number selects that item from the currently shown (filtered) list;
//   - "q" quits (PickQuit); EOF/Ctrl-D quits too;
//   - ".." goes up a level (PickGoUp) when AllowUp is set;
//   - an empty line clears an active filter (back to the full list);
//   - any other text becomes a case-insensitive substring filter.
//
// A genuine stdin read error (anything other than a clean EOF) is returned
// rather than swallowed.
func (u *UI) pickLine(title string, ordered []PickItem, opts PickOptions) (PickResult, error) {
	filter := ""

	for {
		filtered := filterPickItems(ordered, filter)
		u.renderPickList(title, filtered, opts.AllowUp, filter != "")

		line, err := u.in.ReadString('\n')
		if err != nil && err != io.EOF {
			return PickResult{}, Wrapf(err, "read selection")
		}
		input := strings.TrimSpace(line)

		if input == "" {
			// A clean EOF with no pending data is a quit (Ctrl-D on a TTY, end of
			// a pipe otherwise). An empty line clears an active filter (back to the
			// full list); with no filter set it simply re-renders.
			if err == io.EOF {
				return PickResult{Kind: PickQuit}, nil
			}
			filter = ""
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
func (u *UI) renderPickList(title string, filtered []PickItem, allowUp, filterActive bool) {
	if title != "" {
		fmt.Fprintln(u.err, title)
	}
	if len(filtered) == 0 {
		fmt.Fprintln(u.err, "  (no matches)")
	}
	for i, it := range filtered {
		u.renderPickRow(i+1, it)
	}
	hint := "[#] open · type to filter"
	if filterActive {
		hint += " · blank clears filter"
	}
	if allowUp {
		hint += " · .. up"
	}
	hint += " · q quit"
	fmt.Fprintln(u.err, "  "+hint)
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
