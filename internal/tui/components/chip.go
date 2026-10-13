package components

import (
	"strconv"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/tui/escape"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

// chipMaxLabelWidth keeps one long title from crowding out the other chips.
const chipMaxLabelWidth = 32

// Chip is the display identity of one prompt attachment, shared by the
// composer (removable) and the transcript (read-only). Type and Label are
// untrusted: they come from the backend or from file names.
type Chip struct {
	Type      string
	Label     string
	Removable bool
}

// EntityChip shows an entity's label, or its ID when it has none. An image's
// ID is its data URL, so an unlabeled image shows its type alone.
func EntityChip(entityType, id, label string, removable bool) Chip {
	if label == "" && entityType != "image_url" {
		label = id
	}
	return Chip{
		Type:      entityType,
		Label:     label,
		Removable: removable,
	}
}

// PlacedChip is a rendered chip and its cells in a block of chip rows. It is
// what is drawn and what clicks are mapped to. Index is the chip's index in
// the laid-out slice; the "+N more" token has Index len(chips).
type PlacedChip struct {
	Index int
	Row   int
	X     int
	Width int
	View  string
}

// LayoutChips wraps chips into rows of width cells, one cell apart. With
// maxRows > 0, chips that do not fit collapse into a trailing "+N more"
// token. The chip (or token) at index active is highlighted; -1 highlights
// none.
func LayoutChips(sty styles.Chip, chips []Chip, width, maxRows, active int) []PlacedChip {
	width = max(1, width)
	placed := make([]PlacedChip, 0, len(chips)+1)
	row, x := 0, 0
	for i, chip := range chips {
		view := renderChip(sty, chip, width, i == active)
		w := lipgloss.Width(view)
		if x > 0 && x+1+w > width {
			row, x = row+1, 0
		} else if x > 0 {
			x++
		}
		placed = append(placed, PlacedChip{
			Index: i,
			Row:   row,
			X:     x,
			Width: w,
			View:  view,
		})
		x += w
	}
	if maxRows <= 0 || row < maxRows {
		return placed
	}

	kept := placed[:0]
	for _, p := range placed {
		if p.Row < maxRows {
			kept = append(kept, p)
		}
	}
	for {
		more := chipToken(sty, "+"+strconv.Itoa(len(chips)-len(kept))+" more", width, len(chips) == active)
		x := 0
		if n := len(kept); n > 0 && kept[n-1].Row == maxRows-1 {
			x = kept[n-1].X + kept[n-1].Width + 1
		}
		moreWidth := lipgloss.Width(more)
		if x+moreWidth <= width || x == 0 {
			return append(kept, PlacedChip{
				Index: len(chips),
				Row:   maxRows - 1,
				X:     x,
				Width: moreWidth,
				View:  more,
			})
		}
		kept = kept[:len(kept)-1]
	}
}

// LayoutExpandedChips lays out every chip and, when they span several rows,
// appends a "less" token with Index len(chips) to collapse them again.
func LayoutExpandedChips(sty styles.Chip, chips []Chip, width, active int) []PlacedChip {
	width = max(1, width)
	placed := LayoutChips(sty, chips, width, 0, active)
	if len(placed) == 0 || placed[len(placed)-1].Row == 0 {
		return placed
	}
	last := placed[len(placed)-1]
	less := chipToken(sty, "less", width, len(chips) == active)
	w := lipgloss.Width(less)
	row, x := last.Row, last.X+last.Width+1
	if x+w > width {
		row, x = row+1, 0
	}
	return append(placed, PlacedChip{
		Index: len(chips),
		Row:   row,
		X:     x,
		Width: w,
		View:  less,
	})
}

// ChipRows joins placed chips into one line per row, painting the gaps with
// the surrounding surface. Lines are not padded past their last chip.
func ChipRows(sty styles.Chip, placed []PlacedChip) []string {
	if len(placed) == 0 {
		return nil
	}
	rows := make([]strings.Builder, placed[len(placed)-1].Row+1)
	ends := make([]int, len(rows))
	for _, p := range placed {
		if gap := p.X - ends[p.Row]; gap > 0 {
			rows[p.Row].WriteString(sty.Gap.Render(strings.Repeat(" ", gap)))
		}
		rows[p.Row].WriteString(p.View)
		ends[p.Row] = p.X + p.Width
	}
	lines := make([]string, len(rows))
	for i := range rows {
		lines[i] = rows[i].String()
	}
	return lines
}

// ChipAt returns the index of the placed chip covering cell (x, row).
func ChipAt(placed []PlacedChip, x, row int) (int, bool) {
	for _, p := range placed {
		if p.Row == row && x >= p.X && x < p.X+p.Width {
			return p.Index, true
		}
	}
	return 0, false
}

// EntityTypeName turns an entity type such as "custom_widget" into the name
// shown to users ("Custom Widget").
func EntityTypeName(entityType string) string {
	if entityType == "image_url" {
		return "Image"
	}
	words := strings.Fields(strings.ReplaceAll(entityType, "_", " "))
	for i, word := range words {
		runes := []rune(word)
		if len(runes) > 0 {
			words[i] = string(unicode.ToUpper(runes[0])) + string(runes[1:])
		}
	}
	if len(words) == 0 {
		return "Entity"
	}
	return strings.Join(words, " ")
}

// renderChip draws "Type: label ×", never wider than maxWidth.
func renderChip(sty styles.Chip, chip Chip, maxWidth int, active bool) string {
	typeName := EntityTypeName(escape.SingleLine(chip.Type))
	label := escape.SingleLine(chip.Label)
	labelStyle, removeStyle := sty.Label, sty.Remove
	if active {
		labelStyle, removeStyle = sty.Active, sty.Active
	}
	remove := ""
	if chip.Removable {
		remove = " ×"
	}

	var b strings.Builder
	if label == "" {
		b.WriteString(sty.Type.Render(typeName))
	} else {
		b.WriteString(sty.Type.Render(typeName + ": "))
		used := sty.Frame.GetHorizontalFrameSize() + ansi.StringWidth(typeName) + 2 + ansi.StringWidth(remove)
		budget := max(1, min(chipMaxLabelWidth, maxWidth-used))
		b.WriteString(labelStyle.Render(ansi.Truncate(label, budget, "…")))
	}
	if remove != "" {
		b.WriteString(removeStyle.Render(remove))
	}
	return fitChip(sty.Frame.Render(b.String()), maxWidth)
}

func chipToken(sty styles.Chip, text string, maxWidth int, active bool) string {
	style := sty.Type
	if active {
		style = sty.Active
	}
	return fitChip(sty.Frame.Render(style.Render(text)), maxWidth)
}

func fitChip(view string, maxWidth int) string {
	if lipgloss.Width(view) <= maxWidth {
		return view
	}
	return ansi.Truncate(view, maxWidth, "…")
}
