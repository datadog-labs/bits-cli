package components

import (
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// Accordion renders the disclosure control for a block's detail rows. The
// caller owns the expanded state and the click target; hover is painted by the
// caller across the whole row (see PaintRowBackground).
func Accordion(sty styles.Accordion, expanded bool) string {
	glyph := sty.Collapsed
	if expanded {
		glyph = sty.Expanded
	}
	return sty.Resting.Render(glyph)
}

// AccordionWidth returns the cells Accordion occupies. It is state-independent,
// so toggling never reflows the row.
func AccordionWidth(sty styles.Accordion) int {
	return sty.Resting.GetHorizontalFrameSize() +
		max(ansi.StringWidth(sty.Expanded), ansi.StringWidth(sty.Collapsed))
}
