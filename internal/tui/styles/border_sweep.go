package styles

import (
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
)

// sweepBandWidth is the width, in cells, of the bright band that sweeps across
// the composer's top border while Bits is working. It is fixed rather than
// proportional to the composer width: 30 cells blends smoothly and still
// leaves most of the row at rest (dim) at any instant, at the composer's
// typical widths (60-160 cols).
const sweepBandWidth = 30

// sweepSpeedCellsPerFrame is how many cells the band advances per animation
// frame. 1 keeps the sweep readable as smooth motion at borderSweepInterval
// (see internal/tui/border_sweep.go) without feeling frantic.
const sweepSpeedCellsPerFrame = 1

// sweepGlyph is the top-border glyph the sweep recolors — inputRule.Top
// (builder.go), so the animated row is indistinguishable in shape from
// today's static rule; only the color animates.
var sweepGlyph = inputRule.Top

// easeInOutSine eases x (a linear progress fraction in [0, 1]) into a
// sinusoidal ease-in-out curve: gently slower at both ends, faster through
// the middle. Applying it to the sweep's ping-pong position (rather than to
// time directly) makes the band decelerate into each bounce and accelerate
// back out, instead of moving at constant speed.
func easeInOutSine(x float64) float64 {
	return -(math.Cos(math.Pi*x) - 1) / 2
}

// BorderSweepRow renders one frame of the animated composer top border for the
// given width in cells. dim is the resting color (the same gray as today's
// static rule); hot is the color the moving band peaks at; bg is the
// composer's background, so the row's cells match the box it sits above.
//
// frame is a monotonically increasing (or decreasing) tick counter; the band
// ping-pongs — sweeping close to (but not all the way to) off-screen left,
// then close to off-screen right, then back — rather than wrapping. Each end
// is inset by the same ~10% of the full off-screen-to-off-screen distance,
// so the reversal reads as a deliberate bounce on both sides rather than
// the band vanishing right at an edge. One full
// there-and-back cycle takes 2*travel frames (travel computed below), and any
// int (including negative) is valid input, matching Shimmer.Frame's
// contract.
//
// The row is computed fresh from width and frame on every call: there is no
// pre-rendered frame table, so it stays correct across a resize without
// needing to be rebuilt or invalidated separately from the rest of the view.
func BorderSweepRow(width, frame int, dim, hot, bg color.Color) string {
	if width <= 0 {
		return ""
	}
	fullTravel := width + sweepBandWidth
	inset := fullTravel / 10 // ~10% inset per end, ~20% off the full travel overall.
	travel := fullTravel - 2*inset
	cycle := 2 * travel
	t := ((frame*sweepSpeedCellsPerFrame)%cycle + cycle) % cycle
	if t >= travel {
		t = cycle - 1 - t
	}
	// Ease the linear ramp t (0..travel, then travel..0) through
	// easeInOutSine before turning it into a cell position, so the band
	// decelerates into each bounce and accelerates back out.
	eased := int(math.Round(easeInOutSine(float64(t)/float64(travel)) * float64(travel)))
	// pos is the band's leading (left) edge, normalized into
	// [-sweepBandWidth+inset, width-inset), inset in from each fully
	// off-screen extreme.
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
