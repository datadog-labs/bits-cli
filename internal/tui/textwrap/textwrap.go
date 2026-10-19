// Package textwrap lays styled text out in rows of a fixed cell width.
//
// Text is wrapped as styled segments rather than as rendered ANSI strings, so
// styles never have to be recovered from escape sequences mid-row. For every
// row [Wrap] returns:
//
//   - it is at most [Options.Width] cells wide, prefix included. A prefix
//     leaving no room for text is cut to leave one cell. The only exception
//     is a single grapheme wider than the room left after the prefix;
//   - no printable text is lost: only whitespace at a soft row break or at
//     the end of a line is dropped, unless [Options.KeepTrailingSpace] keeps
//     the latter or [Options.Ellipsis] asks to cut words wider than a row;
//   - each segment renders with its own style on every row it reaches, so no
//     row depends on terminal state left by the previous one.
//
// Width is measured in terminal cells per grapheme cluster. Tabs measure zero
// cells: expand them before wrapping.
package textwrap

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Style renders text in a style. lipgloss.Style satisfies it.
type Style interface {
	Render(strs ...string) string
}

// Segment is a run of text sharing one style. A nil Style renders the text
// unstyled. A "\n" in Text ends the source line.
type Segment struct {
	Text  string
	Style Style
	// AvoidBreak keeps Text on one row if it fits on one.
	AvoidBreak bool
}

// Plain returns an unstyled segment.
func Plain(text string) Segment { return Segment{Text: text} }

// Styled returns a segment rendered with style.
func Styled(text string, style Style) Segment { return Segment{Text: text, Style: style} }

// Options controls a layout.
type Options struct {
	// Width is the cell width of every row, prefix included. Values below 1
	// are treated as 1.
	Width int
	// FirstPrefix starts the first row and RestPrefix every other row. Both
	// are rendered as given; their cell widths are measured.
	FirstPrefix, RestPrefix string
	// Breakpoints lists characters after which a row may break, in addition
	// to whitespace, e.g. [Prose] or [Shell].
	Breakpoints string
	// KeepIndent repeats each source line's leading spaces on every row of
	// that line, capped at half the room, instead of treating them as a
	// word break. The indent keeps the style of the spaces it stands for.
	KeepIndent bool
	// KeepTrailingSpace keeps whitespace ending a source line, wrapping it
	// like text, where it is content, e.g. in a diff.
	KeepTrailingSpace bool
	// Ellipsis, if set, cuts a word wider than a whole row to one row ending
	// in Ellipsis instead of breaking it across rows. Use it where a compact
	// summary matters more than the full text.
	Ellipsis string
}

// Breakpoint sets for [Options.Breakpoints].
const (
	Prose  = "-"
	Output = "-/"  // command output: hyphens and paths
	Shell  = "/|;" // "&" is left out: splitting "&&" helps no one
	Code   = ",;/"
)

// Wrap lays segs out in rows according to o and renders them.
func Wrap(o Options, segs ...Segment) []string {
	o.Width = max(1, o.Width)
	o.FirstPrefix = fitPrefix(o.FirstPrefix, o.Width)
	o.RestPrefix = fitPrefix(o.RestPrefix, o.Width)
	l := layout{
		firstRoom: max(1, o.Width-ansi.StringWidth(o.FirstPrefix)),
		restRoom:  max(1, o.Width-ansi.StringWidth(o.RestPrefix)),
		ellipsis:  o.Ellipsis,
	}
	for _, line := range splitLines(toCells(segs, o.Breakpoints)) {
		l.line(line, o.KeepIndent, o.KeepTrailingSpace)
	}
	out := make([]string, len(l.rows))
	for i, r := range l.rows {
		prefix := o.RestPrefix
		if i == 0 {
			prefix = o.FirstPrefix
		}
		indent := strings.Repeat(" ", r.indent)
		if r.indent > 0 && segs[r.indentSeg].Style != nil {
			indent = segs[r.indentSeg].Style.Render(indent)
		}
		out[i] = prefix + indent + renderCells(r.cells, segs)
	}
	return out
}

// WrapProse word-wraps text to width rows in style, which may be nil, with
// [Prose] breakpoints.
func WrapProse(width int, text string, style Style) []string {
	return Wrap(Options{Width: width, Breakpoints: Prose}, Segment{Text: text, Style: style})
}

// fitPrefix cuts a prefix that leaves no room for text in a row of width.
func fitPrefix(prefix string, width int) string {
	if ansi.StringWidth(prefix) < width {
		return prefix
	}
	return ansi.Truncate(prefix, width-1, "")
}

// Render renders segs on one row, each with its style, without wrapping.
func Render(segs ...Segment) string {
	var b strings.Builder
	for _, seg := range segs {
		if seg.Style != nil {
			b.WriteString(seg.Style.Render(seg.Text))
		} else {
			b.WriteString(seg.Text)
		}
	}
	return b.String()
}

type cellKind uint8

const (
	cellText  cellKind = iota
	cellSpace          // breakable whitespace, dropped at a soft break
	cellBreak          // text after which a row may break
	cellNewline
)

// cell is one grapheme cluster and the segment it came from.
type cell struct {
	text  string
	width int
	seg   int
	kind  cellKind
	avoid bool // Segment.AvoidBreak
}

func toCells(segs []Segment, breakpoints string) []cell {
	var cells []cell
	for i, seg := range segs {
		for text := seg.Text; text != ""; {
			cluster, width := ansi.FirstGraphemeCluster(text, ansi.GraphemeWidth)
			text = text[len(cluster):]
			cells = append(cells, cell{text: cluster, width: width, seg: i, kind: classify(cluster, breakpoints), avoid: seg.AvoidBreak})
		}
	}
	return cells
}

func classify(cluster, breakpoints string) cellKind {
	r, size := utf8.DecodeRuneInString(cluster)
	switch {
	case cluster == "\n" || cluster == "\r\n":
		return cellNewline
	case size == len(cluster) && unicode.IsSpace(r) && r != '\u00a0':
		// A space carrying combining marks is text, not a break.
		return cellSpace
	case breakpoints != "" && strings.Contains(breakpoints, cluster):
		return cellBreak
	default:
		return cellText
	}
}

func splitLines(cells []cell) [][]cell {
	lines := make([][]cell, 0, 1)
	start := 0
	for i, c := range cells {
		if c.kind == cellNewline {
			lines = append(lines, cells[start:i])
			start = i + 1
			continue
		}
	}
	return append(lines, cells[start:])
}

type row struct {
	indent    int
	indentSeg int // segment whose style the indent takes
	cells     []cell
	width     int
}

type layout struct {
	firstRoom, restRoom int
	ellipsis            string
	indentSeg           int // of the current line
	rows                []row
}

// room is the text width available on the current row.
func (l *layout) room() int {
	return max(1, l.prefixRoom()-l.rows[len(l.rows)-1].indent)
}

// prefixRoom is the width after the current row's prefix, indent included.
func (l *layout) prefixRoom() int {
	if len(l.rows) == 1 {
		return l.firstRoom
	}
	return l.restRoom
}

func (l *layout) newRow(indent int) {
	l.rows = append(l.rows, row{indent: indent, indentSeg: l.indentSeg})
}

func (l *layout) add(cs []cell) {
	r := &l.rows[len(l.rows)-1]
	for _, c := range cs {
		r.cells = append(r.cells, c)
		r.width += c.width
	}
}

func (l *layout) line(cells []cell, keepIndent, keepTrailing bool) {
	indent := 0
	if keepIndent {
		indent = min(leadingSpaces(cells), min(l.firstRoom, l.restRoom)/2)
		if indent > 0 {
			l.indentSeg = cells[0].seg
		}
		cells = cells[indent:]
	}
	l.newRow(indent)

	// Leading whitespace is content until the line's first word lands; after
	// that, whitespace before a word is dropped if the word wraps.
	atLineStart := true
	var pending []cell
	pendingWidth := 0

	for len(cells) > 0 {
		// Whitespace waits until we know where the next word goes.
		if cells[0].kind == cellSpace {
			pending = append(pending, cells[0])
			pendingWidth += cells[0].width
			cells = cells[1:]
			continue
		}

		word, wordWidth := l.nextWord(cells, indent)
		cells = cells[len(word):]

		if l.fits(pendingWidth + wordWidth) {
			l.add(pending)
			l.add(word)
		} else {
			if atLineStart {
				l.hardAdd(pending, indent) // leading whitespace beyond the indent cap
			}
			if l.rows[len(l.rows)-1].width > 0 {
				l.newRow(indent)
			}

			if l.ellipsis != "" && wordWidth > l.room() {
				l.add(l.cut(word))
			} else {
				l.hardAdd(word, indent)
			}
		}

		pending, pendingWidth, atLineStart = nil, 0, false
	}

	if atLineStart || keepTrailing {
		// A line of only whitespace keeps it, as far as it fits.
		l.hardAdd(pending, indent)
	}
}

// leadingSpaces counts the plain spaces starting cells.
func leadingSpaces(cells []cell) int {
	n := 0
	for n < len(cells) && cells[n].kind == cellSpace && cells[n].text == " " {
		n++
	}
	return n
}

// fits reports whether width more cells fit on the current row.
func (l *layout) fits(width int) bool {
	return l.rows[len(l.rows)-1].width+width <= l.room()
}

// nextWord returns the word starting cells and its width. AvoidBreak text
// is a word of its own, unless it is too long for any row: then it breaks
// like plain text.
func (l *layout) nextWord(cells []cell, indent int) ([]cell, int) {
	n, width := wordEnd(cells, true)
	if width <= max(1, l.restRoom-indent) {
		return cells[:n], width
	}

	for i := range cells[:n] {
		cells[i].avoid = false
	}
	// Rescan the overlong run alone, so the text after it stays protected.
	n, width = wordEnd(cells[:n], false)
	return cells[:n], width
}

// wordEnd returns the length and width of the word starting cells: text up
// to whitespace, or through the first breakpoint. With avoid, AvoidBreak
// runs are words of their own, whitespace and breakpoints included.
func wordEnd(cells []cell, avoid bool) (n, width int) {
	for ; n < len(cells); n++ {
		c := cells[n]
		held := avoid && c.avoid

		if avoid && n > 0 && c.avoid != cells[n-1].avoid {
			break // an AvoidBreak run starts or ends
		}
		if c.kind == cellSpace && !held {
			break
		}

		width += c.width
		if c.kind == cellBreak && !held {
			return n + 1, width
		}
	}
	return n, width
}

// cut shortens word to fit the current row, ending in the ellipsis styled
// like the last grapheme kept.
func (l *layout) cut(word []cell) []cell {
	room := l.room() - ansi.StringWidth(l.ellipsis)
	n, width := 0, 0
	for n < len(word)-1 && width+word[n].width <= room {
		width += word[n].width
		n++
	}
	last := word[max(0, n-1)]
	return append(word[:n:n], cell{text: l.ellipsis, width: ansi.StringWidth(l.ellipsis), seg: last.seg, kind: cellText})
}

// hardAdd places cells grapheme by grapheme, opening rows as needed.
func (l *layout) hardAdd(cells []cell, indent int) {
	for _, c := range cells {
		if r := l.rows[len(l.rows)-1]; r.width > 0 && r.width+c.width > l.room() {
			l.newRow(indent)
		}
		if r := &l.rows[len(l.rows)-1]; r.width == 0 && c.width > l.room() {
			// A grapheme wider than the room left by the indent gives up
			// indentation rather than overflowing the row.
			r.indent = max(0, min(r.indent, l.prefixRoom()-c.width))
		}
		l.add([]cell{c})
	}
}

// renderCells renders runs of cells from the same segment with its style.
func renderCells(cells []cell, segs []Segment) string {
	var b, run strings.Builder
	flush := func(seg int) {
		if run.Len() == 0 {
			return
		}
		if style := segs[seg].Style; style != nil {
			b.WriteString(style.Render(run.String()))
		} else {
			b.WriteString(run.String())
		}
		run.Reset()
	}
	for i, c := range cells {
		if i > 0 && cells[i-1].seg != c.seg {
			flush(cells[i-1].seg)
		}
		run.WriteString(c.text)
	}
	if len(cells) > 0 {
		flush(cells[len(cells)-1].seg)
	}
	return b.String()
}
