package components

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

func newChoicePrompt(selected int) *ChoicePrompt {
	p := NewChoicePrompt("Share conversation", []Option{
		{Label: "Private", Description: "Only you can view this conversation."},
		{Label: "Shared", Description: "Anyone in your organization with the link can view all current and future messages."},
	}, selected)
	p.SetStyles(styles.Default(true))
	return p
}

func TestChoicePromptKeys(t *testing.T) {
	tests := []struct {
		name          string
		selected      int
		keys          []tea.KeyPressMsg
		wantIndex     int
		wantDismissed bool
	}{
		{name: "enter picks the initial option", selected: 1, keys: []tea.KeyPressMsg{{Code: tea.KeyEnter}}, wantIndex: 1},
		{name: "up moves to the previous option", selected: 1, keys: []tea.KeyPressMsg{{Code: tea.KeyUp}, {Code: tea.KeyEnter}}, wantIndex: 0},
		{name: "down wraps around", selected: 1, keys: []tea.KeyPressMsg{{Code: tea.KeyDown}, {Code: tea.KeyEnter}}, wantIndex: 0},
		{name: "tab cycles", selected: 0, keys: []tea.KeyPressMsg{{Code: tea.KeyTab}, {Code: tea.KeyEnter}}, wantIndex: 1},
		{name: "esc dismisses", selected: 1, keys: []tea.KeyPressMsg{{Code: tea.KeyEscape}}, wantDismissed: true},
		{name: "an out-of-range selection is clamped", selected: 5, keys: []tea.KeyPressMsg{{Code: tea.KeyEnter}}, wantIndex: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newChoicePrompt(tt.selected)
			for _, key := range tt.keys {
				if _, _, done := p.Result(); done {
					t.Fatal("the prompt finished before its last key")
				}
				p.Update(key)
			}
			index, dismissed, done := p.Result()
			if !done || dismissed != tt.wantDismissed || !dismissed && index != tt.wantIndex {
				t.Fatalf("Result() = (%d, %v, %v), want (%d, %v, true)", index, dismissed, done, tt.wantIndex, tt.wantDismissed)
			}
		})
	}
}

func TestChoicePromptLayout(t *testing.T) {
	p := newChoicePrompt(1)
	view, answerable := p.Layout(Slot{Width: 50, Height: 20})
	plain := ansi.Strip(view)
	if !answerable || !strings.Contains(plain, "› Shared") {
		t.Fatalf("answerable=%v view:\n%s", answerable, plain)
	}
	// The long description wraps under its own column instead of truncating.
	if !strings.Contains(plain, "future messages.") || strings.Contains(plain, "…") {
		t.Fatalf("description was cut:\n%s", plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if w := ansi.StringWidth(line); w > 50 {
			t.Fatalf("row is %d cells wide, past the slot:\n%s", w, plain)
		}
	}
	rows := strings.Count(view, "\n") + 1
	if _, answerable := p.Layout(Slot{Width: 50, Height: rows - 1}); answerable {
		t.Fatal("a slot shorter than the prompt was answerable")
	}
	if _, used := p.Update(tea.MouseClickMsg{Button: tea.MouseLeft}); used {
		t.Fatal("the prompt kept a click it does not handle")
	}
}
