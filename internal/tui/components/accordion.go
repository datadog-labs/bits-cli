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
	Hovered  bool
}

// Accordion is the clickable disclosure control that expands and collapses a
// block's detail rows. It renders a fixed-width chevron box and reports the
// span it occupies so a caller can hit-test a pointer against it.
type Accordion struct {
	styles styles.Accordion

	// OnClick runs when Click reports a press on the control. It receives the
	// state the control should move to, leaving the caller to store it.
	OnClick func(next AccordionState)
}

// NewAccordion creates a control with the supplied shared theme styles.
func NewAccordion(sty styles.Accordion) *Accordion { return &Accordion{styles: sty} }

// SetStyles replaces the control's theme-derived styles.
func (a *Accordion) SetStyles(sty styles.Accordion) { a.styles = sty }

// Render draws the control in the given state.
func (a *Accordion) Render(state AccordionState) string {
	style := a.styles.Resting
	if state.Hovered {
		style = a.styles.Hover
	}
	return style.Render(a.glyph(state.Expanded))
}

// Width returns the cells the control occupies. It is state-independent, so a
// header keeps its layout when the control is toggled or hovered.
func (a *Accordion) Width() int {
	return a.styles.Resting.GetHorizontalFrameSize() +
		max(ansi.StringWidth(a.styles.Expanded), ansi.StringWidth(a.styles.Collapsed))
}

// Click toggles the control and notifies OnClick. It reports the new state so
// a caller that would rather read the result than register a handler can.
func (a *Accordion) Click(state AccordionState) AccordionState {
	next := state
	next.Expanded = !state.Expanded
	if a.OnClick != nil {
		a.OnClick(next)
	}
	return next
}

func (a *Accordion) glyph(expanded bool) string {
	if expanded {
		return a.styles.Expanded
	}
	return a.styles.Collapsed
}
