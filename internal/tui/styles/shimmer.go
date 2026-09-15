package styles

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/lucasb-eyer/go-colorful"
)

// shimmerSteps is how many frames one full sweep is pre-rendered to. It sets
// the loop period: at the tui's animation interval, 60 steps is a sweep of a
// few seconds, smooth enough to read as motion without making a theme rebuild
// expensive.
const shimmerSteps = 60

// Shimmer is a pre-rendered animated label: a low-to-high emphasis band swept
// across the text, one entry per animation step. Every frame is already
// wrapped in its lipgloss colors, so drawing one is a slice index rather than
// a styling pass — the approach charmbracelet/crush's anim package takes, for
// the same reason.
//
// Two properties make a Shimmer safe inside a fixed-width chip: every frame
// has the same display width, and every frame strips to the same text. The
// sweep only ever recolors characters.
type Shimmer struct {
	// frames is one full sweep, or nil for a Shimmer that does not animate.
	frames []string
	// static is the flat rendering used when there is no motion.
	static string
}

// Frame returns the label at the given animation step. It wraps, so callers
// can pass a monotonically increasing (or briefly negative) tick counter
// without doing modular arithmetic. A Shimmer without motion returns its
// static rendering at every step, which is what lets the block renderer stay
// free of animation special cases.
func (s Shimmer) Frame(step int) string {
	if len(s.frames) == 0 {
		return s.static
	}
	i := step % len(s.frames)
	if i < 0 {
		i += len(s.frames)
	}
	return s.frames[i]
}

// Static returns the flat, unanimated rendering of the label.
func (s Shimmer) Static() string { return s.static }

// Len is the number of steps in one full sweep, or 0 when there is no motion.
func (s Shimmer) Len() int { return len(s.frames) }

// withoutMotion returns the same label with its sweep dropped.
func (s Shimmer) withoutMotion() Shimmer { return Shimmer{static: s.static} }

// shimmerPalette holds the colors every animated chip in one theme shares: the
// flat label color, the chip surface, and the two ends of the sweep.
type shimmerPalette struct {
	fg, bg   color.Color
	dim, hot color.Color
}

// shimmer pre-renders one animated label.
func (sp shimmerPalette) shimmer(text string) Shimmer {
	flat := lipgloss.NewStyle().Foreground(sp.fg).Background(sp.bg).Render(text)

	runes := []rune(text)
	if len(runes) == 0 {
		return Shimmer{static: flat}
	}

	// dim → hot → dim across twice the label's width, so roughly half the word
	// glows at a time with a trough between passes.
	ramp := gradientRamp(len(runes)*2, sp.dim, sp.hot, sp.dim)
	if len(ramp) == 0 {
		return Shimmer{static: flat}
	}

	frames := make([]string, shimmerSteps)
	for f := range frames {
		shift := f * len(ramp) / shimmerSteps
		var b strings.Builder
		for j, r := range runes {
			c := ramp[((j-shift)%len(ramp)+len(ramp))%len(ramp)]
			b.WriteString(lipgloss.NewStyle().Foreground(c).Background(sp.bg).Render(string(r)))
		}
		frames[f] = b.String()
	}
	return Shimmer{frames: frames, static: flat}
}

// WithoutMotion returns a frame-independent theme for callers that explicitly
// request reduced motion. Compact tool activity itself does not depend on color
// gradients, so terminal color depth does not invoke this policy automatically.
func (t Theme) WithoutMotion() Theme {
	t.Input.SweepMotion = false
	t.Chat.StatusRunningLabel = t.Chat.StatusRunningLabel.withoutMotion()
	t.Chat.StatusAwaitingLabel = t.Chat.StatusAwaitingLabel.withoutMotion()
	t.Chat.StatusSpinner = t.Chat.StatusSpinner.withoutMotion()
	return t
}

// gradientRamp blends size colors across the given stops. Blending is done in
// Hcl space so the intermediate colors stay in gamut. Lifted from crush's
// anim.makeGradientRamp.
func gradientRamp(size int, stops ...color.Color) []color.Color {
	if size < 1 || len(stops) < 2 {
		return nil
	}
	points := make([]colorful.Color, len(stops))
	for i, s := range stops {
		points[i], _ = colorful.MakeColor(s)
	}
	segments := len(stops) - 1
	out := make([]color.Color, 0, size)
	base, rem := size/segments, size%segments
	for i := range segments {
		n := base
		if i < rem {
			n++
		}
		for j := range n {
			out = append(out, points[i].BlendHcl(points[i+1], float64(j)/float64(max(n, 1))))
		}
	}
	return out
}
