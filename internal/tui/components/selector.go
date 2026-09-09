package components

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// Choice is one selector row. Label is primary; Detail is subordinate context.
type Choice struct {
	Label  string
	Detail string
}

// Selector owns static-list navigation and bounded two-column rendering.
type Selector struct {
	choices       []Choice
	selected      int
	styles        styles.Selector
	compactDetail bool
	fillWidth     bool
}

// NewSelector creates a selector and copies choices so callers cannot mutate
// its rows behind the component.
func NewSelector(choices []Choice, sty styles.Selector) *Selector {
	return &Selector{choices: append([]Choice(nil), choices...), styles: sty}
}

// SetStyles replaces the selector's theme-derived styles.
func (s *Selector) SetStyles(sty styles.Selector) { s.styles = sty }

// SetCompactDetail renders detail immediately after each label instead of
// aligning it to the longest label. This is useful for autocomplete menus,
// where column padding looks like an empty selectable area.
func (s *Selector) SetCompactDetail(compact bool) { s.compactDetail = compact }

// SetFillWidth pads each row to the requested view width using its active item
// style. Popup callers use this to prevent the underlying view from showing
// through between or after styled spans.
func (s *Selector) SetFillWidth(fill bool) { s.fillWidth = fill }

// SetIndex selects index, clamped to the available rows.
func (s *Selector) SetIndex(index int) {
	if len(s.choices) == 0 {
		s.selected = 0
		return
	}
	s.selected = max(0, min(index, len(s.choices)-1))
}

// Index returns the selected row index.
func (s *Selector) Index() int { return s.selected }

// Selected returns the selected choice, when one exists.
func (s *Selector) Selected() (Choice, bool) {
	if len(s.choices) == 0 || s.selected < 0 || s.selected >= len(s.choices) {
		return Choice{}, false
	}
	return s.choices[s.selected], true
}

// UpdateKey applies selector navigation and reports whether it consumed key.
func (s *Selector) UpdateKey(key string) bool {
	if len(s.choices) == 0 {
		return false
	}
	switch key {
	case "up", "k", "ctrl+p":
		s.selected = (s.selected - 1 + len(s.choices)) % len(s.choices)
		return true
	case "down", "j", "ctrl+n":
		s.selected = (s.selected + 1) % len(s.choices)
		return true
	default:
		return false
	}
}

// View renders all choices within width. Detail truncates before labels and is
// omitted when the terminal cannot fit both columns.
func (s *Selector) View(width int) string {
	if width < 1 || len(s.choices) == 0 {
		return ""
	}
	labelWidth := 0
	for _, choice := range s.choices {
		labelWidth = max(labelWidth, ansi.StringWidth(choice.Label))
	}
	markerWidth := max(ansi.StringWidth(s.styles.Marker), ansi.StringWidth(s.styles.SelectedMarker))
	gap := max(0, s.styles.ColumnGap)
	hasDetail := false
	maxDetailWidth := 0
	for _, choice := range s.choices {
		if choice.Detail != "" {
			hasDetail = true
			maxDetailWidth = max(maxDetailWidth, ansi.StringWidth(choice.Detail))
		}
	}
	if hasDetail && width-markerWidth-gap >= 8 {
		// Reserve cells for type and metadata so one long label cannot erase
		// the detail column for every row.
		reservedDetail := min(maxDetailWidth, min(16, max(4, width/3)))
		labelWidth = min(labelWidth, max(1, width-markerWidth-gap-reservedDetail))
	}

	rows := make([]string, len(s.choices))
	for i, choice := range s.choices {
		selected := i == s.selected
		marker := s.styles.Marker
		labelStyle, detailStyle := s.styles.Item, s.styles.Detail
		if selected {
			marker = s.styles.SelectedMarker
			labelStyle, detailStyle = s.styles.Selected, s.styles.SelectedDetail
		}
		marker = padRight(marker, markerWidth)

		availableAfterMarker := max(0, width-markerWidth)
		rowLabelWidth := min(labelWidth, availableAfterMarker)
		label := ansi.Truncate(choice.Label, rowLabelWidth, "…")
		if !s.compactDetail {
			label = padRight(label, rowLabelWidth)
		} else {
			rowLabelWidth = ansi.StringWidth(label)
		}
		prefix := labelStyle.Render(marker + label)

		detailWidth := width - markerWidth - rowLabelWidth - gap
		if choice.Detail != "" && detailWidth > 1 {
			detail := ansi.Truncate(choice.Detail, detailWidth, "…")
			prefix += labelStyle.Render(strings.Repeat(" ", gap)) + detailStyle.Render(detail)
		}
		if s.fillWidth {
			prefix += labelStyle.Render(strings.Repeat(" ", max(0, width-ansi.StringWidth(prefix))))
		}
		rows[i] = ansi.Truncate(prefix, width, "…")
	}
	return strings.Join(rows, "\n")
}

func padRight(value string, width int) string {
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}
