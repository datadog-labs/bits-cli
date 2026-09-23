package components

import (
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func TestAccordionWidthIsStableAcrossStates(t *testing.T) {
	sty := styles.Default(true).Accordion
	want := AccordionWidth(sty)
	for _, expanded := range []bool{false, true} {
		if got := ansi.StringWidth(Accordion(sty, expanded)); got != want {
			t.Fatalf("expanded=%t rendered %d cells, want %d", expanded, got, want)
		}
	}
}

func TestAccordionGlyphFollowsDisclosureState(t *testing.T) {
	sty := styles.Default(true).Accordion
	if got := ansi.Strip(Accordion(sty, true)); got != " "+sty.Expanded+" " {
		t.Fatalf("expanded glyph = %q, want %q", got, sty.Expanded)
	}
	if got := ansi.Strip(Accordion(sty, false)); got != " "+sty.Collapsed+" " {
		t.Fatalf("collapsed glyph = %q, want %q", got, sty.Collapsed)
	}
}
