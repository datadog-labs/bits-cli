package components

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

func newTextPrompt(value string, limit int) *TextPrompt {
	p := NewTextPrompt("Rename conversation", value, limit)
	p.SetStyles(styles.Default(true))
	return p
}

func TestTextPromptSubmitsTrimmedText(t *testing.T) {
	p := newTextPrompt("", 0)
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, _, done := p.Result(); done {
		t.Fatal("enter submitted an empty value")
	}
	p.Update(tea.PasteMsg{Content: "  Prod outage  "})
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if value, dismissed, done := p.Result(); value != "Prod outage" || dismissed || !done {
		t.Fatalf("Result() = (%q, %v, %v)", value, dismissed, done)
	}
	if _, used := p.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}); !used {
		t.Fatal("a finished prompt released input to the host")
	}
}

func TestTextPromptEscDismisses(t *testing.T) {
	p := newTextPrompt("draft", 0)
	p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if value, dismissed, done := p.Result(); value != "" || !dismissed || !done {
		t.Fatalf("Result() = (%q, %v, %v)", value, dismissed, done)
	}
}

func TestTextPromptKeepsTypingWithinLimit(t *testing.T) {
	p := newTextPrompt("ab", 3)
	for _, r := range "cde" {
		p.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if value, _, _ := p.Result(); value != "abc" {
		t.Fatalf("value = %q, want the limited input", value)
	}
}

func TestTextPromptLayout(t *testing.T) {
	p := newTextPrompt("Prod outage", 0)
	view, answerable := p.Layout(Slot{Width: 60, Height: 10})
	if !answerable || !strings.Contains(view, "Rename conversation") || !strings.Contains(view, "Prod outage") {
		t.Fatalf("answerable=%v view=%q", answerable, view)
	}
	if p.Placement() != ReplacesInput {
		t.Fatal("the prompt should replace the composer")
	}
	if _, answerable := p.Layout(Slot{Width: 60, Height: textPromptHeight - 1}); answerable {
		t.Fatal("a slot shorter than the prompt was answerable")
	}
	if _, used := p.Update(tea.MouseClickMsg{Button: tea.MouseLeft}); used {
		t.Fatal("the prompt kept a click it does not handle")
	}
}
