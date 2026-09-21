package components

import (
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPaintRowBackgroundOverridesBackgroundOnly(t *testing.T) {
	fg := lipgloss.Color("#ff0000")
	line := lipgloss.NewStyle().Foreground(fg).Background(lipgloss.Color("#000000")).Render("hi")
	width := ansi.StringWidth(line)

	painted := PaintRowBackground(line, width, lipgloss.Color("#22242b"))

	if !colorPresent(painted, "255;0;0") {
		t.Fatalf("painted row lost its foreground color: %q", painted)
	}
	if colorPresent(painted, "0;0;0") {
		t.Fatalf("painted row kept its original background: %q", painted)
	}
	if !colorPresent(painted, "34;36;43") {
		t.Fatalf("painted row missing the new background (#22242b): %q", painted)
	}
}

func TestPaintRowBackgroundFillsBeyondContentWidth(t *testing.T) {
	line := lipgloss.NewStyle().Render("hi")
	painted := PaintRowBackground(line, 10, lipgloss.Color("#22242b"))
	if got := ansi.StringWidth(ansi.Strip(painted)); got != 10 {
		t.Fatalf("painted row width = %d, want 10", got)
	}
}

func colorPresent(s, rgb string) bool {
	for i := 0; i+len(rgb) <= len(s); i++ {
		if s[i:i+len(rgb)] == rgb {
			return true
		}
	}
	return false
}
