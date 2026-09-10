package styles

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func sweepColors() (dim, hot, bg color.Color) {
	return lipgloss.Color("#383A40"), lipgloss.Color("#5e6dd6"), lipgloss.Color("#22252F")
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
	for frame := 0; frame < width+sweepBandWidth; frame++ {
		distinct[BorderSweepRow(width, frame, dim, hot, bg)] = true
	}
	if len(distinct) < 2 {
		t.Errorf("border sweep produced %d distinct frames, want at least 2", len(distinct))
	}
}

func TestBorderSweepRowLoops(t *testing.T) {
	dim, hot, bg := sweepColors()
	const width = 40
	period := width + sweepBandWidth
	first := BorderSweepRow(width, 0, dim, hot, bg)
	if got := BorderSweepRow(width, period, dim, hot, bg); got != first {
		t.Error("frame period did not return to the starting row")
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
	// At frame 0 the band leads at -sweepBandWidth, i.e. fully off-screen to
	// the left, so every cell should be dim.
	row := BorderSweepRow(width, 0, dim, hot, bg)
	cell := lipgloss.NewStyle().Foreground(dim).Background(bg).Render(sweepGlyph)
	wantRow := ""
	for i := 0; i < width; i++ {
		wantRow += cell
	}
	if row != wantRow {
		t.Error("frame 0 (band fully off-screen) should render every cell dim")
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
			if got, want := in.SweepDim, lipgloss.Color(test.palette.inputRule); got != want {
				t.Errorf("SweepDim = %v, want %v", got, want)
			}
			if got, want := in.SweepHot, lipgloss.Color(test.palette.primary); got != want {
				t.Errorf("SweepHot = %v, want %v", got, want)
			}
		})
	}
}
