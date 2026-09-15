package styles

import (
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
)

// sweepBandWidth is the width, in cells, of the bright band that sweeps across
// the composer's top border while Bits is working.
const sweepBandWidth = 30

// sweepSpeedCellsPerFrame is how many cells the band advances per animation
// frame.
const sweepSpeedCellsPerFrame = 1

// sweepGlyph matches inputRule.Top (builder.go) so the animated row is
// indistinguishable in shape from the static rule; only the color animates.
var sweepGlyph = inputRule.Top

// easeInOutSine eases a linear [0, 1] progress fraction into a sinusoidal
// ease-in-out curve, so the sweep decelerates into each bounce.
func easeInOutSine(x float64) float64 {
	return -(math.Cos(math.Pi*x) - 1) / 2
}

// BorderSweep is a prepared composer-border animation. Preparing it once when
// the width or theme changes keeps color interpolation and lipgloss rendering
// out of the per-frame path.
type BorderSweep struct {
	width     int
	dimStyle  lipgloss.Style
	rampCells []string
}

// NewBorderSweep prepares a composer-border sweep for repeated rendering.
func NewBorderSweep(width int, dim, hot, bg color.Color) BorderSweep {
	if width <= 0 {
		return BorderSweep{}
	}
	ramp := gradientRamp(sweepBandWidth, dim, hot, dim)
	rampCells := make([]string, len(ramp))
	for i, c := range ramp {
		rampCells[i] = lipgloss.NewStyle().Foreground(c).Background(bg).Render(sweepGlyph)
	}
	return BorderSweep{
		width:     width,
		dimStyle:  lipgloss.NewStyle().Foreground(dim).Background(bg),
		rampCells: rampCells,
	}
}

// Row renders one frame. frame is a monotonically increasing (or decreasing)
// tick counter; the band ping-pongs between the edges, inset ~10% short of
// fully off-screen on each side so the bounce reads as deliberate.
func (s BorderSweep) Row(frame int) string {
	if s.width <= 0 {
		return ""
	}
	width := s.width
	fullTravel := width + sweepBandWidth
	inset := fullTravel / 10
	travel := fullTravel - 2*inset
	cycle := 2 * travel
	t := ((frame*sweepSpeedCellsPerFrame)%cycle + cycle) % cycle
	if t >= travel {
		t = cycle - 1 - t
	}
	eased := int(math.Round(easeInOutSine(float64(t)/float64(travel)) * float64(travel)))
	pos := eased - sweepBandWidth + inset

	bandStart := min(width, max(0, pos))
	bandEnd := min(width, max(0, pos+len(s.rampCells)))

	var b strings.Builder
	b.Grow(width * len(sweepGlyph))
	if bandStart > 0 {
		b.WriteString(s.dimStyle.Render(strings.Repeat(sweepGlyph, bandStart)))
	}
	for offset := bandStart - pos; offset < bandEnd-pos; offset++ {
		b.WriteString(s.rampCells[offset])
	}
	if bandEnd < width {
		b.WriteString(s.dimStyle.Render(strings.Repeat(sweepGlyph, width-bandEnd)))
	}
	return b.String()
}

// BorderSweepRow renders one frame of an animated composer top border. Callers
// drawing multiple frames should retain a BorderSweep instead, so preparation
// is paid only when width or colors change.
func BorderSweepRow(width, frame int, dim, hot, bg color.Color) string {
	return NewBorderSweep(width, dim, hot, bg).Row(frame)
}
