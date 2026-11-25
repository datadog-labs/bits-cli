package styles

import (
	"image/color"
	"math"

	"charm.land/lipgloss/v2"
	"github.com/lucasb-eyer/go-colorful"
)

// pendingGlyph marks a queued prompt: a pause sign, since the prompt waits its
// turn rather than being worked on.
const pendingGlyph = "⏸"

// pulseSteps is one breath at the activity tick, about 2.4 seconds.
const pulseSteps = 48

// pendingRest is how much of the hot color the pulse keeps at its dimmest, so
// the glyph never disappears into the surface.
const pendingRest = 0.3

// pulse breathes the whole label's color from rest up to hot and back,
// lingering at both ends, rather than sweeping a band across it.
func pulse(text string, hot, bg color.Color) Shimmer {
	render := func(c color.Color) string {
		return lipgloss.NewStyle().Bold(true).Foreground(c).Background(bg).Render(text)
	}
	peak, _ := colorful.MakeColor(hot)
	surface, _ := colorful.MakeColor(bg)
	rest := surface.BlendRgb(peak, pendingRest)
	frames := make([]string, pulseSteps)
	for i := range frames {
		triangle := 1 - math.Abs(2*float64(i)/pulseSteps-1)
		frames[i] = render(rest.BlendHcl(peak, easeInOutSine(triangle)).Clamped())
	}
	return Shimmer{frames: frames, static: render(hot)}
}
