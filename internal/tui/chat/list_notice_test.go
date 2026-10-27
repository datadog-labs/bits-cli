package chat

import (
	"errors"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func TestLocalNoticesStayBetweenAgentBlocks(t *testing.T) {
	l := NewList()
	l.SetWidth(32)
	l.SetHeight(10)
	first := textBlock("a", "FIRST BLOCK")
	second := textBlock("b", "SECOND BLOCK")
	n := NoticeItem{ID: 1, After: first.ID, Notice: Notice{
		Level: NoticeWarn, Text: "A warning that wraps across more than one line", Err: errors.New("private backend detail"),
	}}
	next := NoticeItem{ID: 2, After: first.ID, Notice: Notice{Level: NoticeInfo, Text: "NEXT NOTICE"}}
	l.SetTranscript([]agent.Block{first}, nil, []NoticeItem{n, next})
	// A block shown before their anchor does not move the notices.
	l.SetTranscript([]agent.Block{textBlock("z", "EARLIER BLOCK"), first, second}, nil, []NoticeItem{n, next})
	doc := ansi.Strip(l.Document())
	if z, a, b, d, c := strings.Index(doc, "EARLIER BLOCK"), strings.Index(doc, "FIRST BLOCK"), strings.Index(doc, "A warning"), strings.Index(doc, "NEXT NOTICE"), strings.Index(doc, "SECOND BLOCK"); z < 0 || a <= z || b <= a || d <= b || c <= d {
		t.Fatalf("incorrect message order:\n%s", doc)
	}
	if strings.Contains(doc, "private backend detail") {
		t.Fatal("raw error detail rendered in transcript")
	}
	if !strings.Contains(doc, "\n   ") {
		t.Fatalf("warning did not wrap with a hanging indent:\n%s", doc)
	}
	for _, line := range strings.Split(doc, "\n") {
		if ansi.StringWidth(line) > 32 {
			t.Fatalf("line exceeds width: %q", line)
		}
	}
}

// A task's blocks show right after the call that started it, whatever came
// next; a task whose call is not shown goes last.
func TestTasksFollowTheCallThatStartedThem(t *testing.T) {
	l := NewList()
	l.SetWidth(60)
	l.SetHeight(20)
	start := agent.Block{
		ID: agent.BlockID{Scope: agent.ScopeTool, Key: "call-1"}, Role: assistant.RoleAssistant, Kind: assistant.KindToolCall,
		Complete: true, Tool: &agent.ToolBlock{Name: "startBackgroundTask", Status: agent.ToolSuccess},
	}
	update := func(key, content string) agent.Block {
		return agent.Block{
			ID: agent.BlockID{Scope: agent.ScopeMessage, Key: key, Kind: assistant.KindProgress}, Role: assistant.RoleAssistant,
			Kind: assistant.KindProgress, Complete: true, Progress: &assistant.ProgressPayload{Content: content},
		}
	}
	l.SetTranscript(
		[]agent.Block{textBlock("a", "LAUNCHING"), start, textBlock("b", "MEANWHILE")},
		[]agent.Task{{CallID: "gone", Blocks: []agent.Block{update("u2", "ORPHAN")}}, {CallID: "call-1", Blocks: []agent.Block{update("u1", "GATHERING")}}},
		nil,
	)
	doc := ansi.Strip(l.Document())
	order := []string{"LAUNCHING", "GATHERING", "MEANWHILE", "ORPHAN"}
	for i := 1; i < len(order); i++ {
		if !strings.Contains(doc, order[i-1]) || strings.Index(doc, order[i]) <= strings.Index(doc, order[i-1]) {
			t.Fatalf("blocks are not in order %q:\n%s", order, doc)
		}
	}
}

// A link in a task update's markdown is a link, like one in assistant text.
func TestTaskUpdateLinksAreLinks(t *testing.T) {
	l := NewList()
	l.SetWidth(60)
	l.SetHeight(10)
	update := agent.Block{
		ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "u1", Kind: assistant.KindProgress}, Role: assistant.RoleAssistant,
		Kind: assistant.KindProgress, Complete: true,
		Progress: &assistant.ProgressPayload{Content: "I'm launching an **[investigation](https://app.datadoghq.com/inv/1)**"},
	}
	l.SetTranscript([]agent.Block{update}, nil, nil)
	for y := range 10 {
		for x := range 60 {
			if hit, ok := l.LinkAt(x, y); ok {
				if hit.URL != "https://app.datadoghq.com/inv/1" || hit.BlockID != update.ID {
					t.Fatalf("link = %+v", hit)
				}
				return
			}
		}
	}
	t.Fatalf("no link in the update:\n%s", ansi.Strip(l.Document()))
}

func TestNoticeRowsUseEditorBackground(t *testing.T) {
	for _, isDark := range []bool{true, false} {
		sty := DefaultStyles(isDark)
		for _, level := range []NoticeLevel{NoticeInfo, NoticeWarn, NoticeError, NoticeFork} {
			rows := strings.Split(renderNotice(Notice{Level: level, Text: "A notice that wraps onto another line"}, 20, sty, false), "\n")
			if len(rows) < 2 {
				t.Fatal("setup: notice did not wrap")
			}
			for rowIdx, row := range rows {
				buf := uv.NewScreenBuffer(20, 1)
				buf.Method = ansi.GraphemeWidth
				uv.NewStyledString(row).Draw(&buf, buf.Bounds())
				if rowIdx == 0 && buf.Lines[0][0].Style.Fg != sty.Notice(level).GetForeground() {
					t.Fatalf("dark=%t level=%v: marker foreground changed", isDark, level)
				}
				for _, cell := range buf.Lines[0] {
					if cell.Style.Bg != sty.Input.Background {
						t.Fatalf("dark=%t level=%v: notice cell background = %v, want editor background %v", isDark, level, cell.Style.Bg, sty.Input.Background)
					}
				}
			}
		}
	}
}

func TestForkMarkerSeparatesCopiedAndNewMessages(t *testing.T) {
	l := NewList()
	l.SetWidth(40)
	l.SetHeight(10)
	copied := agent.Block{ID: agent.BlockID{Scope: agent.ScopeTool, Key: "copied-tool"}, Role: assistant.RoleAssistant, Kind: assistant.KindToolCall, Complete: true, Tool: &agent.ToolBlock{Name: "read_file", Status: agent.ToolSuccess}}
	next := textBlock("next", "NEW MESSAGE")
	marker := NoticeItem{ID: 1, After: copied.ID, Notice: Notice{Level: NoticeFork, Text: "Forked conversation · abcdef12\nOriginal: original-id · available in /resume"}}
	l.ReplaceTranscript([]agent.Block{copied}, nil, []NoticeItem{marker})
	l.SetTranscript([]agent.Block{copied, next}, nil, []NoticeItem{marker})
	doc := ansi.Strip(l.Document())
	if !strings.Contains(doc, "Forked conversation") || !strings.Contains(doc, "NEW MESSAGE") || !strings.Contains(doc, "⑂") || strings.Index(doc, "Forked conversation") >= strings.Index(doc, "NEW MESSAGE") {
		t.Fatalf("marker order: %s", doc)
	}
	for _, line := range strings.Split(doc, "\n") {
		if ansi.StringWidth(line) > 40 {
			t.Fatalf("overflow: %q", line)
		}
	}
}

func TestReplaceTranscriptRefreshesCacheAndKeepsViewport(t *testing.T) {
	l := headerList(t, 40, 3)
	blocks := []agent.Block{textBlock("one", "OLD CONTENT"), textBlock("two", "second"), textBlock("three", "third"), textBlock("four", "fourth")}
	l.SetTranscript(blocks, nil, nil)
	_ = l.Document()
	l.ScrollToTop()
	index, line := l.offsetIdx, l.offsetLine
	blocks[0] = textBlock("one", "NEW CONTENT")
	l.ReplaceTranscript(blocks, nil, nil)
	doc := ansi.Strip(l.Document())
	if strings.Contains(doc, "OLD CONTENT") || !strings.Contains(doc, "NEW CONTENT") {
		t.Fatalf("stale cache: %s", doc)
	}
	if l.Following() || l.offsetIdx != index || l.offsetLine != line {
		t.Fatal("viewport changed")
	}
}

// TestRenderDoneNoticeShowsSparkle confirms a finished-response notice shows
// the sparkle marker alongside its "Done · <time>" text.
func TestRenderDoneNoticeShowsSparkle(t *testing.T) {
	sty := DefaultStyles(true)
	rows := strings.Split(renderNotice(Notice{Level: NoticeDone, Text: "Done · Oct 26, 3:12 PM"}, 80, sty, false), "\n")
	if got := ansi.Strip(rows[0]); got != "  ✦ Done · Oct 26, 3:12 PM" {
		t.Fatalf("row 0 = %q, want two leading spaces, the sparkle, one space, then the done text", got)
	}
}

// TestDoneNoticePaintsNoRowBackground pins that a done line is not padded out to
// the full width the way other notices are: it marks the end of an exchange
// rather than announcing something, so it carries no background band.
func TestDoneNoticePaintsNoRowBackground(t *testing.T) {
	sty := DefaultStyles(true)
	done := renderNotice(Notice{Level: NoticeDone, Text: "Done · Oct 26, 3:12 PM"}, 80, sty, false)
	if strings.Contains(done, backgroundSGR) {
		t.Fatalf("done notice paints a row background: %q", done)
	}
	info := renderNotice(Notice{Level: NoticeInfo, Text: "Copied to clipboard."}, 80, sty, false)
	if !strings.Contains(info, backgroundSGR) {
		t.Fatal("setup: NoticeInfo paints no row background either — the comparison only means something if other notices still do")
	}
}

// TestDoneNoticeNeverOverflowsRow pins the narrow-terminal guard. This path does
// not go through PaintRowBackground, which is what clips every other level, so
// without an explicit clamp a terminal narrower than the marker overflows.
func TestDoneNoticeNeverOverflowsRow(t *testing.T) {
	sty := DefaultStyles(true)
	for _, width := range []int{1, 3, 4, 6, 12, 24} {
		doc := renderNotice(Notice{Level: NoticeDone, Text: "Done · Oct 26, 3:12 PM"}, width, sty, false)
		for _, row := range strings.Split(doc, "\n") {
			if got := ansi.StringWidth(ansi.Strip(row)); got > width {
				t.Errorf("width %d: row width %d: %q", width, got, ansi.Strip(row))
			}
		}
	}
}

// backgroundSGR is the truecolor background introducer PaintRowBackground emits.
const backgroundSGR = "48;2;"

// TestDoneNoticeWrapsAtNarrowWidth is Review Focus 4: a done notice wraps
// like any other notice at narrow widths, with continuation rows indented.
func TestDoneNoticeWrapsAtNarrowWidth(t *testing.T) {
	sty := DefaultStyles(true)
	doc := renderNotice(Notice{Level: NoticeDone, Text: "Done · Oct 26, 3:12 PM, a notice that is long enough to wrap"}, 40, sty, false)
	rows := strings.Split(doc, "\n")
	if len(rows) < 2 {
		t.Fatal("setup: notice did not wrap")
	}
	for _, row := range rows {
		if ansi.StringWidth(ansi.Strip(row)) > 40 {
			t.Fatalf("row exceeds width 40: %q", row)
		}
	}
	for _, row := range rows[1:] {
		if !strings.HasPrefix(ansi.Strip(row), "    ") {
			t.Fatalf("continuation row not indented by 4, under the text: %q", row)
		}
	}
}

// TestDoneNoticeWithoutActionIsUnchanged pins that a notice without Action
// set renders byte-identically to today's single-row form — the common case,
// since only the transcript's last done notice ever sets it.
func TestDoneNoticeWithoutActionIsUnchanged(t *testing.T) {
	sty := DefaultStyles(true)
	text := "Done · Oct 26, 3:12 PM"
	got := renderNotice(Notice{Level: NoticeDone, Text: text}, 80, sty, false)
	want := RenderDoneNotice(text, 80, sty)
	if got != want {
		t.Fatalf("notice without Action = %q, want plain form %q", got, want)
	}
	if ansi.Strip(got) != "  ✦ "+text {
		t.Fatalf("row = %q, want unchanged single-row form", ansi.Strip(got))
	}
}

// TestDoneActionRendersBorderAndButton checks the three-row form: a rounded
// border box around the ⑂ glyph, joined to the timestamp's right, with the
// timestamp itself on the middle row.
func TestDoneActionRendersBorderAndButton(t *testing.T) {
	sty := DefaultStyles(true)
	text := "Done · Oct 26, 3:12 PM"
	out := RenderDoneNoticeAction(text, 80, sty, false)
	rows := strings.Split(out, "\n")
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3:\n%s", len(rows), ansi.Strip(out))
	}
	if !strings.HasPrefix(ansi.Strip(rows[1]), "  ✦ "+text) {
		t.Fatalf("middle row = %q, want it to start with the timestamp", ansi.Strip(rows[1]))
	}
	if !strings.Contains(out, "⑂") {
		t.Fatalf("no fork glyph in:\n%s", ansi.Strip(out))
	}
	if !strings.Contains(ansi.Strip(rows[0]), "╭") || !strings.Contains(ansi.Strip(rows[2]), "╰") {
		t.Fatalf("missing rounded border corners:\nrow0=%q\nrow2=%q", ansi.Strip(rows[0]), ansi.Strip(rows[2]))
	}
}

// TestDoneActionHoverMatchesRestingShape pins the global constraint that
// hover never reflows the transcript: both forms share width and row count,
// differing only in the button's own colors.
func TestDoneActionHoverMatchesRestingShape(t *testing.T) {
	sty := DefaultStyles(true)
	text := "Done · Oct 26, 3:12 PM"
	resting := strings.Split(RenderDoneNoticeAction(text, 80, sty, false), "\n")
	hovered := strings.Split(RenderDoneNoticeAction(text, 80, sty, true), "\n")
	if len(resting) != len(hovered) {
		t.Fatalf("row count differs: resting %d, hovered %d", len(resting), len(hovered))
	}
	for i := range resting {
		rw, hw := ansi.StringWidth(ansi.Strip(resting[i])), ansi.StringWidth(ansi.Strip(hovered[i]))
		if rw != hw {
			t.Fatalf("row %d width differs: resting %d, hovered %d", i, rw, hw)
		}
	}
	if resting[1] == hovered[1] {
		t.Fatal("hovered middle row is identical to resting — hover should change the button's styling")
	}
}

// TestDoneActionDegradesToThePlainForm checks the global constraint that a
// width too narrow for the timestamp plus the button falls back to exactly
// the plain form, not just "some" row that happens to fit.
func TestDoneActionDegradesToThePlainForm(t *testing.T) {
	sty := DefaultStyles(true)
	text := "Done · Oct 26, 3:12 PM"
	if _, ok := DoneActionButtonArea(text, 20); ok {
		t.Fatal("setup: expected width 20 too narrow for the button")
	}
	got := RenderDoneNoticeAction(text, 20, sty, false)
	want := RenderDoneNotice(text, 20, sty)
	if got != want {
		t.Fatalf("degraded form = %q, want the plain form %q", got, want)
	}
}

// TestDoneActionNarrowWidths sweeps the boundary around where the button
// starts fitting: no row ever exceeds its width, and 33 — the first width
// wide enough for "Done · Oct 26, 3:12 PM" plus the button — is exactly
// where the glyph starts appearing.
func TestDoneActionNarrowWidths(t *testing.T) {
	sty := DefaultStyles(true)
	text := "Done · Oct 26, 3:12 PM"
	for _, width := range []int{1, 3, 4, 6, 32, 33} {
		out := RenderDoneNoticeAction(text, width, sty, false)
		for _, row := range strings.Split(out, "\n") {
			if got := ansi.StringWidth(ansi.Strip(row)); got > width {
				t.Errorf("width %d: row exceeds it: %q", width, ansi.Strip(row))
			}
		}
		hasGlyph := strings.Contains(out, "⑂")
		wantGlyph := width >= 33
		if hasGlyph != wantGlyph {
			t.Errorf("width %d: glyph present = %v, want %v", width, hasGlyph, wantGlyph)
		}
	}
}

// TestDoneActionButtonAreaMatchesRender asserts the exported seam Task 2
// hit-tests against the real render, not a parallel re-computation: the
// rectangle it reports must cut exactly the button box — "│ ⑂ │" on the
// middle row, the border on the rows above and below — not merely contain
// the glyph somewhere inside a looser range.
func TestDoneActionButtonAreaMatchesRender(t *testing.T) {
	sty := DefaultStyles(true)
	text := "Done · Oct 26, 3:12 PM"
	const width = 80
	area, ok := DoneActionButtonArea(text, width)
	if !ok {
		t.Fatal("setup: expected the button to fit at width 80")
	}
	if area.Dy() != 3 {
		t.Fatalf("area height = %d, want 3 (all three rows of the button)", area.Dy())
	}
	rows := strings.Split(RenderDoneNoticeAction(text, width, sty, false), "\n")
	for y := area.Min.Y; y < area.Max.Y; y++ {
		got := ansi.Strip(ansi.Cut(rows[y], area.Min.X, area.Max.X))
		want := []string{"╭───╮", "│ ⑂ │", "╰───╯"}[y]
		if got != want {
			t.Fatalf("row %d, cols %d:%d = %q, want %q; full row = %q", y, area.Min.X, area.Max.X, got, want, ansi.Strip(rows[y]))
		}
	}
	before := ansi.Strip(ansi.Cut(rows[area.Min.Y+1], 0, area.Min.X))
	if strings.Contains(before, "⑂") {
		t.Fatalf("glyph appears before the reported area start %d: %q", area.Min.X, before)
	}
}

func TestForkMarkerWithMissingAnchorIsOmitted(t *testing.T) {
	l := headerList(t, 60, 10)
	l.SetTranscript([]agent.Block{textBlock("new", "CURRENT")}, nil, []NoticeItem{{After: agent.BlockID{Scope: agent.ScopeMessage, Key: "missing"}, Notice: Notice{Level: NoticeFork, Text: "Forked conversation"}}})
	doc := l.Document()
	if strings.Contains(doc, "Forked conversation") || !strings.Contains(doc, "CURRENT") {
		t.Fatalf("invented marker: %s", doc)
	}
}
