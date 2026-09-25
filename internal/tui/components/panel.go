// Package components contains reusable presentation and interaction primitives
// shared by top-level TUI screens.
package components

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// PanelContent supplies screen-specific copy and a width-aware body to Panel.
type PanelContent struct {
	Title   string
	Dismiss string
	Body    func(width int) string
	// BodyHeader, ScrollableBody, and BodyFooter form an optional bounded
	// layout whose scroll state is owned by Panel. ScrollableBody must return
	// display-width-wrapped rows; Panel performs vertical paging only.
	BodyHeader     func(width int) string
	ScrollableBody func(width int) string
	BodyFooter     func(width int) string
	BodyFooterGap  int
	FooterLeft     string
	FooterRight    string

	CompactTitle   string
	CompactMessage string
	CompactBody    func(width int) string
	TinyMessage    string
}

// Panel renders the shared understated, responsive bordered surface.
type Panel struct {
	styles         styles.Panel
	scrollOffset   int
	scrollPageSize int
	scrollRows     int
}

// NewPanel creates a panel with the supplied shared theme styles.
func NewPanel(sty styles.Panel) *Panel { return &Panel{styles: sty} }

// SetStyles replaces the panel's theme-derived styles.
func (p *Panel) SetStyles(sty styles.Panel) { p.styles = sty }

// ResetScroll returns an optional scrollable body to its first row.
func (p *Panel) ResetScroll() {
	p.scrollOffset = 0
	p.scrollPageSize = 0
	p.scrollRows = 0
}

// ScrollBy moves an optional scrollable body by rows. Rendering normalizes the
// offset again after content or dimensions change.
func (p *Panel) ScrollBy(rows int) {
	maxOffset := max(0, p.scrollRows-p.scrollPageSize)
	p.scrollOffset = min(max(0, p.scrollOffset+rows), maxOffset)
}

// PageUp and PageDown move an optional scrollable body by one visible page.
func (p *Panel) PageUp()   { p.ScrollBy(-max(1, p.scrollPageSize)) }
func (p *Panel) PageDown() { p.ScrollBy(max(1, p.scrollPageSize)) }

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
	if width <= 0 || height <= 0 {
		return ""
	}
	full := p.full(width, height, content)
	if full != "" && lipgloss.Width(full) <= width && lipgloss.Height(full) <= height {
		return full
	}
	return p.compact(width, height, content)
}

func (p *Panel) full(width, height int, content PanelContent) string {
	margin := max(0, p.styles.HorizontalMargin)
	available := width - 2*margin
	if available <= p.styles.Frame.GetHorizontalFrameSize() {
		return ""
	}
	outerWidth := available
	if p.styles.MaxWidth > 0 {
		outerWidth = min(outerWidth, p.styles.MaxWidth)
	}
	bodyWidth := outerWidth - p.styles.Frame.GetHorizontalFrameSize()
	if bodyWidth < 1 {
		return ""
	}

	header := p.header(bodyWidth, content.Title, content.Dismiss)
	footer := ""
	if content.FooterLeft != "" || content.FooterRight != "" {
		footer = p.footer(bodyWidth, content.FooterLeft, content.FooterRight)
	}
	gapRows := max(1, p.styles.SectionGap+1)
	bodyHeight := height - p.styles.Frame.GetVerticalFrameSize() - lipgloss.Height(header)
	if content.Body != nil || content.ScrollableBody != nil {
		bodyHeight -= gapRows
	}
	if footer != "" {
		bodyHeight -= lipgloss.Height(footer) + gapRows
	}

	body := ""
	if content.ScrollableBody != nil {
		if bodyHeight <= 0 {
			return ""
		}
		body = p.scrollableBody(bodyWidth, bodyHeight, content)
	} else if content.Body != nil {
		body = content.Body(bodyWidth)
	}
	sections := []string{header}
	if body != "" {
		sections = append(sections, body)
	}
	if footer != "" {
		sections = append(sections, footer)
	}
	gap := strings.Repeat("\n", gapRows)
	return p.styles.Frame.Width(outerWidth).Render(strings.Join(sections, gap))
}

func (p *Panel) scrollableBody(width, height int, content PanelContent) string {
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
		p.scrollOffset = 0
		p.scrollPageSize = len(rows)
		p.scrollRows = len(rows)
		return joinNonEmpty(parts...)
	}

	pageSize := available - 1 // reserve a row for position and key help
	if pageSize < 1 {
		// The natural body makes the full layout fail its height check and lets
		// the panel select its existing compact fallback.
		return joinNonEmpty(parts...)
	}
	p.scrollRows = len(rows)
	p.scrollPageSize = pageSize
	maxOffset := len(rows) - pageSize
	p.scrollOffset = min(max(0, p.scrollOffset), maxOffset)
	end := min(len(rows), p.scrollOffset+pageSize)
	position := fmt.Sprintf("lines %d–%d of %d · pgup/pgdown scroll", p.scrollOffset+1, end, len(rows))
	middle := strings.Join(rows[p.scrollOffset:end], "\n") + "\n" +
		p.styles.Help.Render(ansi.Truncate(position, width, "…"))
	return joinNonEmpty(header, middle, footer)
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
	gap := width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap > 0 {
		return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gap), right)
	}
	joined := left + p.styles.FooterSeparator + right
	return ansi.Truncate(joined, width, "…")
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
