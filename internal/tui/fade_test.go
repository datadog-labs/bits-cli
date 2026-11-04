package tui

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

var fadeTestBackground = lipgloss.Color("#171921")

func hexOf(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02X%02X%02X", r>>8, g>>8, b>>8)
}

func fadedCell(t *testing.T, content string, text color.Color, x int) uv.Cell {
	t.Helper()
	faded := fade(content, ansi.StringWidth(content), 1, text, fadeTestBackground)
	buf := uv.NewScreenBuffer(ansi.StringWidth(content), 1)
	buf.Method = ansi.GraphemeWidth
	uv.NewStyledString(faded).Draw(&buf, buf.Bounds())
	return buf.Lines[0][x]
}

func TestFadeColor(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"brand green", "#41C464", "#224432"},
		{"white", "#FFFFFF", "#515359"},
		{"background", "#171921", "#171921"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := hexOf(fadeColor(lipgloss.Color(test.in), fadeTestBackground))
			if got != test.want {
				t.Fatalf("fadeColor(%s) = %s, want %s", test.in, got, test.want)
			}
		})
	}
}

func TestFadeBlendsForegroundAndBackground(t *testing.T) {
	content := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(lipgloss.Color("#8B80F9")).
		Render("x")

	cell := fadedCell(t, content, lipgloss.Color("#E6E8EE"), 0)

	if got := hexOf(cell.Style.Fg); got != "#515359" {
		t.Errorf("fg = %s, want #515359", got)
	}
	if got := hexOf(cell.Style.Bg); got != "#343357" {
		t.Errorf("bg = %s, want #343357", got)
	}
}

func TestFadeDefaultForegroundUsesText(t *testing.T) {
	cell := fadedCell(t, "hi", lipgloss.Color("#E6E8EE"), 0)

	if got := hexOf(cell.Style.Fg); got != "#4B4D54" {
		t.Errorf("fg = %s, want #4B4D54", got)
	}
	if cell.Style.Bg != nil {
		t.Errorf("bg = %s, want nil so the terminal background shows through", hexOf(cell.Style.Bg))
	}
}

func TestFadeIndexedColor(t *testing.T) {
	content := lipgloss.NewStyle().Foreground(lipgloss.Color("236")).Render("x")

	cell := fadedCell(t, content, lipgloss.Color("#E6E8EE"), 0)

	if got := hexOf(cell.Style.Fg); got != "#1D1F25" {
		t.Errorf("fg = %s, want #1D1F25", got)
	}
}

func TestFadeKeepsGraphemeColumns(t *testing.T) {
	lines := []string{
		lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Render("🙂 漢字 ok"),
		"plain 🙂 a",
	}
	content := strings.Join(lines, "\n")
	width := 0
	for _, line := range lines {
		width = max(width, ansi.StringWidth(line))
	}

	faded := strings.Split(fade(content, width, len(lines), lipgloss.Color("#E6E8EE"), fadeTestBackground), "\n")

	for i, line := range lines {
		if got, want := ansi.StringWidth(faded[i]), ansi.StringWidth(line); got != want {
			t.Errorf("line %d width = %d, want %d", i, got, want)
		}
		if got, want := ansi.Strip(faded[i]), ansi.Strip(line); got != want {
			t.Errorf("line %d text = %q, want %q", i, got, want)
		}
	}
}
