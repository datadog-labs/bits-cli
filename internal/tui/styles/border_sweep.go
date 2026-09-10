package styles

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// sweepBandWidth is the width, in cells, of the bright band that sweeps across
// the composer's top border while Bits is working. It is fixed rather than
// proportional to the composer width: 20 cells blends smoothly and still
// leaves most of the row at rest (dim) at any instant, at the composer's
// typical widths (60-160 cols).
const sweepBandWidth = 20

// sweepSpeedCellsPerFrame is how many cells the band advances per animation
// frame. 1 keeps the sweep readable as smooth motion at borderSweepInterval
// (see internal/tui/border_sweep.go) without feeling frantic.
const sweepSpeedCellsPerFrame = 1

// sweepGlyph is the top-border glyph the sweep recolors — inputRule.Top
// (builder.go), so the animated row is indistinguishable in shape from
// today's static rule; only the color animates.
var sweepGlyph = inputRule.Top

// BorderSweepRow renders one frame of the animated composer top border for the
// given width in cells. dim is the resting color (the same gray as today's
// static rule); hot is the color the moving band peaks at; bg is the
// composer's background, so the row's cells match the box it sits above.
//
// frame is a monotonically increasing (or decreasing) tick counter; the row
// loops with period width+sweepBandWidth, and any int (including negative) is
// valid input, matching Shimmer.Frame's contract.
//
// The row is computed fresh from width and frame on every call: there is no
// pre-rendered frame table, so it stays correct across a resize without
// needing to be rebuilt or invalidated separately from the rest of the view.
func BorderSweepRow(width, frame int, dim, hot, bg color.Color) string {
	if width <= 0 {
		return ""
	}
	period := width + sweepBandWidth
	// pos is the band's leading (left) edge, normalized into [-sweepBandWidth, width).
	pos := ((frame*sweepSpeedCellsPerFrame)%period+period)%period - sweepBandWidth

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
