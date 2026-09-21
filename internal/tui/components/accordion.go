package components

import (
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// AccordionState is how one control should be drawn. The caller owns it: the
// transcript renders from immutable blocks and caches rows per revision, so
// disclosure state has to live with the screen that survives a re-render
// rather than inside the control.
type AccordionState struct {
	Expanded bool
}

// Accordion renders the disclosure glyph for a block's detail rows. Toggling
// is not this type's concern: the click target is the whole header row, and
// deciding whether a mouse-up on that row is a toggle or the end of a text
// drag requires the selection state Model already owns, so Model resolves
// that gesture itself and calls chat.List.ToggleDisclosure directly.
type Accordion struct {
	styles styles.Accordion
	width  int
}

// NewAccordion creates a control with the supplied shared theme styles.
func NewAccordion(sty styles.Accordion) *Accordion {
	a := &Accordion{}
	a.SetStyles(sty)
	return a
}

// SetStyles replaces the control's theme-derived styles.
func (a *Accordion) SetStyles(sty styles.Accordion) {
	a.styles = sty
	a.width = sty.Resting.GetHorizontalFrameSize() +
		max(ansi.StringWidth(sty.Expanded), ansi.StringWidth(sty.Collapsed))
}

// Render draws the control's glyph. Hover is not reflected here: the caller
// repaints the whole header row's background via PaintRowBackground, which
// covers the control's own cells too.
func (a *Accordion) Render(state AccordionState) string {
	return a.styles.Resting.Render(a.glyph(state.Expanded))
}

// Width returns the cells the control occupies. It is state-independent, so a
// header keeps its layout when the control is toggled or hovered.
func (a *Accordion) Width() int {
	return a.width
}

func (a *Accordion) glyph(expanded bool) string {
	if expanded {
		return a.styles.Expanded
	}
	return a.styles.Collapsed
}
