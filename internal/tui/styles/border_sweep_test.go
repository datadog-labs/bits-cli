package styles

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func sweepColors() (dim, hot, bg color.Color) {
	return lipgloss.Color("#383A40"), lipgloss.Color("#A2C6FF"), lipgloss.Color("#22252F")
}

func TestBorderSweepRowWidthMatchesInput(t *testing.T) {
	dim, hot, bg := sweepColors()
	for _, width := range []int{1, 9, 10, 11, 40, 80, 160} {
		row := BorderSweepRow(width, 0, dim, hot, bg)
		if got := ansi.StringWidth(row); got != width {
			t.Errorf("width %d: row width = %d, want %d", width, got, width)
		}
	}
}

func TestBorderSweepRowZeroWidthIsEmpty(t *testing.T) {
	dim, hot, bg := sweepColors()
	if got := BorderSweepRow(0, 0, dim, hot, bg); got != "" {
		t.Errorf("BorderSweepRow(0, ...) = %q, want empty", got)
	}
	if got := BorderSweepRow(-1, 0, dim, hot, bg); got != "" {
		t.Errorf("BorderSweepRow(-1, ...) = %q, want empty", got)
	}
}

func TestBorderSweepRowAnimates(t *testing.T) {
	dim, hot, bg := sweepColors()
	const width = 80
	distinct := map[string]bool{}
	for frame := range width + sweepBandWidth {
		distinct[BorderSweepRow(width, frame, dim, hot, bg)] = true
	}
	if len(distinct) < 2 {
		t.Errorf("border sweep produced %d distinct frames, want at least 2", len(distinct))
	}
}

func TestBorderSweepRowLoops(t *testing.T) {
	dim, hot, bg := sweepColors()
	const width = 40
	fullTravel := width + sweepBandWidth
	inset := fullTravel / 10
	cycle := 2 * (fullTravel - 2*inset)
	first := BorderSweepRow(width, 0, dim, hot, bg)
	if got := BorderSweepRow(width, cycle, dim, hot, bg); got != first {
		t.Error("frame cycle did not return to the starting row")
	}
}

func TestBorderSweepRowHandlesNegativeFrame(t *testing.T) {
	dim, hot, bg := sweepColors()
	const width = 40
	if got := ansi.StringWidth(BorderSweepRow(width, -1, dim, hot, bg)); got != width {
		t.Errorf("BorderSweepRow with a negative frame has width %d, want %d", got, width)
	}
}

func TestBorderSweepRowRestsAtDim(t *testing.T) {
	dim, hot, bg := sweepColors()
	const width = 40
	// At frame 0 the band leads at -sweepBandWidth+inset, so only the
	// leading `inset` cells carry any band color.
	fullTravel := width + sweepBandWidth
	inset := fullTravel / 10
	row := BorderSweepRow(width, 0, dim, hot, bg)
	dimRun := lipgloss.NewStyle().Foreground(dim).Background(bg).Render(strings.Repeat(sweepGlyph, width-inset))
	wantSuffix := dimRun
	if !strings.HasSuffix(row, wantSuffix) {
		t.Error("frame 0 (band inset from off-screen) should render dim past the inset")
	}
}

func TestThemeExposesSweepColors(t *testing.T) {
	for _, test := range []struct {
		name    string
		isDark  bool
		palette palette
	}{
		{"dark", true, darkPalette()},
		{"light", false, lightPalette()},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := Default(test.isDark).Input
			if !in.SweepMotion {
				t.Error("SweepMotion = false, want true in the default theme")
			}
			if got, want := in.SweepDim, lipgloss.Color(test.palette.inputRule); got != want {
				t.Errorf("SweepDim = %v, want %v", got, want)
			}
			if got, want := in.SweepHot, lipgloss.Color(test.palette.sweepHot); got != want {
				t.Errorf("SweepHot = %v, want %v", got, want)
			}
		})
	}
}

func TestThemeWithoutMotionDisablesBorderSweep(t *testing.T) {
	if Default(true).WithoutMotion().Input.SweepMotion {
		t.Error("WithoutMotion left the input border sweep enabled")
	}
}
