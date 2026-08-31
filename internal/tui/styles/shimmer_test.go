package styles

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// busyShimmer returns the shimmer palette a theme builds its animated status
// chips from, for tests that do not care which mode they run in.
func busyShimmer(isDark bool) shimmerPalette {
	p := lightPalette()
	if isDark {
		p = darkPalette()
	}
	return shimmerPalette{
		fg:  lipgloss.Color(p.busy),
		bg:  lipgloss.Color(p.busySurface),
		dim: lipgloss.Color(p.busyDim),
		hot: lipgloss.Color(p.busyHot),
	}
}

// TestShimmerFrameWidthIsConstant guards the property that makes an animated
// label safe inside a pill: if frames differ in display width, the right cap
// moves every frame and the whole header line reflows.
func TestShimmerFrameWidthIsConstant(t *testing.T) {
	for _, test := range []struct {
		name   string
		isDark bool
	}{{"dark", true}, {"light", false}} {
		t.Run(test.name, func(t *testing.T) {
			for _, text := range []string{"running", "awaiting approval", "x"} {
				s := busyShimmer(test.isDark).shimmer(text)
				want := ansi.StringWidth(s.Static())
				for step := range s.Len() {
					if got := ansi.StringWidth(s.Frame(step)); got != want {
						t.Errorf("%q frame %d width = %d, want %d", text, step, got, want)
					}
				}
			}
		})
	}
}

// TestShimmerNeverGarblesTheLabel is what makes the shimmer readable, and the
// reason it was chosen over the scrambled-glyph options: the sweep only
// recolors characters, so the word itself is always intact.
func TestShimmerNeverGarblesTheLabel(t *testing.T) {
	const text = "running"
	s := busyShimmer(true).shimmer(text)
	for step := range s.Len() {
		if got := ansi.Strip(s.Frame(step)); got != text {
			t.Errorf("frame %d text = %q, want %q", step, got, text)
		}
	}
	if got := ansi.Strip(s.Static()); got != text {
		t.Errorf("static text = %q, want %q", got, text)
	}
}

// TestShimmerAnimates catches a sweep whose offset always lands on the same
// ramp index, which would render as a static label.
func TestShimmerAnimates(t *testing.T) {
	s := busyShimmer(true).shimmer("running")
	distinct := map[string]bool{}
	for step := range s.Len() {
		distinct[s.Frame(step)] = true
	}
	if len(distinct) < 2 {
		t.Errorf("shimmer produced %d distinct frames across %d steps, want at least 2", len(distinct), s.Len())
	}
}

// TestShimmerIsDeterministic keeps two builds of the same theme
// byte-identical, so a theme rebuild cannot make the animation jump.
func TestShimmerIsDeterministic(t *testing.T) {
	a := busyShimmer(true).shimmer("running")
	b := busyShimmer(true).shimmer("running")
	if a.Len() != b.Len() {
		t.Fatalf("frame counts differ: %d vs %d", a.Len(), b.Len())
	}
	for step := range a.Len() {
		if a.Frame(step) != b.Frame(step) {
			t.Fatalf("frame %d differs between builds", step)
		}
	}
}

// TestShimmerFrameWraps lets callers pass a monotonically increasing tick
// counter without doing modular arithmetic themselves.
func TestShimmerFrameWraps(t *testing.T) {
	s := busyShimmer(true).shimmer("running")
	if s.Frame(s.Len()) != s.Frame(0) {
		t.Error("Frame(Len()) should wrap to Frame(0)")
	}
	if s.Frame(s.Len()*3+2) != s.Frame(2) {
		t.Error("Frame should wrap on every multiple of Len()")
	}
}

// TestShimmerFrameHandlesNegativeStep guards against a caller passing a
// decremented counter; it must not panic or return empty.
func TestShimmerFrameHandlesNegativeStep(t *testing.T) {
	s := busyShimmer(true).shimmer("running")
	if got := ansi.Strip(s.Frame(-1)); got != "running" {
		t.Errorf("Frame(-1) = %q, want the label", got)
	}
}

// TestShimmerStaticIsTheFlatLabel pins the fallback rendering: the label in
// the chip's flat foreground on its surface, which is what a terminal that
// cannot animate should show.
func TestShimmerStaticIsTheFlatLabel(t *testing.T) {
	sp := busyShimmer(true)
	s := sp.shimmer("running")
	want := lipgloss.NewStyle().Foreground(sp.fg).Background(sp.bg).Render("running")
	if got := s.Static(); got != want {
		t.Errorf("Static() = %q, want %q", got, want)
	}
}

// TestShimmerWithoutMotionIsStaticAtEveryStep is the graceful degradation.
// lipgloss renders truecolor regardless of the terminal, so a low-color
// terminal is detected in the tui (via tea.ColorProfileMsg) and the theme is
// flattened here; the renderer then needs no special case at all.
func TestShimmerWithoutMotionIsStaticAtEveryStep(t *testing.T) {
	s := busyShimmer(true).shimmer("running").withoutMotion()
	for _, step := range []int{0, 1, 7, 59, 60, 1000, -3} {
		if got, want := s.Frame(step), s.Static(); got != want {
			t.Fatalf("frame %d = %q, want the static label %q", step, got, want)
		}
	}
}

// TestThemeWithoutMotionFlattensEveryAnimatedChip keeps a newly added
// animated label from being missed by the degradation path.
func TestThemeWithoutMotionFlattensEveryAnimatedChip(t *testing.T) {
	flat := Default(true).WithoutMotion()
	for name, s := range map[string]Shimmer{
		"running":           flat.Chat.StatusRunningLabel,
		"awaiting approval": flat.Chat.StatusAwaitingLabel,
	} {
		if s.Frame(3) != s.Static() {
			t.Errorf("%s chip still animates after WithoutMotion()", name)
		}
	}
}

// TestShimmerEmptyLabel keeps a missing label from panicking or producing
// stray frames.
func TestShimmerEmptyLabel(t *testing.T) {
	s := busyShimmer(true).shimmer("")
	if got := ansi.Strip(s.Frame(0)); got != "" {
		t.Errorf("Frame(0) = %q, want empty", got)
	}
	if got := ansi.Strip(s.Static()); got != "" {
		t.Errorf("Static() = %q, want empty", got)
	}
}

// TestGradientRampSpansItsStops checks the ramp helper lifted from crush: it
// must hit the requested size and actually travel between the stops. Hcl
// blending round-trips through float conversions, so the endpoints are
// compared with a tolerance rather than exactly.
func TestGradientRampSpansItsStops(t *testing.T) {
	a := lipgloss.Color("#8A6A1F")
	b := lipgloss.Color("#FFE9A8")
	ramp := gradientRamp(10, a, b)
	if got := len(ramp); got != 10 {
		t.Fatalf("ramp size = %d, want 10", got)
	}
	if !nearColor(ramp[0], a) {
		t.Errorf("ramp[0] = %s, want approximately the first stop %s", hex(ramp[0]), hex(a))
	}
	if nearColor(ramp[0], ramp[len(ramp)-1]) {
		t.Error("ramp should travel between its stops, not collapse")
	}
}

// TestGradientRampRejectsDegenerateInput keeps the helper from panicking on
// sizes or stop counts it cannot satisfy.
func TestGradientRampRejectsDegenerateInput(t *testing.T) {
	if got := gradientRamp(0, lipgloss.Color("#000000"), lipgloss.Color("#FFFFFF")); got != nil {
		t.Error("size 0 should yield no ramp")
	}
	if got := gradientRamp(5, lipgloss.Color("#8A6A1F")); got != nil {
		t.Error("a single stop should yield no ramp")
	}
}

// nearColor reports whether two colors are within a small per-channel
// tolerance of each other.
func nearColor(a, b color.Color) bool {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	const tol = 0x0200 // ~2/255 per channel
	within := func(x, y uint32) bool {
		if x > y {
			return x-y <= tol
		}
		return y-x <= tol
	}
	return within(ar, br) && within(ag, bg) && within(ab, bb)
}

func hex(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return string([]byte{'#'}) + hexByte(uint8(r>>8)) + hexByte(uint8(g>>8)) + hexByte(uint8(b>>8))
}

func hexByte(b uint8) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{digits[b>>4], digits[b&0x0f]})
}

// TestThemeExposesSweepEnds lets the style catalog illustrate the sweep with
// the real palette instead of keeping a second copy of these colors.
func TestThemeExposesSweepEnds(t *testing.T) {
	for _, test := range []struct {
		name    string
		isDark  bool
		palette palette
	}{
		{"dark", true, darkPalette()},
		{"light", false, lightPalette()},
	} {
		t.Run(test.name, func(t *testing.T) {
			chat := Default(test.isDark).Chat
			if got, want := chat.StatusSweepDim, lipgloss.Color(test.palette.busyDim); got != want {
				t.Errorf("sweep dim = %v, want token %v", got, want)
			}
			if got, want := chat.StatusSweepHot, lipgloss.Color(test.palette.busyHot); got != want {
				t.Errorf("sweep hot = %v, want token %v", got, want)
			}
			if sameColor(chat.StatusSweepDim, chat.StatusSweepHot) {
				t.Error("the sweep's two ends must differ, or there is no gradient to animate")
			}
		})
	}
}

// sameColor reports whether two colors are bit-identical.
func sameColor(a, b color.Color) bool {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	return ar == br && ag == bg && ab == bb
}

// TestStatusSpinnerFramesAreSingleWidth keeps the spinner from shifting the
// rest of the header line as it cycles.
func TestStatusSpinnerFramesAreSingleWidth(t *testing.T) {
	s := Default(true).Chat.StatusSpinner
	if s.Len() == 0 {
		t.Fatal("the theme has no status spinner")
	}
	for step := range s.Len() {
		if got := ansi.StringWidth(s.Frame(step)); got != 1 {
			t.Errorf("frame %d width = %d, want 1", step, got)
		}
	}
}

// TestSpinnerFrameWraps lets callers pass a monotonically increasing tick
// counter, as they already do for Shimmer.
func TestSpinnerFrameWraps(t *testing.T) {
	s := Default(true).Chat.StatusSpinner
	if s.Frame(s.Len()) != s.Frame(0) {
		t.Error("Frame(Len()) should wrap to Frame(0)")
	}
	if s.Frame(-1) == "" {
		t.Error("a negative step should still yield a glyph")
	}
}

// TestSpinnerHoldsEachGlyph checks the rate divisor: the spinner shares the
// shimmer's tick but must advance more slowly to stay legible.
func TestSpinnerHoldsEachGlyph(t *testing.T) {
	s := Default(true).Chat.StatusSpinner
	if s.Frame(0) != s.Frame(1) {
		t.Error("each glyph should be held for more than one tick")
	}
	if s.Frame(0) == s.Frame(2) {
		t.Error("the spinner should advance within two ticks")
	}
}
