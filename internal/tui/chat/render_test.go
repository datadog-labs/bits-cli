package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// toolBlockOf builds a representative tool block in the given status.
func toolBlockOf(status agent.ToolStatus) agent.Block {
	return agent.Block{
		Kind: assistant.KindToolResult,
		Tool: &agent.ToolBlock{
			Name:   "search_logs",
			Input:  `{"query":"timeout"}`,
			Output: "ok: 4 results",
			Status: status,
		},
	}
}

// headerOf returns the first line of a rendered block, which carries the tool
// name and its status chip.
func headerOf(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func TestInFlightChipAnimatesAcrossFrames(t *testing.T) {
	sty := DefaultStyles(true)
	for _, test := range []struct {
		name   string
		status agent.ToolStatus
		anim   styles.Shimmer
	}{
		{"running", agent.ToolRunning, sty.StatusRunningLabel},
		{"awaiting approval", agent.ToolAwaitingApproval, sty.StatusAwaitingLabel},
	} {
		t.Run(test.name, func(t *testing.T) {
			block := toolBlockOf(test.status)
			seen := map[string]bool{}
			for frame := range test.anim.Len() {
				seen[headerOf(RenderBlock(block, 80, sty, frame))] = true
			}
			if len(seen) < 2 {
				t.Errorf("chip rendered %d distinct headers across a full sweep, want at least 2", len(seen))
			}
		})
	}
}

// TestInFlightChipKeepsItsWidthAndText is the anti-reflow guarantee: an
// animated chip must not change the header's width or garble its label, or the
// transcript line would jitter every frame.
func TestInFlightChipKeepsItsWidthAndText(t *testing.T) {
	sty := DefaultStyles(true)
	for _, test := range []struct {
		name   string
		status agent.ToolStatus
		label  string
		anim   styles.Shimmer
	}{
		{"running", agent.ToolRunning, "running", sty.StatusRunningLabel},
		{"awaiting approval", agent.ToolAwaitingApproval, "awaiting approval", sty.StatusAwaitingLabel},
	} {
		t.Run(test.name, func(t *testing.T) {
			block := toolBlockOf(test.status)
			want := ansi.StringWidth(headerOf(RenderBlock(block, 80, sty, 0)))
			for frame := range test.anim.Len() {
				header := headerOf(RenderBlock(block, 80, sty, frame))
				if got := ansi.StringWidth(header); got != want {
					t.Errorf("frame %d header width = %d, want %d", frame, got, want)
				}
				if plain := ansi.Strip(header); !strings.Contains(plain, test.label) {
					t.Errorf("frame %d header = %q, want it to contain %q", frame, plain, test.label)
				}
			}
		})
	}
}

// TestInFlightChipHasPillCaps covers the palette fix: the busy surface is what
// makes pill() draw its rounded caps instead of bare text.
func TestInFlightChipHasPillCaps(t *testing.T) {
	sty := DefaultStyles(true)
	header := headerOf(RenderBlock(toolBlockOf(agent.ToolRunning), 80, sty, 0))
	if !strings.Contains(header, pillCapLeft) || !strings.Contains(header, pillCapRight) {
		t.Error("in-flight chip should be drawn with pill caps")
	}
}

// TestSettledChipsIgnoreTheFrame keeps the animation confined to in-flight
// states; a finished tool must render identically forever so the list cache
// can keep serving it.
func TestSettledChipsIgnoreTheFrame(t *testing.T) {
	sty := DefaultStyles(true)
	for _, test := range []struct {
		name   string
		status agent.ToolStatus
	}{
		{"success", agent.ToolSuccess},
		{"error", agent.ToolError},
		{"unknown", agent.ToolUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			block := toolBlockOf(test.status)
			want := RenderBlock(block, 80, sty, 0)
			for _, frame := range []int{1, 17, 59, 1000} {
				if got := RenderBlock(block, 80, sty, frame); got != want {
					t.Errorf("frame %d changed a settled chip", frame)
				}
			}
		})
	}
}

// TestFlattenedThemeChipDoesNotAnimate is the degradation path end to end: on
// a terminal that cannot render a gradient the tui flattens the theme, and the
// chip must then be frame-independent without the renderer special-casing it.
func TestFlattenedThemeChipDoesNotAnimate(t *testing.T) {
	sty := StylesFor(styles.Default(true).WithoutMotion())
	block := toolBlockOf(agent.ToolRunning)
	want := RenderBlock(block, 80, sty, 0)
	for _, frame := range []int{1, 7, 42} {
		if got := RenderBlock(block, 80, sty, frame); got != want {
			t.Errorf("frame %d animated a flattened theme", frame)
		}
	}
	if plain := ansi.Strip(headerOf(want)); !strings.Contains(plain, "running") {
		t.Errorf("flattened chip = %q, want it to still read \"running\"", plain)
	}
}

// TestChipLabelsAreNotDuplicated guards the wiring: an animated label replaces
// the static word rather than rendering alongside it.
func TestChipLabelsAreNotDuplicated(t *testing.T) {
	sty := DefaultStyles(true)
	plain := ansi.Strip(headerOf(RenderBlock(toolBlockOf(agent.ToolRunning), 80, sty, 3)))
	if got := strings.Count(plain, "running"); got != 1 {
		t.Errorf("header = %q, contains %d occurrences of \"running\", want 1", plain, got)
	}
}

// TestStatusChipPrecedesToolName pins the header order: glyph, then chip, then
// the tool name.
func TestStatusChipPrecedesToolName(t *testing.T) {
	sty := DefaultStyles(true)
	for _, test := range []struct {
		name   string
		status agent.ToolStatus
		label  string
	}{
		{"running", agent.ToolRunning, "running"},
		{"awaiting approval", agent.ToolAwaitingApproval, "awaiting approval"},
		{"error", agent.ToolError, "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plain := ansi.Strip(headerOf(RenderBlock(toolBlockOf(test.status), 80, sty, 0)))
			label := strings.Index(plain, test.label)
			name := strings.Index(plain, "search_logs")
			if label < 0 || name < 0 {
				t.Fatalf("header = %q, want it to contain both %q and the tool name", plain, test.label)
			}
			if label > name {
				t.Errorf("header = %q, want the chip before the tool name", plain)
			}
		})
	}
}

// TestHeaderHasNoDotSeparator covers the removal of the separator that used to
// sit between the tool name and its chip.
func TestHeaderHasNoDotSeparator(t *testing.T) {
	sty := DefaultStyles(true)
	for _, status := range []agent.ToolStatus{
		agent.ToolRunning, agent.ToolAwaitingApproval, agent.ToolSuccess, agent.ToolError, agent.ToolUnknown,
	} {
		plain := ansi.Strip(headerOf(RenderBlock(toolBlockOf(status), 80, sty, 0)))
		if strings.Contains(plain, "·") {
			t.Errorf("header = %q, want no separator glyph", plain)
		}
	}
}

// TestSuccessRendersGlyphOnly is the simplification: a finished tool needs no
// chip, because the check mark already says it succeeded.
func TestSuccessRendersGlyphOnly(t *testing.T) {
	sty := DefaultStyles(true)
	header := headerOf(RenderBlock(toolBlockOf(agent.ToolSuccess), 80, sty, 0))
	plain := ansi.Strip(header)

	if !strings.Contains(plain, "✓") {
		t.Errorf("header = %q, want the success glyph", plain)
	}
	if strings.Contains(plain, "success") {
		t.Errorf("header = %q, want no \"success\" label", plain)
	}
	if strings.Contains(header, pillCapLeft) || strings.Contains(header, pillCapRight) {
		t.Errorf("header = %q, want no pill caps for a succeeded tool", plain)
	}
}

// TestErrorKeepsItsChip is the counterpart: a failure is worth the emphasis a
// chip gives it.
func TestErrorKeepsItsChip(t *testing.T) {
	sty := DefaultStyles(true)
	header := headerOf(RenderBlock(toolBlockOf(agent.ToolError), 80, sty, 0))
	if !strings.Contains(ansi.Strip(header), "error") {
		t.Errorf("header = %q, want the error label", ansi.Strip(header))
	}
	if !strings.Contains(header, pillCapLeft) {
		t.Error("an errored tool should keep its pill caps")
	}
}

func TestGenericExecResultShowsTruncationMetadataBeforeLargeStreams(t *testing.T) {
	block := agent.Block{
		Kind: assistant.KindToolResult,
		Tool: &agent.ToolBlock{
			Name:   "exec_command",
			Status: agent.ToolSuccess,
			Output: `{"status":"success","duration_ms":17,"truncated":true,"output_incomplete":true,"stdout_omitted_bytes":2048,"stderr_omitted_bytes":1024,"stdout":"` +
				strings.Repeat("command output ", 500) + `TAIL","stderr":""}`,
		},
	}
	rendered := ansi.Strip(RenderBlock(block, 60, DefaultStyles(true), 0))
	for _, want := range []string{`"duration_ms":17`, `"truncated":true`, `"output_incomplete":true`, `"stdout_omitted_bytes":2048`, `"stderr_omitted_bytes":1024`} {
		if !strings.Contains(rendered, want) {
			t.Errorf("generic exec rendering hid %s before its output clamp:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "TAIL") {
		t.Fatalf("test fixture did not exercise the generic output clamp:\n%s", rendered)
	}
}

// leadGlyphOf returns the first visible character of a rendered header, which
// is the status glyph.
func leadGlyphOf(header string) string {
	runes := []rune(ansi.Strip(header))
	if len(runes) == 0 {
		return ""
	}
	return string(runes[0])
}

// TestRunningGlyphAnimates is the change: while a tool runs, the leading glyph
// is a spinner rather than a static dot.
func TestRunningGlyphAnimates(t *testing.T) {
	sty := DefaultStyles(true)
	block := toolBlockOf(agent.ToolRunning)

	seen := map[string]bool{}
	for frame := range sty.StatusSpinner.Len() {
		seen[leadGlyphOf(headerOf(RenderBlock(block, 80, sty, frame)))] = true
	}
	if len(seen) < 2 {
		t.Errorf("leading glyph took %d distinct values across a full cycle, want at least 2", len(seen))
	}
	if seen["•"] {
		t.Error("a running tool should no longer show the static dot")
	}
}

// TestRunningGlyphUsesTheSpinnerFrames ties the rendered glyph to the theme's
// spinner rather than to whatever the renderer happens to pick.
func TestRunningGlyphUsesTheSpinnerFrames(t *testing.T) {
	sty := DefaultStyles(true)
	block := toolBlockOf(agent.ToolRunning)

	for _, frame := range []int{0, 3, 8, 17, 40} {
		got := leadGlyphOf(headerOf(RenderBlock(block, 80, sty, frame)))
		if want := sty.StatusSpinner.Frame(frame); got != want {
			t.Errorf("frame %d glyph = %q, want %q", frame, got, want)
		}
	}
}

// TestRunningGlyphAdvancesSlowerThanTheShimmer keeps the spinner readable: it
// shares the shimmer's tick, so without slowing it down a braille glyph at
// 20fps reads as a blur.
func TestRunningGlyphAdvancesSlowerThanTheShimmer(t *testing.T) {
	sty := DefaultStyles(true)
	block := toolBlockOf(agent.ToolRunning)
	glyph := func(frame int) string {
		return leadGlyphOf(headerOf(RenderBlock(block, 80, sty, frame)))
	}

	if glyph(0) != glyph(1) {
		t.Error("the spinner advanced on every tick; it should hold each glyph for more than one frame")
	}
	if glyph(0) == glyph(2) {
		t.Error("the spinner never advanced across two ticks")
	}
}

// TestAwaitingApprovalKeepsTheStaticDot confines the spinner to work actually
// in progress. An approval prompt is blocked on the user, so a spinner there
// would claim progress that is not happening.
func TestAwaitingApprovalKeepsTheStaticDot(t *testing.T) {
	sty := DefaultStyles(true)
	block := toolBlockOf(agent.ToolAwaitingApproval)

	for _, frame := range []int{0, 1, 5, 23} {
		if got := leadGlyphOf(headerOf(RenderBlock(block, 80, sty, frame))); got != "•" {
			t.Errorf("frame %d glyph = %q, want the static dot", frame, got)
		}
	}
}

// TestSettledGlyphsAreStatic guards the "only StatusRunning" boundary.
func TestSettledGlyphsAreStatic(t *testing.T) {
	sty := DefaultStyles(true)
	for _, test := range []struct {
		name   string
		status agent.ToolStatus
		glyph  string
	}{
		{"success", agent.ToolSuccess, "✓"},
		{"error", agent.ToolError, "✗"},
		{"unknown", agent.ToolUnknown, "•"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, frame := range []int{0, 2, 9, 31} {
				block := toolBlockOf(test.status)
				if got := leadGlyphOf(headerOf(RenderBlock(block, 80, sty, frame))); got != test.glyph {
					t.Errorf("frame %d glyph = %q, want %q", frame, got, test.glyph)
				}
			}
		})
	}
}

// TestFlattenedThemeFallsBackToTheStaticDot pins the degradation for the glyph:
// with motion disabled the spinner has no frames, so a running tool shows the
// same dot the awaiting state does rather than an empty gap.
func TestFlattenedThemeFallsBackToTheStaticDot(t *testing.T) {
	sty := StylesFor(styles.Default(true).WithoutMotion())
	for _, frame := range []int{0, 1, 5, 23} {
		got := leadGlyphOf(headerOf(RenderBlock(toolBlockOf(agent.ToolRunning), 80, sty, frame)))
		if got != "•" {
			t.Errorf("frame %d glyph = %q, want the static dot", frame, got)
		}
	}
}
