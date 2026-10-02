// Package components contains reusable presentation and interaction primitives
// shared by top-level TUI screens.
package components

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// PanelContent supplies screen-specific copy and a size-aware body to Panel.
type PanelContent struct {
	Title   string
	Dismiss string
	// Body renders into the area BodySize reports.
	Body func(width, height int) string
	// MinBodyWidth is the narrowest body the full layout renders; below it the
	// panel falls back to its compact form.
	MinBodyWidth int
	// BodyHeader, ScrollableBody, and BodyFooter form an optional bounded
	// layout. ScrollableBody must return display-width-wrapped rows; Panel
	// performs vertical paging only, starting at ScrollOffset. Panel keeps no
	// scroll state: Layout reports the Window it showed and the caller keeps it.
	// ScrollHint follows the position line, naming the caller's scroll keys.
	BodyHeader     func(width int) string
	ScrollableBody func(width int) string
	BodyFooter     func(width int) string
	BodyFooterGap  int
	ScrollOffset   int
	ScrollHint     string
	FooterLeft     string
	FooterRight    string

	CompactTitle   string
	CompactMessage string
	CompactBody    func(width int) string
	TinyMessage    string
}

// Window is the slice of a scrollable body a layout showed: its first row, the
// body's row count and the rows per page. The zero Window means nothing scrolls.
// Callers keep it between layouts so wheel and paging clamp to what is visible.
type Window struct {
	Offset, Rows, Page int
}

// Scrolled returns the window moved by rows, clamped to the body.
func (w Window) Scrolled(rows int) Window {
	w.Offset = min(max(0, w.Offset+rows), max(0, w.Rows-w.Page))
	return w
}

// PageSize is the number of rows a page jump moves.
func (w Window) PageSize() int { return max(1, w.Page) }

// Panel renders the shared understated, responsive bordered surface. It holds
// only theme styles, so rendering is a pure function of its arguments.
type Panel struct {
	styles styles.Panel
}

// NewPanel creates a panel with the supplied shared theme styles.
func NewPanel(sty styles.Panel) *Panel { return &Panel{styles: sty} }

// SetStyles replaces the panel's theme-derived styles.
func (p *Panel) SetStyles(sty styles.Panel) { p.styles = sty }

// View renders and centers a full panel when it fits, otherwise a bounded
// compact or one-line fallback. The returned string never exceeds width/height.
func (p *Panel) View(width, height int, content PanelContent) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, p.Render(width, height, content))
}

// Render returns the panel at its natural height for embedding in another
// layout. It uses the same bounded full and compact states as View without
// adding the surrounding centering space.
func (p *Panel) Render(width, height int, content PanelContent) string {
	rendered, _ := p.Layout(width, height, content)
	return rendered
}

// Layout is Render that also reports the Window of a scrollable body it showed.
// The window is zero when the compact fallback is shown.
func (p *Panel) Layout(width, height int, content PanelContent) (string, Window) {
	if width <= 0 || height <= 0 {
		return "", Window{}
	}
	if full, window, ok := p.fit(width, height, content); ok {
		return full, window
	}
	return p.compact(width, height, content), Window{}
}

// Fits reports whether Render shows the full layout rather than a compact
// fallback, so a caller can disable controls the fallback hides.
func (p *Panel) Fits(width, height int, content PanelContent) bool {
	_, _, ok := p.fit(width, height, content)
	return ok
}

func (p *Panel) fit(width, height int, content PanelContent) (string, Window, bool) {
	full, window := p.full(width, height, content)
	return full, window, full != "" && lipgloss.Width(full) <= width && lipgloss.Height(full) <= height
}

// BodySize is the body area the full layout leaves at width×height, below a
// header and above an optional footer. Components that size a list or a
// document outside rendering read it instead of re-deriving the frame.
func (p *Panel) BodySize(width, height int, footer bool) (int, int) {
	outerWidth := width - 2*max(0, p.styles.HorizontalMargin)
	if p.styles.MaxWidth > 0 {
		outerWidth = min(outerWidth, p.styles.MaxWidth)
	}
	bodyHeight := height - p.styles.Frame.GetVerticalFrameSize() - 1 - p.gapRows()
	if footer {
		bodyHeight -= 1 + p.gapRows()
	}
	return outerWidth - p.styles.Frame.GetHorizontalFrameSize(), bodyHeight
}

func (p *Panel) gapRows() int { return max(1, p.styles.SectionGap+1) }

func (p *Panel) full(width, height int, content PanelContent) (string, Window) {
	hasFooter := content.FooterLeft != "" || content.FooterRight != ""
	bodyWidth, bodyHeight := p.BodySize(width, height, hasFooter)
	if bodyWidth < max(1, content.MinBodyWidth) {
		return "", Window{}
	}

	var window Window
	sections := []string{p.header(bodyWidth, content.Title, content.Dismiss)}
	switch {
	case content.ScrollableBody != nil:
		if bodyHeight <= 0 {
			return "", Window{}
		}
		var body string
		body, window = p.scrollableBody(bodyWidth, bodyHeight, content)
		sections = append(sections, body)
	case content.Body != nil:
		sections = append(sections, content.Body(bodyWidth, bodyHeight))
	}
	if hasFooter {
		sections = append(sections, p.footer(bodyWidth, content.FooterLeft, content.FooterRight))
	}
	sections = slices.DeleteFunc(sections, func(section string) bool { return section == "" })
	gap := strings.Repeat("\n", p.gapRows())
	framed := p.styles.Frame.Width(bodyWidth + p.styles.Frame.GetHorizontalFrameSize()).Render(strings.Join(sections, gap))
	return framed, window
}

func (p *Panel) scrollableBody(width, height int, content PanelContent) (string, Window) {
	header, body, footer := "", content.ScrollableBody(width), ""
	if content.BodyHeader != nil {
		header = content.BodyHeader(width)
	}
	if content.BodyFooter != nil {
		footer = content.BodyFooter(width)
		if footer != "" && content.BodyFooterGap > 0 {
			footer = strings.Repeat("\n", content.BodyFooterGap) + footer
		}
	}
	rows := strings.Split(body, "\n")
	if body == "" {
		rows = nil
	}

	parts := []string{header, body, footer}
	fixedHeight, separators := 0, -1
	for _, part := range parts {
		if part == "" {
			continue
		}
		fixedHeight += lipgloss.Height(part)
		separators++
	}
	available := height - fixedHeight + len(rows) - max(0, separators)
	if available < 1 || len(rows) <= available {
		return joinNonEmpty(parts...), Window{Rows: len(rows), Page: len(rows)}
	}

	pageSize := available - 1 // reserve a row for position and key help
	if pageSize < 1 {
		// The natural body makes the full layout fail its height check and lets
		// the panel select its existing compact fallback.
		return joinNonEmpty(parts...), Window{}
	}
	window := Window{Rows: len(rows), Page: pageSize}.Scrolled(content.ScrollOffset)
	end := min(len(rows), window.Offset+pageSize)
	position := fmt.Sprintf("lines %d–%d of %d", window.Offset+1, end, len(rows))
	if content.ScrollHint != "" {
		position += " · " + content.ScrollHint
	}
	middle := strings.Join(rows[window.Offset:end], "\n") + "\n" +
		p.styles.Help.Render(ansi.Truncate(position, width, "…"))
	return joinNonEmpty(header, middle, footer), window
}

func joinNonEmpty(parts ...string) string {
	nonEmpty := parts[:0]
	for _, part := range parts {
		if part != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}
	return strings.Join(nonEmpty, "\n")
}

func (p *Panel) header(width int, title, dismiss string) string {
	title = ansi.Truncate(title, width, "…")
	if dismiss == "" {
		return p.styles.Title.Render(title)
	}
	dismissWidth := ansi.StringWidth(dismiss)
	if dismissWidth >= width {
		return p.styles.Title.Render(title)
	}
	title = ansi.Truncate(title, max(1, width-dismissWidth-1), "…")
	gap := max(1, width-ansi.StringWidth(title)-dismissWidth)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		p.styles.Title.Render(title),
		strings.Repeat(" ", gap),
		p.styles.Dismiss.Render(dismiss),
	)
}

func (p *Panel) footer(width int, left, right string) string {
	left = p.styles.Help.Render(left)
	right = p.styles.Help.Render(right)
	if right == "" {
		return ansi.Truncate(left, width, "…")
	}
	// Keep the action visible and give the remaining space to the status text.
	right = ansi.Truncate(right, width, "…")
	leftWidth := width - ansi.StringWidth(right) - 1
	if leftWidth <= 0 {
		return right
	}
	left = ansi.Truncate(left, leftWidth, "…")
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(right)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gap), right)
}

func (p *Panel) compact(width, height int, content PanelContent) string {
	tiny := content.TinyMessage
	if tiny == "" {
		tiny = content.CompactTitle
	}
	if height < 3 || width < 20 {
		return p.styles.Compact.Render(ansi.Truncate(tiny, width, ""))
	}

	title := content.CompactTitle
	if title == "" {
		title = content.Title
	}
	compactWidth := width
	if p.styles.CompactMaxWidth > 0 {
		compactWidth = min(compactWidth, p.styles.CompactMaxWidth)
	}
	message := content.CompactMessage
	if content.CompactBody != nil {
		message = content.CompactBody(compactWidth)
	}
	if message == "" {
		message = tiny
	}
	candidate := p.styles.Compact.Width(compactWidth).Render(title + "\n\n" + message)
	if lipgloss.Width(candidate) <= width && lipgloss.Height(candidate) <= height {
		return candidate
	}
	return p.styles.Compact.Render(ansi.Truncate(tiny, width, ""))
}
