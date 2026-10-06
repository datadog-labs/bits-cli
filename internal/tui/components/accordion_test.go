package components

import (
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

// TestAccordionRendersStateGlyphAtFixedWidth: toggling swaps the glyph but
// never the width, so a header does not reflow.
func TestAccordionRendersStateGlyphAtFixedWidth(t *testing.T) {
	sty := styles.Default(true).Accordion
	for expanded, glyph := range map[bool]string{true: sty.Expanded, false: sty.Collapsed} {
		got := Accordion(sty, expanded)
		if plain := ansi.Strip(got); plain != " "+glyph+" " {
			t.Errorf("expanded=%t rendered %q, want %q", expanded, plain, " "+glyph+" ")
		}
		if w := ansi.StringWidth(got); w != AccordionWidth(sty) {
			t.Errorf("expanded=%t rendered %d cells, want %d", expanded, w, AccordionWidth(sty))
		}
	}
}
