package chat

import (
	"cmp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
	"github.com/datadog-labs/bits-cli/internal/tui/graphics"
)

// WidgetAction is what a click on a widget does. LinkAt reports it on every
// row of the widget, the way an accordion takes clicks on its whole block.
type WidgetAction uint8

const (
	WidgetNone  WidgetAction = iota
	WidgetOpen               // open the interactive panel
	WidgetRetry              // retry a failed preview
	WidgetWeb                // open the web sandbox
)

// widgetIcons mark the title as a way out of the terminal, as the former
// action links did.
var widgetIcons = map[WidgetAction]string{WidgetOpen: " ⧉", WidgetWeb: " ↗"}

// WidgetView is what the transcript draws for a widget block. The tui decides
// it from capabilities and operation state; the list only renders it.
type WidgetView struct {
	// Image reserves a full-width band Rows tall when Rows > 0, and paints the
	// picture, Columns wide and centered, once ID > 0.
	Image   graphics.Image
	Loading string // spinner label: a badge over the box, or a line without one
	Note    string // one muted line, such as a failure reason
	Action  WidgetAction
}

// animated reports whether the view depends on the frame counter.
func (v WidgetView) animated() bool { return v.Loading != "" }

// widgetTitle reads the title a widget_def carries inside its definition,
// then the legacy payload's, or "" when there is none.
func widgetTitle(it agent.Block) string {
	if it.Widget == nil {
		return ""
	}
	if def := it.Widget.WidgetDef; def != nil {
		if title, _ := (*def)["title"].(string); title != "" {
			return escape.SingleLine(title)
		}
	}
	return escape.SingleLine(it.Widget.Title)
}

func renderWidgetView(it agent.Block, v WidgetView, c renderContext) string {
	width := max(1, c.width)
	lines := []string{widgetHeader(it, v, width, c)}
	box, badged := widgetBox(v, width, c)
	lines = append(lines, box...)
	if v.Loading != "" && !badged {
		glyph, style := statusGlyph(agent.ToolRunning, c.sty, c.frame)
		lines = append(lines, ansi.Truncate(style.UnsetBackground().Render(glyph)+" "+c.sty.Meta.Render(v.Loading), width, "…"))
	}
	if v.Note != "" {
		lines = append(lines, ansi.Truncate(c.sty.Meta.Render(escape.SingleLine(v.Note)), width, "…"))
	}
	return strings.Join(lines, "\n")
}

// widgetTitlePad indents the title like the markdown around it.
const widgetTitlePad = "  "

// widgetHeader is the title row: padded, on the band's surface across the
// full width, so title and image read as one card.
func widgetHeader(it agent.Block, v WidgetView, width int, c renderContext) string {
	title := c.sty.ToolName.Background(c.sty.WidgetBox.GetBackground())
	if v.Action != WidgetNone {
		title = title.Foreground(c.sty.MarkdownLink)
	}
	icon := widgetIcons[v.Action]
	room := max(1, width-len(widgetTitlePad)-lipgloss.Width(icon))
	head := c.sty.WidgetBox.Render(widgetTitlePad) +
		ansi.Truncate(title.Render(cmp.Or(widgetTitle(it), "Widget")), room, "…") + title.Render(icon)
	if fill := width - lipgloss.Width(head); fill > 0 {
		head += c.sty.WidgetBox.Render(strings.Repeat(" ", fill))
	}
	return ansi.Truncate(head, width, "")
}

// widgetBox draws a band across the full width, the image centered in it: an
// empty surface until the pixels arrive, and the loading badge spliced over
// the band's center cells. It reports whether the badge fit.
func widgetBox(v WidgetView, width int, c renderContext) ([]string, bool) {
	img := v.Image
	img.Columns = min(img.Columns, width)
	if img.Rows <= 0 {
		return nil, false
	}
	surface := func(n int) string {
		if n <= 0 {
			return ""
		}
		return c.sty.WidgetBox.Render(strings.Repeat(" ", n))
	}
	left := (width - img.Columns) / 2
	// cells draws band columns [from, to): the image where it lies, surface
	// around it.
	cells := func(y, from, to int) string {
		l, r := max(from, left), min(to, left+img.Columns)
		if img.ID == 0 || l >= r {
			return surface(to - from)
		}
		return surface(l-from) + img.Row(y, l-left, r-left, c.sty.WidgetBox) + surface(to-r)
	}
	badge := widgetBadge(v.Loading, width, c)
	badgeWidth := 0
	if len(badge) > 0 {
		badgeWidth = lipgloss.Width(badge[0])
	}
	if len(badge) > img.Rows {
		badge = badge[1:2]
	}
	top, at := (img.Rows-len(badge))/2, (width-badgeWidth)/2
	rows := make([]string, img.Rows)
	for y := range rows {
		if b := y - top; b >= 0 && b < len(badge) {
			rows[y] = cells(y, 0, at) + badge[b] + cells(y, at+badgeWidth, width)
			continue
		}
		rows[y] = cells(y, 0, width)
	}
	return rows, len(badge) > 0
}

// widgetBadge is the running pill padded by a blank row above and below. A
// box too narrow for the label keeps the spinner alone.
func widgetBadge(label string, columns int, c renderContext) []string {
	if label == "" {
		return nil
	}
	glyph, style := statusGlyph(agent.ToolRunning, c.sty, c.frame)
	middle := style.Render("  " + glyph + "  " + label + "  ")
	if lipgloss.Width(middle) > columns {
		middle = style.Render(" " + glyph + " ")
	}
	if lipgloss.Width(middle) > columns {
		return nil
	}
	blank := style.Render(strings.Repeat(" ", lipgloss.Width(middle)))
	return []string{blank, middle, blank}
}
