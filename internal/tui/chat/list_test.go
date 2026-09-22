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
	gutter := strings.Repeat(" ", list.accordion.Width())
	for _, want := range []string{sty.StatusSpinner.Frame(0) + " inspecting", "read a.go, b.go", "between", "✓ " + gutter + "search ToolBlock in internal"} {
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
		{name: "running", status: agent.ToolRunning, want: "listing .", hidden: "inspecting"},
		// The gutter sits between the settled glyph and the name, so the check
		// is no longer immediately adjacent to "list .".
		{name: "settled", status: agent.ToolSuccess, want: "✓ <gutter>list .", hidden: "inspected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			list := NewList()
			list.SetStyles(DefaultStyles(true))
			list.SetWidth(80)
			list.SetHeight(8)
			list.SetItems([]agent.Block{inspectBlock("1", "list_files", `{"path":""}`, test.status, "listing")})

			gutter := strings.Repeat(" ", list.accordion.Width())
			plain := ansi.Strip(list.Render())
			want := strings.ReplaceAll(test.want, "<gutter>", gutter)
			if !strings.Contains(plain, want) {
				t.Fatalf("singleton inspection rendering = %q, want %q", plain, want)
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

func TestAdjacentReasoningBlocksRenderAsOneActivity(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(8)
	list.SetItems([]agent.Block{
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "thinking-1", Kind: assistant.KindReasoning}, Kind: assistant.KindReasoning, Complete: true, Thinking: &assistant.ThinkingPayload{Content: "first"}},
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "thinking-2", Kind: assistant.KindReasoning}, Kind: assistant.KindReasoning, Complete: true, Thinking: &assistant.ThinkingPayload{Content: "second"}},
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "answer", Kind: assistant.KindText}, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: "answer"}},
	})

	if got := len(list.view); got != 2 {
		t.Fatalf("presentation item count = %d, want 2", got)
	}
	plain := ansi.Strip(list.Render())
	if got := strings.Count(plain, "✓ thought"); got != 1 {
		t.Fatalf("settled reasoning rows = %d, want 1:\n%s", got, plain)
	}
}

func TestReasoningGroupStaysActiveWhileMemberStreams(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(8)
	list.SetItems([]agent.Block{
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "thinking-1", Kind: assistant.KindReasoning}, Kind: assistant.KindReasoning, Complete: true, Thinking: &assistant.ThinkingPayload{Content: "first"}},
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "thinking-2", Kind: assistant.KindReasoning}, Kind: assistant.KindReasoning, Complete: false, Thinking: &assistant.ThinkingPayload{Content: "second"}},
	})

	plain := ansi.Strip(list.Render())
	if !strings.Contains(plain, "thinking.") || strings.Contains(plain, "✓ thought") {
		t.Fatalf("active reasoning group = %q", plain)
	}
	if !list.HasAnimated() {
		t.Fatal("active reasoning group did not remain animated")
	}
}

func TestToolMarginsCollapseAcrossAdjacentItems(t *testing.T) {
	for _, test := range []struct {
		name   string
		blocks []agent.Block
		first  string
		second string
	}{
		{
			name:   "write then exec",
			blocks: []agent.Block{layoutToolBlock("write_file", `{"path":"one.txt"}`), layoutToolBlock("exec_command", `{"cmd":"pwd"}`)},
			first:  "wrote one.txt",
			second: "ran pwd",
		},
		{
			name:   "exec then write",
			blocks: []agent.Block{layoutToolBlock("exec_command", `{"cmd":"pwd"}`), layoutToolBlock("write_file", `{"path":"one.txt"}`)},
			first:  "ran pwd",
			second: "wrote one.txt",
		},
		{
			name:   "write then edit",
			blocks: []agent.Block{layoutToolBlock("write_file", `{"path":"one.txt"}`), layoutToolBlock("edit_file", `{"path":"one.txt"}`)},
			first:  "wrote one.txt",
			second: "edited one.txt",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			list := NewList()
			list.SetStyles(DefaultStyles(true))
			list.SetWidth(80)
			list.SetHeight(8)
			list.SetItems(test.blocks)

			rows := strings.Split(ansi.Strip(list.Render()), "\n")
			first, second := -1, -1
			for i, row := range rows {
				if strings.Contains(row, test.first) {
					first = i
				}
				if strings.Contains(row, test.second) {
					second = i
				}
			}
			if first < 0 || second < 0 {
				t.Fatalf("tool rows missing from render:\n%s", strings.Join(rows, "\n"))
			}
			if got, want := second-first-1, 1; got != want {
				t.Fatalf("blank rows between tools = %d, want %d:\n%s", got, want, strings.Join(rows, "\n"))
			}
		})
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
	// This single running tool is guttered, but the gutter is spliced in after
	// the status glyph so the glyph stays the row's leftmost cell.
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

func layoutToolBlock(name, input string) agent.Block {
	return agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: name + input},
		Kind: assistant.KindToolResult,
		Tool: &agent.ToolBlock{Name: name, Input: input, Status: agent.ToolSuccess, IsClientSide: true},
	}
}

func TestToggleAllDisclosureKeepsViewportAnchoredOnCollapse(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(2)
	list.SetItems([]agent.Block{
		inspectBlock("1", "list_files", `{"path":"a"}`, agent.ToolSuccess, "first"),
		inspectBlock("2", "list_files", `{"path":"b"}`, agent.ToolSuccess, "second"),
	})
	list.Render() // populate the render cache ToggleAllDisclosure reads through

	if h := list.itemHeight(0); h < 3 {
		t.Fatalf("fixture item 0 height = %d, want at least 3 expanded lines to exercise the clamp", h)
	}
	list.offsetIdx, list.offsetLine = 0, 2 // scrolled two lines into item 0's body

	list.ToggleAllDisclosure() // collapse everything

	if list.offsetIdx != 0 {
		t.Fatalf("collapsing walked the viewport to item %d, want it to stay anchored on item 0", list.offsetIdx)
	}
	if h := list.itemHeight(0); list.offsetLine >= h {
		t.Fatalf("offsetLine %d falls outside item 0's collapsed height %d", list.offsetLine, h)
	}
}

func TestGutteredItemNarrowsForTheAccordion(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	it := list.view[0]
	if !list.guttered(it) {
		t.Fatal("a single-tool presentation item must be guttered")
	}
	want := list.width - list.accordion.Width()
	if got := list.itemWidth(it); got != want {
		t.Fatalf("itemWidth() = %d, want %d", got, want)
	}
}

func TestNonGutteredItemUsesFullWidth(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(8)
	list.SetItems([]agent.Block{
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "thinking", Kind: assistant.KindReasoning}, Kind: assistant.KindReasoning, Complete: true, Thinking: &assistant.ThinkingPayload{Content: "plan"}},
	})
	it := list.view[0]
	if list.guttered(it) {
		t.Fatal("a reasoning group must not be guttered")
	}
	if got := list.itemWidth(it); got != list.width {
		t.Fatalf("itemWidth() = %d, want the full width %d", got, list.width)
	}
}

func TestNarrowViewportDisablesTheGutter(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	list.SetWidth(list.accordion.Width())
	it := list.view[0]
	if list.hasGutterRoom() {
		t.Fatal("width equal to the accordion's own width leaves no room for content")
	}
	if got := list.itemWidth(it); got != list.width {
		t.Fatalf("itemWidth() = %d, want the unreduced width %d when there is no gutter room", got, list.width)
	}
}

func TestGutteredItemStaysWithinTotalWidth(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	for _, line := range list.renderItem(0) {
		if w := ansi.StringWidth(line); w > list.width {
			t.Fatalf("rendered line %q is %d cells wide, want <= %d", line, w, list.width)
		}
	}
}

func TestToggleDisclosureCollapsesToHeaderLineAndBack(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	id := list.view[0].id
	full := list.itemHeight(0)
	if full <= 1 {
		t.Fatalf("fixture tool did not produce a disclosable body, height = %d", full)
	}

	list.ToggleDisclosure(id)
	if got := list.itemHeight(0); got != 1 {
		t.Fatalf("collapsed height = %d, want 1", got)
	}

	list.ToggleDisclosure(id)
	if got := list.itemHeight(0); got != full {
		t.Fatalf("re-expanded height = %d, want %d", got, full)
	}
}

func TestHeaderOnlyToolReservesGutterButDrawsNoChevron(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(4)
	list.SetItems([]agent.Block{{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: "call-1"},
		Kind: assistant.KindToolResult,
		Tool: &agent.ToolBlock{Name: "list_monitors", Status: agent.ToolSuccess},
	}})
	list.Render() // populates zones for the current viewport

	if height := list.itemHeight(0); height != 1 {
		t.Fatalf("header-only tool rendered %d lines, want 1", height)
	}
	if _, ok := list.ZoneAt(0, 0); ok {
		t.Fatal("header-only tool recorded a clickable zone")
	}
	lines := list.itemLines(0)
	gutter := strings.Repeat(" ", list.accordion.Width())
	// The gutter sits right after the fixed-width status glyph, not before it,
	// so the glyph stays the leftmost cell on the row.
	if got := ansi.Strip(ansi.Cut(lines[0], statusGlyphWidth, statusGlyphWidth+list.accordion.Width())); got != gutter {
		t.Fatalf("header-only tool did not reserve the gutter after the status glyph: %q", lines[0])
	}
}

func TestDocumentAgreesWithVisibleSurfaceOnTheGutter(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	surface := list.VisibleSurface()
	document := list.Document()
	surfaceLines := strings.Split(surface.Content, "\n")
	documentLines := strings.Split(document, "\n")
	height := list.itemHeight(0)
	for i := range height {
		if surfaceLines[i] != documentLines[i] {
			t.Fatalf("row %d differs — surface %q, document %q", i, surfaceLines[i], documentLines[i])
		}
	}
}

// TestDocumentAgreesWithVisibleSurfaceAcrossCollapseHoverAndScroll extends the
// single-item, top-of-viewport check above to the cases it can't catch: a
// collapsed item, a hovered item, and a viewport scrolled to a non-zero
// offset, all at once. It compares each visible row against the document row
// it actually corresponds to (surface.Top + i), not just row 0..N, so a
// scroll-position bug in either renderSurface's or Document's gutter/collapse
// bookkeeping would show up here even though the two never start at the same
// document row.
func TestDocumentAgreesWithVisibleSurfaceAcrossCollapseHoverAndScroll(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(3)

	keys := []string{"call-0", "call-1", "call-2"}
	blocks := make([]agent.Block, len(keys))
	for i, key := range keys {
		block := toolBlockOf(agent.ToolSuccess)
		block.ID = agent.BlockID{Scope: agent.ScopeTool, Key: key}
		blocks[i] = block
	}
	list.SetItems(blocks)

	// item 0 collapsed, item 1 hovered, item 2 plain — and the viewport
	// scrolled one line into item 1, so row 0 of the surface is NOT row 0 of
	// the document.
	list.ToggleDisclosure(list.view[0].id)
	list.SetHovered(list.view[1].id, true)
	list.ScrollToTop()
	list.ScrollBy(1)

	surface := list.VisibleSurface()
	if surface.Top == 0 {
		t.Fatal("fixture did not scroll into a non-zero document offset")
	}
	document := list.Document()
	surfaceLines := strings.Split(surface.Content, "\n")
	documentLines := strings.Split(document, "\n")

	for i, line := range surfaceLines {
		row := surface.Top + i
		if row >= len(documentLines) {
			break // remaining surface rows are viewport-fill padding past the document's end
		}
		if line != documentLines[row] {
			t.Fatalf("surface row %d (document row %d) differs — surface %q, document %q", i, row, line, documentLines[row])
		}
	}
}

func TestZoneNotRecordedWhenHeaderScrollsOffTop(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	// listWithTool's fixture is taller than the height set below, and List
	// defaults to follow=true; without pinning to the top first, the
	// pre-existing follow self-heal (renderSurface) would immediately scroll
	// to the item's tail on the very first Render(), before this test's own
	// explicit ScrollBy(1) gets a chance to exercise the scroll-off case it's
	// named for. ScrollToTop also sets follow=false, giving a deterministic
	// starting position with the header visible.
	list.ScrollToTop()
	list.SetHeight(1)
	list.Render()
	if _, ok := list.ZoneAt(0, 0); !ok {
		t.Fatal("zone missing before scrolling")
	}

	list.ScrollBy(1)
	list.Render()
	if _, ok := list.ZoneAt(0, 0); ok {
		t.Fatal("zone still recorded after its header scrolled off the top")
	}
}

func TestToggleAllDisclosureAlternatesExpandFirst(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	full := list.itemHeight(0)

	list.ToggleAllDisclosure() // first press: expand-first, a no-op from all-expanded
	if got := list.itemHeight(0); got != full {
		t.Fatalf("first ctrl+o changed height to %d, want %d (expand-first is a no-op)", got, full)
	}

	list.ToggleAllDisclosure() // second press: collapse
	if got := list.itemHeight(0); got != 1 {
		t.Fatalf("second ctrl+o height = %d, want 1", got)
	}

	list.ToggleAllDisclosure() // third press: expand again
	if got := list.itemHeight(0); got != full {
		t.Fatalf("third ctrl+o height = %d, want %d", got, full)
	}
}

func TestToggleAllDisclosureOverridesAnIndividualToggle(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	id := list.view[0].id
	full := list.itemHeight(0)

	list.ToggleDisclosure(id)
	if got := list.itemHeight(0); got != 1 {
		t.Fatalf("individual collapse failed, height = %d", got)
	}

	list.ToggleAllDisclosure() // universally overrides: first press means expand
	if got := list.itemHeight(0); got != full {
		t.Fatalf("global expand did not override the individual collapse: height = %d, want %d", got, full)
	}
}

func TestSetHoveredReportsChangeOnlyOnActualChange(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	id := list.view[0].id
	other := agent.BlockID{Scope: agent.ScopeTool, Key: "call-2"}

	if !list.SetHovered(id, true) {
		t.Fatal("first hover should report a change")
	}
	if list.SetHovered(id, true) {
		t.Fatal("repeating the same hover should not report a change")
	}
	if !list.SetHovered(other, true) {
		t.Fatal("hovering a different block should report a change")
	}
	if !list.SetHovered(agent.BlockID{}, false) {
		t.Fatal("clearing hover should report a change")
	}
	if list.SetHovered(agent.BlockID{}, false) {
		t.Fatal("clearing hover twice should not report a change the second time")
	}
}

func TestResetClearsDisclosureAndHoverState(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	id := list.view[0].id
	list.ToggleDisclosure(id)
	list.SetHovered(id, true)

	list.Reset()

	if list.collapsed[id] {
		t.Fatal("Reset left a stale collapsed entry")
	}
	if list.hasHover {
		t.Fatal("Reset left hover active")
	}
}

// TestHoveringABlockDoesNotChangeItemHeight pins down the spec's "hover state
// is not persisted and has no bearing on itemHeight": hovering only changes
// which style itemLines picks for the control glyph, never how many lines
// the item occupies (that's collapsed's job alone).
func TestHoveringABlockDoesNotChangeItemHeight(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	id := list.view[0].id
	before := list.itemHeight(0)

	if got := list.SetHovered(id, true); !got {
		t.Fatal("hovering the block should report a change")
	}
	if got := list.itemHeight(0); got != before {
		t.Fatalf("hovering changed itemHeight from %d to %d", before, got)
	}

	list.SetHovered(agent.BlockID{}, false)
	if got := list.itemHeight(0); got != before {
		t.Fatalf("clearing hover changed itemHeight from %d to %d", before, got)
	}
}

// TestResetRestoresExpandAllAndClearsZones covers the two pieces of Reset's
// state clearing that TestResetClearsDisclosureAndHoverState doesn't:
// expandAll (so a fresh conversation's first ctrl+o is expand-first again,
// not whatever direction the previous conversation left it pointing) and
// zones (a stale zone would let ZoneAt report a hit for a row that belongs
// to a conversation that no longer exists).
func TestResetRestoresExpandAllAndClearsZones(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	list.Render() // populates zones for the current viewport
	if len(list.zones) == 0 {
		t.Fatal("fixture did not populate a zone before Reset")
	}

	list.ToggleAllDisclosure() // flips expandAll away from its post-NewList default
	if list.expandAll {
		t.Fatal("ToggleAllDisclosure did not flip expandAll")
	}

	list.Reset()

	if !list.expandAll {
		t.Fatal("Reset did not restore expandAll to true")
	}
	if len(list.zones) != 0 {
		t.Fatal("Reset left stale zones")
	}
}
