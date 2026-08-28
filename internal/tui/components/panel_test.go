package components

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func TestPanelFullAndFallbackViewsStayBounded(t *testing.T) {
	panel := NewPanel(styles.Default(true).Panel)
	content := PanelContent{
		Title:          "Choose an item",
		Dismiss:        "esc ×",
		Body:           func(width int) string { return "body at width " + strings.Repeat("x", max(0, width-14)) },
		FooterLeft:     "↑/↓ navigate",
		FooterRight:    "enter to continue",
		CompactTitle:   "Choose an item",
		CompactMessage: "Resize the terminal to continue.",
		TinyMessage:    "Resize to continue",
	}
	for _, size := range []struct{ width, height int }{{100, 24}, {36, 8}, {10, 2}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			view := panel.View(size.width, size.height, content)
			assertBounded(t, view, size.width, size.height)
		})
	}

	full := ansi.Strip(panel.View(100, 24, content))
	for _, want := range []string{"Choose an item", "esc ×", "body at width", "↑/↓ navigate", "enter to continue"} {
		if !strings.Contains(full, want) {
			t.Errorf("full panel missing %q", want)
		}
	}
	if tiny := ansi.Strip(panel.View(10, 2, content)); !strings.Contains(tiny, "Resize") {
		t.Fatalf("tiny panel = %q", tiny)
	}
}

func TestPanelBodyReceivesInnerWidth(t *testing.T) {
	theme := styles.Default(true)
	panel := NewPanel(theme.Panel)
	got := 0
	_ = panel.View(80, 20, PanelContent{
		Title: "Title",
		Body: func(width int) string {
			got = width
			return "body"
		},
	})
	outer := min(theme.Panel.MaxWidth, 80-2*theme.Panel.HorizontalMargin)
	want := outer - theme.Panel.Frame.GetHorizontalFrameSize()
	if got != want {
		t.Fatalf("body width = %d, want %d", got, want)
	}
}

func assertBounded(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		t.Fatalf("height = %d, want <= %d", len(lines), height)
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("line %d width = %d, want <= %d", i+1, got, width)
		}
	}
}
