package components

import (
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func TestAccordionWidthIsStableAcrossStates(t *testing.T) {
	accordion := NewAccordion(styles.Default(true).Accordion)
	want := accordion.Width()
	for _, state := range []AccordionState{
		{},
		{Expanded: true},
	} {
		if got := ansi.StringWidth(accordion.Render(state)); got != want {
			t.Fatalf("state %+v rendered %d cells, want %d", state, got, want)
		}
	}
}

func TestAccordionGlyphFollowsDisclosureState(t *testing.T) {
	sty := styles.Default(true).Accordion
	accordion := NewAccordion(sty)
	if got := ansi.Strip(accordion.Render(AccordionState{Expanded: true})); got != " "+sty.Expanded+" " {
		t.Fatalf("expanded glyph = %q, want %q", got, sty.Expanded)
	}
	if got := ansi.Strip(accordion.Render(AccordionState{})); got != " "+sty.Collapsed+" " {
		t.Fatalf("collapsed glyph = %q, want %q", got, sty.Collapsed)
	}
}

