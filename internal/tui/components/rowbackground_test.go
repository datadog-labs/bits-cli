package components

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPaintRowBackgroundOverridesBackgroundOnly(t *testing.T) {
	fg := lipgloss.Color("#ff0000")
	line := lipgloss.NewStyle().Foreground(fg).Background(lipgloss.Color("#000000")).Render("hi")
	width := ansi.StringWidth(line)

	painted := PaintRowBackground(line, width, lipgloss.Color("#22242b"))

	if !strings.Contains(painted, "255;0;0") {
		t.Fatalf("painted row lost its foreground color: %q", painted)
	}
	if strings.Contains(painted, "0;0;0") {
		t.Fatalf("painted row kept its original background: %q", painted)
	}
	if !strings.Contains(painted, "34;36;43") {
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

func TestShadeRowShiftsBackgroundsByShadeOffset(t *testing.T) {
	canvas, shade := lipgloss.Color("#101010"), lipgloss.Color("#202020")
	line := lipgloss.NewStyle().Background(lipgloss.Color("#0A2F10")).Render("+") + "x"

	painted := ShadeRow(line, 2, canvas, shade)

	if !strings.Contains(painted, "26;63;32") { // #1A3F20: #0A2F10 lifted by #101010
		t.Fatalf("styled cell lost its own background: %q", painted)
	}
	if !strings.Contains(painted, "32;32;32") { // plain cell sits on canvas, so it becomes shade
		t.Fatalf("plain cell missing the shade: %q", painted)
	}
}
