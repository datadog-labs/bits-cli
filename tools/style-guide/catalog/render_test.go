package catalog

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestDelimiterWidth(t *testing.T) {
	if got := ansi.StringWidth(delimiter(10)); got != 10 {
		t.Fatalf("delimiter width = %d, want 10", got)
	}
}

func TestHexOf(t *testing.T) {
	if got := hexOf(lipgloss.Color("#5e6dd6")); got != "#5E6DD6" {
		t.Fatalf("hexOf hex = %q, want #5E6DD6", got)
	}
	if got := hexOf(lipgloss.NoColor{}); got != "—" {
		t.Fatalf("hexOf NoColor = %q, want —", got)
	}
	if got := hexOf(nil); got != "—" {
		t.Fatalf("hexOf nil = %q, want —", got)
	}
}

func TestFooterContainsPositionAndMode(t *testing.T) {
	f := footer(200, groupMarkdown, true)
	if !strings.Contains(f, "4 / 5") {
		t.Fatalf("footer missing position: %q", f)
	}
	if !strings.Contains(f, "DARK") {
		t.Fatalf("footer missing mode: %q", f)
	}
}

func TestVisibleLines(t *testing.T) {
	body := "l0\nl1\nl2\nl3\nl4"
	if got := visibleLines(body, 1, 2); got != "l1\nl2" {
		t.Fatalf("visibleLines = %q, want l1\\nl2", got)
	}
	if got := visibleLines(body, 4, 10); got != "l4" {
		t.Fatalf("visibleLines tail = %q, want l4", got)
	}
}
