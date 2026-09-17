package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func TestResetInvalidatesCacheAcrossConversationIdentityDomains(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(4)
	id := agent.BlockID{Scope: agent.ScopeLocal, Key: "1"}
	list.SetItems([]agent.Block{{ID: id, Rev: 0, Role: assistant.RoleUser, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: "OLD-CONTENT"}}})
	if got := list.Render(); !strings.Contains(got, "OLD-CONTENT") {
		t.Fatalf("old render missing content: %q", got)
	}

	list.Reset()
	list.SetItems([]agent.Block{{ID: id, Rev: 0, Role: assistant.RoleUser, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: "NEW-CONTENT"}}})
	got := list.Render()
	if !strings.Contains(got, "NEW-CONTENT") || strings.Contains(got, "OLD-CONTENT") {
		t.Fatalf("reset reused stale cached render: %q", got)
	}
}

// listWithTool returns a sized list holding one tool block in the given status.
func listWithTool(status agent.ToolStatus) *List {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(8)
	block := toolBlockOf(status)
	block.ID = agent.BlockID{Scope: agent.ScopeTool, Key: "call-1"}
	list.SetItems([]agent.Block{block})
	return list
}

// TestSetFrameAnimatesInFlightBlocks is the whole point of the frame counter:
// advancing it must change what an in-flight tool block renders, even though
// the block's revision and the list width are unchanged.
func TestSetFrameAnimatesInFlightBlocks(t *testing.T) {
	list := listWithTool(agent.ToolRunning)
	seen := map[string]bool{}
	for frame := range DefaultStyles(true).StatusSpinner.Len() {
		list.SetFrame(frame)
		seen[list.Render()] = true
	}
	if len(seen) < 2 {
		t.Errorf("rendered %d distinct views across a sweep, want at least 2", len(seen))
	}
}

func TestAwaitingApprovalDoesNotAnimate(t *testing.T) {
	list := listWithTool(agent.ToolAwaitingApproval)
	want := list.Render()
	if list.HasAnimated() {
		t.Fatal("approval wait should not arm animation")
	}
	list.SetFrame(17)
	if got := list.Render(); got != want {
		t.Fatalf("approval wait changed with frame:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunningToolWithoutMotionDoesNotAnimate(t *testing.T) {
	list := listWithTool(agent.ToolRunning)
	list.SetStyles(StylesFor(styles.Default(true).WithoutMotion()))
	want := list.Render()

	if list.HasAnimated() {
		t.Fatal("motion-disabled running tool should not arm animation")
	}
	list.SetFrame(17)
	if got := list.Render(); got != want {
		t.Fatalf("motion-disabled running tool changed with frame:\n%s\nwant:\n%s", got, want)
	}
}

// TestSetFrameLeavesSettledBlocksCached keeps the per-frame cost proportional
// to the number of in-flight tools rather than the length of the transcript.
func TestSetFrameLeavesSettledBlocksCached(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	want := list.Render()
	for _, frame := range []int{1, 9, 40} {
		list.SetFrame(frame)
		if got := list.Render(); got != want {
			t.Fatalf("frame %d changed a settled block", frame)
		}
	}
}

// TestSettledBlocksStayCachedAcrossFrames asserts the cache is actually doing
// the work: a settled block rendered once must not be re-rendered when the
// frame advances. Mutating the block's payload behind the list's back is
// visible only if the cache was bypassed.
func TestSettledBlocksStayCachedAcrossFrames(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	first := list.Render()
	if !strings.Contains(first, "search_logs") {
		t.Fatalf("expected the tool name in %q", first)
	}

	list.items[0].Tool.Name = "MUTATED"
	list.SetFrame(5)
	if got := list.Render(); strings.Contains(got, "MUTATED") {
		t.Error("settled block was re-rendered when the frame advanced; it should have been served from cache")
	}
}

func TestInFlightBlocksReuseCacheWithinFrame(t *testing.T) {
	list := listWithTool(agent.ToolRunning)
	first := list.renderItem(0)
	second := list.renderItem(0)
	if &first[0] != &second[0] {
		t.Fatal("running block was re-rendered without an animation frame change")
	}

	list.SetFrame(5)
	third := list.renderItem(0)
	if &first[0] == &third[0] {
		t.Fatal("running block reused cached lines after the animation frame changed")
	}
}

func TestAnimationStateTransitionInvalidatesCache(t *testing.T) {
	list := listWithTool(agent.ToolRunning)
	list.Render()

	// A transition out of the animated state must not reuse the running frame's
	// cache entry, even before SetItems supplies the normal revision update.
	list.items[0].Tool.Status = agent.ToolSuccess
	if got := ansi.Strip(list.Render()); !strings.Contains(got, "✓") {
		t.Error("settled block was served from its running animation cache entry")
	}
}

func TestInspectionGroupingCoalescesReadsAndStopsAtText(t *testing.T) {
	blocks := []agent.Block{
		inspectBlock("1", "list_files", `{"path":""}`, agent.ToolSuccess, "listing"),
		inspectBlock("2", "read_file", `{"path":"a.go"}`, agent.ToolSuccess, "contents-a"),
		inspectBlock("3", "read_file", `{"path":"b.go"}`, agent.ToolRunning, "contents-b"),
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "text"}, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: "between"}},
		inspectBlock("4", "grep_files", `{"pattern":"ToolBlock","path":"internal"}`, agent.ToolSuccess, "matches"),
	}
	list := NewList()
	sty := DefaultStyles(true)
	list.SetStyles(sty)
	list.SetWidth(100)
	list.SetHeight(20)
	list.SetItems(blocks)
	if got := len(list.view); got != 3 {
		t.Fatalf("presentation item count = %d, want 3", got)
	}
	plain := ansi.Strip(list.Render())
	for _, want := range []string{sty.StatusSpinner.Frame(0) + " inspecting", "read a.go, b.go", "between", "✓ search ToolBlock in internal"} {
		if !strings.Contains(plain, want) {
			t.Errorf("group rendering missing %q:\n%s", want, plain)
		}
	}
	for _, hidden := range []string{"listing", "contents-a", "contents-b", "matches"} {
		if strings.Contains(plain, hidden) {
			t.Errorf("group rendering exposed inspection output %q:\n%s", hidden, plain)
		}
	}
}

func TestSingletonInspectionRendersAsTool(t *testing.T) {
	for _, test := range []struct {
		name   string
		status agent.ToolStatus
		want   string
		hidden string
	}{
		{name: "running", status: agent.ToolRunning, want: "list .", hidden: "inspecting"},
		{name: "settled", status: agent.ToolSuccess, want: "✓ list .", hidden: "inspected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			list := NewList()
			list.SetStyles(DefaultStyles(true))
			list.SetWidth(80)
			list.SetHeight(8)
			list.SetItems([]agent.Block{inspectBlock("1", "list_files", `{"path":""}`, test.status, "listing")})

			plain := ansi.Strip(list.Render())
			if !strings.Contains(plain, test.want) {
				t.Fatalf("singleton inspection rendering = %q, want %q", plain, test.want)
			}
			if strings.Contains(plain, test.hidden) || strings.Contains(plain, "└") {
				t.Fatalf("singleton inspection retained grouped rendering: %q", plain)
			}
		})
	}
}

func TestReasoningStacksWithToolPresentation(t *testing.T) {
	list := NewList()
	list.SetItems([]agent.Block{
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "thinking", Kind: assistant.KindReasoning}, Kind: assistant.KindReasoning, Complete: true, Thinking: &assistant.ThinkingPayload{Content: "plan"}},
		inspectBlock("read", "read_file", `{"path":"README.md"}`, agent.ToolSuccess, "contents"),
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "answer", Kind: assistant.KindText}, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: "answer"}},
	})

	if got := list.gapAfter(0); got != 0 {
		t.Fatalf("reasoning-to-tool gap = %d, want 0", got)
	}
	if got := list.gapAfter(1); got != 1 {
		t.Fatalf("tool-to-answer gap = %d, want 1", got)
	}
}

func TestInspectionGroupKeepsMixedFailuresQuiet(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(12)
	list.SetItems([]agent.Block{
		inspectBlock("1", "read_file", `{"path":"README.md"}`, agent.ToolSuccess, "readme body"),
		inspectBlock("2", "read_file", `{"path":"missing.md"}`, agent.ToolError, "open missing.md: no such file"),
	})
	plain := ansi.Strip(list.Render())
	if !strings.Contains(plain, "✓ inspected") || !strings.Contains(plain, "read missing.md") {
		t.Fatalf("mixed inspection rendering = %q", plain)
	}
	if strings.Contains(plain, "no such file") || strings.Contains(plain, "inspection failed") {
		t.Fatalf("mixed inspection exposed routine failure: %q", plain)
	}
}

func TestInspectionGroupShowsOneDiagnosticWhenAllFail(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(12)
	list.SetItems([]agent.Block{
		inspectBlock("1", "read_file", `{"path":"one"}`, agent.ToolError, "first error\nwith detail"),
		inspectBlock("2", "read_file", `{"path":"two"}`, agent.ToolError, "second error"),
	})
	plain := ansi.Strip(list.Render())
	if !strings.Contains(plain, "✗ inspection failed") || !strings.Contains(plain, "first error with detail") {
		t.Fatalf("failed inspection rendering = %q", plain)
	}
	if strings.Contains(plain, "second error") {
		t.Fatalf("failed inspection rendered more than one diagnostic: %q", plain)
	}
}

func TestInspectionDiagnosticUsesFirstAvailableDetailBesideItsChild(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(12)
	list.SetItems([]agent.Block{
		inspectBlock("1", "read_file", `{"path":"one"}`, agent.ToolError, ""),
		inspectBlock("2", "read_file", `{"path":"two"}`, agent.ToolError, "second error"),
	})
	rows := strings.Split(ansi.Strip(list.Render()), "\n")
	child, diagnostic := -1, -1
	for i, row := range rows {
		if strings.Contains(row, "read two") {
			child = i
		}
		if strings.Contains(row, "second error") {
			diagnostic = i
		}
	}
	if child < 0 || diagnostic != child+1 {
		t.Fatalf("diagnostic was not attached to its child:\n%s", strings.Join(rows, "\n"))
	}
}

func TestInspectionGroupCacheInvalidatesWhenMemberIsAdded(t *testing.T) {
	first := inspectBlock("1", "read_file", `{"path":"one"}`, agent.ToolSuccess, "")
	second := inspectBlock("2", "read_file", `{"path":"two"}`, agent.ToolSuccess, "")
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(8)
	list.SetItems([]agent.Block{first})
	if got := ansi.Strip(list.Render()); !strings.Contains(got, "read one") {
		t.Fatalf("initial group render = %q", got)
	}

	list.SetItems([]agent.Block{first, second})
	if got := ansi.Strip(list.Render()); !strings.Contains(got, "read one, two") {
		t.Fatalf("group cache retained stale members: %q", got)
	}
}

func TestInspectionGroupRowsStayWithinWidth(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(18)
	list.SetHeight(20)
	list.SetItems([]agent.Block{
		inspectBlock("1", "read_file", `{"path":"a-very-long-file-name.md"}`, agent.ToolError, ""),
		inspectBlock("2", "read_file", `{"path":"another-long-file-name.md"}`, agent.ToolError, "open failed because the path is unavailable"),
	})
	for _, row := range strings.Split(list.Render(), "\n") {
		if got := ansi.StringWidth(row); got > list.Width() {
			t.Errorf("row width = %d, want <= %d: %q", got, list.Width(), ansi.Strip(row))
		}
	}
}

func TestInspectionSpinnerAnimationKeepsHeaderWidth(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(5)
	list.SetItems([]agent.Block{inspectBlock("1", "read_file", `{"path":"one"}`, agent.ToolRunning, "")})
	var width int
	seen := map[string]bool{}
	for _, frame := range []int{0, 8, 16} {
		list.SetFrame(frame)
		header := headerOf(list.Render())
		seen[ansi.Strip(header)] = true
		if got, want := string([]rune(ansi.Strip(header))[0]), list.sty.StatusSpinner.Frame(frame); got != want {
			t.Fatalf("frame %d glyph = %q, want %q", frame, got, want)
		}
		if frame == 0 {
			width = ansi.StringWidth(header)
		} else if got := ansi.StringWidth(header); got != width {
			t.Fatalf("frame %d header width = %d, want %d", frame, got, width)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("spinner animation rendered %d distinct frames, want 3", len(seen))
	}
}

func TestVisibleSurfaceTracksPartiallyScrolledMarkdown(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(24)
	list.SetHeight(1)
	list.SetItems([]agent.Block{
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "first", Kind: assistant.KindText}, Role: assistant.RoleAssistant, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: "first\n\nsecond"}},
	})

	list.ScrollToTop()
	if !list.ScrollByChanged(1) {
		t.Fatal("scrolling to the second Markdown row did not report a change")
	}
	if got := list.VisibleSurface().Top; got != 1 {
		t.Fatalf("visible top = %d, want 1", got)
	}
}

func TestResizeNormalizesScrolledOffsetAfterWrappingChanges(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(20)
	list.SetHeight(4)
	list.SetItems([]agent.Block{
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "first", Kind: assistant.KindText}, Role: assistant.RoleAssistant, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: strings.Repeat("first ", 20)}},
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "second", Kind: assistant.KindText}, Role: assistant.RoleAssistant, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: strings.Repeat("second ", 20)}},
	})

	list.ScrollToTop()
	list.Render()
	oldHeight := list.itemHeight(0)
	list.offsetLine = oldHeight - 1

	list.SetWidth(80)

	if list.offsetIdx >= len(list.view) {
		t.Fatalf("offset index = %d, want an item in the document", list.offsetIdx)
	}
	if got, limit := list.offsetLine, list.itemHeight(list.offsetIdx)+list.gapAfter(list.offsetIdx); got >= limit {
		t.Fatalf("offset line = %d, want < %d after resize", got, limit)
	}
	surface := list.VisibleSurface()
	documentRows := strings.Split(ansi.Strip(list.Document()), "\n")
	visibleRows := strings.Split(ansi.Strip(surface.Content), "\n")
	if surface.Top >= len(documentRows) || visibleRows[0] != documentRows[surface.Top] {
		t.Fatalf("visible top/content desynchronized: top=%d visible=%q document=%q", surface.Top, visibleRows[0], documentRows[surface.Top])
	}
}

func TestDocumentUsesFullRowsAndNoViewportFill(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(24)
	list.SetHeight(10)
	list.SetItems([]agent.Block{
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "first", Kind: assistant.KindText}, Role: assistant.RoleAssistant, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: "first"}},
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "second", Kind: assistant.KindText}, Role: assistant.RoleAssistant, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: "second"}},
	})

	document := list.Document()
	if got := len(strings.Split(document, "\n")); got != 4 {
		t.Fatalf("document rows = %d, want two items, gap, and trailing gap", got)
	}
	if strings.Count(document, "\n") >= list.Height() {
		t.Fatalf("document unexpectedly includes viewport fill rows: %q", document)
	}
}

func inspectBlock(id, name, input string, status agent.ToolStatus, output string) agent.Block {
	return agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: id},
		Kind: assistant.KindToolResult,
		Tool: &agent.ToolBlock{Name: name, Input: input, Output: output, Status: status, IsClientSide: true},
	}
}
