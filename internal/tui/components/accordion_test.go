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
		{Hovered: true},
		{Expanded: true, Hovered: true},
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

// The hover fill is deliberately the same recipe as a selected approval action
// rather than its own palette role, so a future theme change cannot drift the
// two pointer-selected surfaces apart.
func TestAccordionHoverReusesSelectedFill(t *testing.T) {
	for _, dark := range []bool{true, false} {
		theme := styles.Default(dark)
		hover, _ := theme.Accordion.Hover.GetBackground().(interface{ RGBA() (uint32, uint32, uint32, uint32) })
		selected, _ := theme.Approval.Selected.GetBackground().(interface{ RGBA() (uint32, uint32, uint32, uint32) })
		if hover == nil || selected == nil {
			t.Fatalf("dark=%t: hover or selected background is unset", dark)
		}
		hr, hg, hb, _ := hover.RGBA()
		sr, sg, sb, _ := selected.RGBA()
		if hr != sr || hg != sg || hb != sb {
			t.Fatalf("dark=%t: hover fill %v does not match Approval.Selected %v", dark, []uint32{hr, hg, hb}, []uint32{sr, sg, sb})
		}
	}
}

func TestAccordionHoverIsVisiblyDistinctFromResting(t *testing.T) {
	accordion := NewAccordion(styles.Default(true).Accordion)
	resting := accordion.Render(AccordionState{Expanded: true})
	hovered := accordion.Render(AccordionState{Expanded: true, Hovered: true})
	if resting == hovered {
		t.Fatal("hovered control renders identically to resting")
	}
}

func TestAccordionClickTogglesAndNotifies(t *testing.T) {
	accordion := NewAccordion(styles.Default(true).Accordion)
	var got []AccordionState
	accordion.OnClick = func(next AccordionState) { got = append(got, next) }

	state := accordion.Click(AccordionState{Expanded: true, Hovered: true})
	if state.Expanded {
		t.Fatal("click on expanded control did not collapse it")
	}
	if !state.Hovered {
		t.Fatal("click cleared the hover state")
	}
	if state = accordion.Click(state); !state.Expanded {
		t.Fatal("second click did not re-expand")
	}
	if len(got) != 2 || got[0].Expanded || !got[1].Expanded {
		t.Fatalf("OnClick received %+v, want collapse then expand", got)
	}
}

func TestAccordionClickWithoutHandlerStillToggles(t *testing.T) {
	accordion := NewAccordion(styles.Default(true).Accordion)
	if !accordion.Click(AccordionState{}).Expanded {
		t.Fatal("click without OnClick did not toggle")
	}
}
