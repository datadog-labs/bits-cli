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

// delimiter is a dim full-width horizontal rule separating samples. chrome is
// the guide's own text color, passed in rather than derived from Faint: the
// guide paints the same pinned background the app does, so an unset foreground
// here would be the terminal's, dimmed.
func delimiter(width int, chrome lipgloss.Style) string {
	if width < 1 {
		width = 1
	}
	return chrome.Render(strings.Repeat("─", width))
}

// renderSample pairs a sample with the title naming it. The title takes the
// primary level: it says what is being shown, so it is content rather than
// chrome, even though the rules around it are not. tag, when set, brackets
// the label to say what kind of page the sample lives on; see groupTag. A
// blank row separates the title from what it names, so the two read as a
// caption over a picture rather than one more line of it.
func renderSample(title lipgloss.Style, tag, label, sample string) string {
	return title.Render(tagLabel(tag, label)) + "\n\n" + sample
}

// tagLabel prefixes label with "[tag] " when tag is set, otherwise returns it
// unchanged.
func tagLabel(tag, label string) string {
	if tag == "" {
		return label
	}
	return "[" + tag + "] " + label
}

// footer is the always-visible status line: title, position, mode, key hints.
func footer(width int, g group, isDark bool, chrome lipgloss.Style) string {
	mode := "LIGHT"
	if isDark {
		mode = "DARK"
	}
	left := fmt.Sprintf("%s  ·  %d / %d  ·  %s", g.title(), int(g)+1, int(numGroups), mode)
	hints := "tab/→ next · shift+tab/← prev · ↑↓ scroll · t theme · q quit"
	line := left + "   " + hints
	return chrome.Render(ansi.Truncate(line, max(1, width), "…"))
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
func swatch(title lipgloss.Style, label string, c color.Color) string {
	chip := lipgloss.NewStyle().Background(c).Render("    ")
	return chip + " " + hexOf(c) + "  " + title.Render(label)
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
