package catalog

import (
	"image/color"
	"math/rand/v2"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// This page records the status-animation alternatives. The selected row uses
// the production compact tool renderer; the remaining rows preserve earlier
// pill experiments for comparison.
//
// The leading glyph is a separate, later decision: the static dot became a
// braille spinner (option C's idea, applied to the glyph rather than the
// label). It is held constant across every row here so it cannot be mistaken
// for a feature of one candidate.
//
// Only the selected row goes through the real block renderer. The option rows
// are illustrations built locally and are deliberately not wired to anything —
// they exist to be compared, not shipped. Their geometry deliberately matches
// production (caps sit directly against the text, no padding), so the only
// thing that differs between rows is the motion itself. This file is part of
// the dev-only style-guide tool and is not linked into the CLI.
//
// Open question for review: whether a "[Design proposal]" page belongs in the
// catalog long-term. It is useful while the decision is being reviewed, but the
// catalog is otherwise a reference for what exists, not a record of what was
// considered. Deleting this file and its nav entry removes the page cleanly.
const (
	selectedMarker = "[SELECTED — building for production]"
	optionMarker   = "[option]"
)

// Rounded pill caps (powerline), the same glyphs the transcript uses. They
// need a Nerd/Powerline font; without one they show as missing-glyph boxes.
const (
	motionCapLeft  = ""
	motionCapRight = ""
)

// motionSteps is how many frames the illustrative option rows loop over. It
// matches the production shimmer's period so every row on the page restarts
// together.
const motionSteps = 60

func statusPillMotionSamples(width int, sty chat.Styles, frame int, title lipgloss.Style, tag string) []string {
	fg := sty.StatusRunning.GetForeground()
	bg := sty.StatusRunning.GetBackground()
	dim, hot := sweepEnds(sty)

	rows := []struct {
		marker string
		name   string
		note   string
		// chip is the pre-styled chip interior. Empty means the row is drawn by
		// the real block renderer instead.
		chip string
		// noCaps reproduces the pre-change look, where StatusRunning had no
		// background and pill() therefore drew no caps at all.
		noCaps bool
		// legacyGlyph leads the row with the static dot instead of the spinner,
		// for the row that records what shipped before this change.
		legacyGlyph bool
	}{
		{
			marker: selectedMarker,
			name:   "compact status",
			note:   "the lifecycle is a bare animated glyph; the action and its argument keep their semantic colors without a status pill",
		},
		{
			marker:      optionMarker,
			name:        "today",
			note:        "the flat label as it shipped before this change: a static dot, and no surface, so pill() drew no caps",
			chip:        lipgloss.NewStyle().Foreground(fg).Render("running"),
			noCaps:      true,
			legacyGlyph: true,
		},
		{
			marker: optionMarker,
			name:   "A · scramble suffix",
			note:   "crush's signature glyph noise beside the word; distinctive, but the noise reads as \"thinking\" rather than \"running\"",
			chip:   flat("running ", fg, bg) + scramble(3, frame, dim, hot, bg),
		},
		{
			marker: optionMarker,
			name:   "A+B · shimmer + suffix",
			note:   "both levers at once; the glyph noise competes with the sweep for attention",
			chip:   sty.StatusRunningLabel.Frame(frame) + flat(" ", fg, bg) + scramble(3, frame, dim, hot, bg),
		},
		{
			marker: optionMarker,
			name:   "A+B · scramble inside",
			note:   "noise flickering inside the word keeps the width fixed but makes the label hard to read",
			chip:   scrambleInside("running", frame, dim, hot, bg),
		},
		{
			marker: optionMarker,
			name:   "crush · faithful",
			note:   "crush's own arrangement: glyph block, label, then a cycling ellipsis",
			chip:   scramble(6, frame, dim, hot, bg) + flat(" running", fg, bg) + ellipsis(frame, fg, bg),
		},
		{
			marker: optionMarker,
			name:   "C · braille spinner",
			note:   "rejected as the label — a spinner says work is happening but not which tool state it is. Adopted for the leading glyph instead, which is why every row above spins: the two ideas turned out to be complementary rather than alternatives",
			chip:   spinner(frame, hot, bg) + flat(" running", fg, bg),
		},
	}

	out := make([]string, 0, len(rows))
	for _, r := range rows {
		header := firstLine(chat.RenderBlock(toolBlock(agent.ToolRunning), width, sty, frame))
		if r.chip != "" {
			chip := r.chip
			if !r.noCaps {
				chip = capped(chip, bg)
			}
			// The leading glyph is production's spinner, not part of what these
			// rows compare, so it is held constant across them — otherwise a
			// reader could mistake it for one candidate's feature.
			glyph := sty.StatusSpinner.Frame(frame)
			if r.legacyGlyph {
				glyph = "•"
			}
			// Same order as the block renderer — glyph, chip, then name — so the
			// option rows differ from the selected one only in their motion.
			header = sty.StatusRunning.UnsetBackground().Render(glyph+" ") +
				chip + " " + sty.ToolName.Render("search_logs")
		}
		out = append(out, renderSample(title, tag,
			r.marker+"  "+r.name,
			header+"\n"+sty.ToolDetail.Render("  "+r.note),
		))
	}
	return out
}

// sweepEnds returns the sweep's two ends from the theme, so the illustrative
// rows use the real palette instead of the catalog holding a second copy of it.
func sweepEnds(sty chat.Styles) (dim, hot color.Color) {
	return sty.StatusSweepDim, sty.StatusSweepHot
}

// flat renders text in one color on the chip surface.
func flat(s string, fg, bg color.Color) string {
	return lipgloss.NewStyle().Foreground(fg).Background(bg).Render(s)
}

// capped wraps chip content in rounded caps colored to its surface, the way
// pill() does in the block renderer.
func capped(content string, bg color.Color) string {
	if bg == nil {
		return content
	}
	if _, ok := bg.(lipgloss.NoColor); ok {
		return content
	}
	caps := lipgloss.NewStyle().Foreground(bg)
	return caps.Render(motionCapLeft) + content + caps.Render(motionCapRight)
}

// scrambleGlyphs is crush's own scramble alphabet.
var scrambleGlyphs = []rune("0123456789abcdefABCDEF~!@#$£€%^&*()+=_")

// scramble renders cells of random glyphs alternating between the sweep's two
// colors. The rune picker is seeded per frame so the row is deterministic.
func scramble(cells, frame int, dim, hot, bg color.Color) string {
	rng := rand.New(rand.NewPCG(uint64(frame%motionSteps), 0x5eed))
	var b strings.Builder
	for j := range cells {
		c := dim
		if (j+frame)%2 == 0 {
			c = hot
		}
		b.WriteString(flat(string(scrambleGlyphs[rng.IntN(len(scrambleGlyphs))]), c, bg))
	}
	return b.String()
}

// scrambleInside keeps the word's shape but lets two rotating positions inside
// it flicker to scramble glyphs.
func scrambleInside(text string, frame int, dim, hot, bg color.Color) string {
	runes := []rune(text)
	rng := rand.New(rand.NewPCG(uint64(frame%motionSteps), 0xb175))
	shift := frame % len(runes)
	var b strings.Builder
	for j, r := range runes {
		ch, c := r, dim
		if shift%len(runes) == j || (shift+1)%len(runes) == j {
			ch, c = scrambleGlyphs[rng.IntN(len(scrambleGlyphs))], hot
		}
		b.WriteString(flat(string(ch), c, bg))
	}
	return b.String()
}

// ellipsis cycles "", ".", "..", "..." padded to a constant three cells. crush
// lets its ellipsis change width; inside a pill that would jitter the right cap
// every few frames, so the field is padded instead.
func ellipsis(frame int, fg, bg color.Color) string {
	phases := []string{"   ", ".  ", ".. ", "..."}
	return flat(phases[(frame/8)%len(phases)], fg, bg)
}

// spinner is the fixed-width braille spinner the login screen uses.
func spinner(frame int, fg, bg color.Color) string {
	phases := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	return flat(string(phases[(frame/3)%len(phases)]), fg, bg)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
