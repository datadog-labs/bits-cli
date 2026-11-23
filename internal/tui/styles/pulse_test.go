package styles

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestPendingPulseOnlyRecolors keeps the queued prompt's marker exactly as
// wide as the composer prompt in every frame, so its text never shifts.
func TestPendingPulseOnlyRecolors(t *testing.T) {
	for _, dark := range []bool{true, false} {
		in := Default(dark).Input
		p := in.Pending
		if p.Len() == 0 {
			t.Fatal("the theme's pending marker does not pulse")
		}
		for step := range p.Len() {
			frame := p.Frame(step)
			if got := ansi.Strip(frame); got != pendingGlyph+" " {
				t.Fatalf("frame %d text = %q", step, got)
			}
			if got := ansi.StringWidth(frame); got != ansi.StringWidth(prompt) {
				t.Fatalf("frame %d width = %d, want the prompt's %d", step, got, ansi.StringWidth(prompt))
			}
		}
		if p.Frame(0) == p.Frame(p.Len()/2) {
			t.Error("rest and peak frames share a color")
		}
		if p.Frame(p.Len()/2) != p.Static() {
			t.Error("the pulse should peak at the static marker's color")
		}
		if p.Frame(p.Len()) != p.Frame(0) || p.Frame(-1) != p.Frame(p.Len()-1) {
			t.Error("Frame should wrap in both directions")
		}
	}
}

func TestPendingPulseWithoutMotionIsStatic(t *testing.T) {
	p := Default(true).WithoutMotion().Input.Pending
	if p.Len() != 0 || p.Frame(0) != p.Frame(7) || p.Frame(0) != p.Static() {
		t.Error("reduced motion kept the pending pulse")
	}
}
