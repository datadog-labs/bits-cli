// Package components contains reusable presentation and interaction primitives
// shared by top-level TUI screens.
package components

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// PanelContent supplies screen-specific copy and a width-aware body to Panel.
type PanelContent struct {
	Title       string
	Dismiss     string
	Body        func(width int) string
	FooterLeft  string
	FooterRight string

	CompactTitle   string
	CompactMessage string
	TinyMessage    string
}

// Panel renders the shared understated, responsive bordered surface.
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
	full := p.full(width, content)
	if full != "" && lipgloss.Width(full) <= width && lipgloss.Height(full) <= height {
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, full)
	}
	compact := p.compact(width, height, content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, compact)
}

func (p *Panel) full(width int, content PanelContent) string {
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
	body := ""
	if content.Body != nil {
		body = content.Body(bodyWidth)
	}
	sections := []string{header}
	if body != "" {
		sections = append(sections, body)
	}
	if content.FooterLeft != "" || content.FooterRight != "" {
		sections = append(sections, p.footer(bodyWidth, content.FooterLeft, content.FooterRight))
	}
	gap := strings.Repeat("\n", max(1, p.styles.SectionGap+1))
	return p.styles.Frame.Width(outerWidth).Render(strings.Join(sections, gap))
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
	message := content.CompactMessage
	if title == "" {
		title = content.Title
	}
	if message == "" {
		message = tiny
	}
	compactWidth := width
	if p.styles.CompactMaxWidth > 0 {
		compactWidth = min(compactWidth, p.styles.CompactMaxWidth)
	}
	candidate := p.styles.Compact.Width(compactWidth).Render(title + "\n\n" + message)
	if lipgloss.Width(candidate) <= width && lipgloss.Height(candidate) <= height {
		return candidate
	}
	return p.styles.Compact.Render(ansi.Truncate(tiny, width, ""))
}
