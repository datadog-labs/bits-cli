package styles

// brailleFrames is the progress spinner's glyph cycle. It matches the one the
// login screen draws while waiting for Datadog, so a running tool and a
// pending sign-in spin identically.
var brailleFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerStepsPerFrame is how many animation ticks each glyph is held for. The
// spinner shares the status chip's tick, which runs fast enough for a smooth
// gradient sweep; at that rate a braille glyph reads as a blur, so it is
// divided down to roughly the cadence the login screen spins at.
const spinnerStepsPerFrame = 2

// Spinner is a cycle of single-width glyphs advanced by an animation step. It
// carries no color: the caller styles the glyph, since a status glyph takes its
// color from the state it reports.
type Spinner struct {
	frames        []string
	stepsPerFrame int
}

// Frame returns the glyph for the given animation step. It wraps and tolerates
// negative steps, so callers can pass a raw tick counter — the same contract
// Shimmer.Frame offers.
func (s Spinner) Frame(step int) string {
	if len(s.frames) == 0 {
		return ""
	}
	held := s.stepsPerFrame
	if held < 1 {
		held = 1
	}
	i := (step / held) % len(s.frames)
	if i < 0 {
		i += len(s.frames)
	}
	return s.frames[i]
}

// withoutMotion returns a spinner with no frames, so callers fall back to their
// static glyph.
func (s Spinner) withoutMotion() Spinner { return Spinner{} }

// Len is the number of animation steps in one full cycle, counting the ticks
// each glyph is held for.
func (s Spinner) Len() int {
	if len(s.frames) == 0 {
		return 0
	}
	held := s.stepsPerFrame
	if held < 1 {
		held = 1
	}
	return len(s.frames) * held
}
