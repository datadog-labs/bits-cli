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
		Body:           func(width, _ int) string { return "body at width " + strings.Repeat("x", max(0, width-14)) },
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
	content.FooterLeft = styles.Default(true).Feedback.Error.Render(strings.Repeat("Failed 界 ", 20))
	content.FooterRight = "enter retry"
	narrow := panel.View(40, 24, content)
	assertBounded(t, narrow, 40, 24)
	if !strings.Contains(ansi.Strip(narrow), "enter retry") {
		t.Fatalf("long error hid the retry action: %q", narrow)
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
		Body: func(width, _ int) string {
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

func TestPanelRenderReturnsNaturalHeight(t *testing.T) {
	panel := NewPanel(styles.Default(true).Panel)
	content := PanelContent{Title: "Permission required", Body: func(int, int) string { return "body" }}
	rendered := panel.Render(80, 20, content)
	if got := len(strings.Split(rendered, "\n")); got >= 20 {
		t.Fatalf("rendered height = %d, want natural height below container height", got)
	}
	assertBounded(t, rendered, 80, 20)
}

func TestPanelCompactBodyReceivesCompactWidth(t *testing.T) {
	theme := styles.Default(true)
	panel := NewPanel(theme.Panel)
	got := 0
	rendered := panel.Render(80, 8, PanelContent{
		Title: "Permission required",
		Body:  func(int, int) string { return strings.Repeat("body\n", 20) },
		CompactBody: func(width int) string {
			got = width
			return "compact actions"
		},
	})
	if got != theme.Panel.CompactMaxWidth {
		t.Fatalf("compact body width = %d, want %d", got, theme.Panel.CompactMaxWidth)
	}
	if plain := ansi.Strip(rendered); !strings.Contains(plain, "compact actions") {
		t.Fatalf("compact body missing from %q", plain)
	}
}

func TestPanelPagesAScrollableBodyThroughItsWindow(t *testing.T) {
	panel := NewPanel(styles.Default(true).Panel)
	rows := make([]string, 12)
	for i := range rows {
		rows[i] = fmt.Sprintf("detail-%02d", i+1)
	}
	content := PanelContent{
		Title:          "Permission required",
		BodyHeader:     func(int) string { return "Approve this command?" },
		ScrollableBody: func(int) string { return strings.Join(rows, "\n") },
		BodyFooter:     func(int) string { return "Deny  Allow" },
		BodyFooterGap:  1,
		ScrollHint:     "keys scroll",
	}

	first, window, _ := panel.Layout(60, 20, content)
	assertBounded(t, first, 60, 20)
	plain := ansi.Strip(first)
	if !strings.Contains(plain, "detail-01") || strings.Contains(plain, "detail-12") || !strings.Contains(plain, "lines 1–7 of 12 · keys scroll") {
		t.Fatalf("initial scroll window is incorrect:\n%s", plain)
	}

	content.ScrollOffset = window.Scrolled(window.PageSize()).Offset
	second, _, _ := panel.Layout(60, 20, content)
	assertBounded(t, second, 60, 20)
	plain = ansi.Strip(second)
	if strings.Contains(plain, "detail-01") || !strings.Contains(plain, "detail-12") {
		t.Fatalf("paged scroll window is incorrect:\n%s", plain)
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

func TestWindowScrolledClampsToTheBody(t *testing.T) {
	w := Window{Rows: 12, Page: 5}
	for _, tc := range []struct{ by, want int }{{-3, 0}, {4, 4}, {100, 7}} {
		if got := w.Scrolled(tc.by).Offset; got != tc.want {
			t.Errorf("Scrolled(%d).Offset = %d, want %d", tc.by, got, tc.want)
		}
	}
	if got := (Window{}).Scrolled(5); got != (Window{}) {
		t.Errorf("zero window scrolled to %+v", got)
	}
}

func TestPanelLayoutIsPure(t *testing.T) {
	panel := NewPanel(styles.Default(true).Panel)
	rows := make([]string, 30)
	for i := range rows {
		rows[i] = fmt.Sprintf("row-%02d", i+1)
	}
	content := PanelContent{
		Title:          "Permission required",
		ScrollableBody: func(int) string { return strings.Join(rows, "\n") },
		ScrollOffset:   9,
	}
	first, window, _ := panel.Layout(60, 16, content)
	second, again, _ := panel.Layout(60, 16, content)
	if first != second || window != again || window.Offset != 9 {
		t.Fatalf("layout is not repeatable: window=%+v again=%+v", window, again)
	}
	content.ScrollOffset = 1000
	if _, clamped, _ := panel.Layout(60, 16, content); clamped.Offset != window.Rows-window.Page {
		t.Fatalf("offset = %d, want the last page at %d", clamped.Offset, window.Rows-window.Page)
	}
}
