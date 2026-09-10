package components

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func TestSelectorNavigationWrapsAndLeavesActionsToParent(t *testing.T) {
	selector := NewSelector([]Choice{{Label: "one"}, {Label: "two"}}, styles.Default(true).Selector)
	if !selector.UpdateKey("up") || selector.Index() != 1 {
		t.Fatalf("up selected %d, want wrapped index 1", selector.Index())
	}
	if !selector.UpdateKey("down") || selector.Index() != 0 {
		t.Fatalf("down selected %d, want wrapped index 0", selector.Index())
	}
	if selector.UpdateKey("enter") || selector.UpdateKey("esc") {
		t.Fatal("selector consumed parent action key")
	}
	choice, ok := selector.Selected()
	if !ok || choice.Label != "one" {
		t.Fatalf("selected = %#v, %t", choice, ok)
	}
}

func TestSelectorViewIsBoundedAndTruncatesDetailFirst(t *testing.T) {
	selector := NewSelector([]Choice{
		{Label: "US1", Detail: "app.datadoghq.com"},
		{Label: "Custom", Detail: "Enter another very long domain name"},
	}, styles.Default(true).Selector)

	for _, width := range []int{40, 18, 8, 1} {
		view := selector.View(width)
		for lineNo, line := range strings.Split(view, "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Fatalf("width %d line %d rendered %d cells: %q", width, lineNo+1, got, ansi.Strip(line))
			}
		}
	}
	plain := ansi.Strip(selector.View(40))
	if !strings.Contains(plain, "› US1") || !strings.Contains(plain, "app.datadoghq.com") {
		t.Fatalf("wide selector = %q", plain)
	}
	if !strings.Contains(ansi.Strip(selector.View(18)), "US1") {
		t.Fatal("narrow selector dropped primary label")
	}
}

func TestSelectorSetIndexClamps(t *testing.T) {
	selector := NewSelector([]Choice{{Label: "one"}, {Label: "two"}}, styles.Default(true).Selector)
	selector.SetIndex(99)
	if selector.Index() != 1 {
		t.Fatalf("high index = %d", selector.Index())
	}
	selector.SetIndex(-1)
	if selector.Index() != 0 {
		t.Fatalf("low index = %d", selector.Index())
	}
}

func TestSelectorCompactDetailDoesNotPadShortLabels(t *testing.T) {
	selector := NewSelector([]Choice{
		{Label: "/web", Detail: "open in Datadog"},
		{Label: "/resume", Detail: "resume a conversation"},
	}, styles.Default(true).Selector)
	selector.SetCompactDetail(true)

	plain := ansi.Strip(selector.View(40))
	if !strings.Contains(plain, "/web  open in Datadog") {
		t.Fatalf("compact selector padded short label: %q", plain)
	}
	if strings.Contains(plain, "/web     open in Datadog") {
		t.Fatalf("compact selector retained aligned-column gap: %q", plain)
	}
}

func TestSelectorFillWidthMakesEveryRowOpaqueWidth(t *testing.T) {
	sty := styles.Default(true).Selector
	sty.Item = sty.Item.Background(lipgloss.Color("#123456"))
	sty.Selected = sty.Selected.Background(lipgloss.Color("#654321"))
	sty.Detail = sty.Detail.Background(lipgloss.Color("#123456"))
	sty.SelectedDetail = sty.SelectedDetail.Background(lipgloss.Color("#654321"))
	selector := NewSelector([]Choice{
		{Label: "short", Detail: "detail"},
		{Label: "longer label", Detail: "more detail"},
	}, sty)
	selector.SetCompactDetail(true)
	selector.SetFillWidth(true)

	for lineNo, line := range strings.Split(selector.View(32), "\n") {
		if got := ansi.StringWidth(line); got != 32 {
			t.Fatalf("row %d width = %d, want 32: %q", lineNo+1, got, ansi.Strip(line))
		}
	}
}
