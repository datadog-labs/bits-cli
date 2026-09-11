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

// BorderSweepRow renders one frame of the animated composer top border for the
// given width in cells. dim is the resting color, hot is the band's peak
// color, and bg is the composer's background.
//
// frame is a monotonically increasing (or decreasing) tick counter; the band
// ping-pongs between the two edges, inset ~10% short of fully off-screen on
// each side so the bounce reads as deliberate. It is computed fresh from
// width and frame on every call, so it stays correct across a resize.
func BorderSweepRow(width, frame int, dim, hot, bg color.Color) string {
	if width <= 0 {
		return ""
	}
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

	ramp := gradientRamp(sweepBandWidth, dim, hot, dim)

	var b strings.Builder
	for i := range width {
		c := dim
		if offset := i - pos; offset >= 0 && offset < sweepBandWidth && len(ramp) > 0 {
			c = ramp[offset]
		}
		b.WriteString(lipgloss.NewStyle().Foreground(c).Background(bg).Render(sweepGlyph))
	}
	return b.String()
}
