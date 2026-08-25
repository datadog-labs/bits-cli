package catalog

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// wrapBody hard-wraps every line of s to width, breaking any line that exceeds
// it exactly as the terminal itself would. Lines already within width (glamour's
// word-wrapped output, the exact-width delimiter) are left untouched. This keeps
// the "\n"-based scroll model in sync with the rows the terminal actually shows.
func wrapBody(s string, width int) string {
	if width < 1 {
		return s
	}
	return ansi.Hardwrap(s, width, false)
}

// delimiter is a dim full-width horizontal rule separating samples.
func delimiter(width int) string {
	if width < 1 {
		width = 1
	}
	return lipgloss.NewStyle().Faint(true).Render(strings.Repeat("─", width))
}

// renderSample pairs a dim label with its rendered sample beneath it.
func renderSample(label, sample string) string {
	return lipgloss.NewStyle().Faint(true).Render(label) + "\n" + sample
}

// footer is the always-visible status line: title, position, mode, key hints.
func footer(width int, g group, isDark bool) string {
	mode := "LIGHT"
	if isDark {
		mode = "DARK"
	}
	left := fmt.Sprintf("%s  ·  %d / %d  ·  %s", g.title(), int(g)+1, int(numGroups), mode)
	hints := "tab/→ next · shift+tab/← prev · ↑↓ scroll · t theme · q quit"
	line := left + "   " + hints
	return lipgloss.NewStyle().Faint(true).Render(ansi.Truncate(line, max(1, width), "…"))
}

// hexOf formats a color as #RRGGBB, or "—" when unset.
func hexOf(c color.Color) string {
	if c == nil {
		return "—"
	}
	if _, ok := c.(lipgloss.NoColor); ok {
		return "—"
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02X%02X%02X", uint8(r>>8), uint8(g>>8), uint8(b>>8))
}

// swatch renders a color chip, its hex, and a label.
func swatch(label string, c color.Color) string {
	chip := lipgloss.NewStyle().Background(c).Render("    ")
	return chip + " " + hexOf(c) + "  " + lipgloss.NewStyle().Faint(true).Render(label)
}

// visibleLines returns the height-line window of body starting at offset.
func visibleLines(body string, offset, height int) string {
	if height < 1 {
		return ""
	}
	lines := strings.Split(body, "\n")
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	end := offset + height
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[offset:end], "\n")
}
