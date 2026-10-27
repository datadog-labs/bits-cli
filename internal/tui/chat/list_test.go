package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/filediff"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
	"github.com/datadog-labs/bits-cli/internal/tui/components"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
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

const disclosableOutput = "row 1\nrow 2\nrow 3\nrow 4\nrow 5"

func disclosableToolBlock(key string) agent.Block {
	block := toolBlockOf(agent.ToolSuccess)
	block.ID = agent.BlockID{Scope: agent.ScopeTool, Key: key}
	block.Tool.Output = disclosableOutput
	// Generic client tools isolate disclosure tests from server grouping.
	block.Tool.IsClientSide = true
	return block
}

// listWithTool returns a sized list holding one tool block with output.
func listWithTool(status agent.ToolStatus) *List {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(8)
	block := toolBlockOf(status)
	block.ID = agent.BlockID{Scope: agent.ScopeTool, Key: "call-1"}
	block.Tool.Output = disclosableOutput
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

func TestInspectionGroupingKeepsChildrenAndStopsAtText(t *testing.T) {
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
	if got := len(list.view); got != 6 {
		t.Fatalf("presentation item count = %d, want 6", got)
	}
	plain := ansi.Strip(list.Render())
	for _, want := range []string{sty.StatusSpinner.Frame(0) + " Files · 3 calls · 1 running", "Read a.go", "Reading b.go", "between", "▶ Search ToolBlock in internal"} {
		if !strings.Contains(plain, want) {
			t.Errorf("group rendering missing %q:\n%s", want, plain)
		}
	}
	for _, hidden := range []string{"Listing", "contents-a", "contents-b", "matches"} {
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
		{name: "running", status: agent.ToolRunning, want: "Listing .", hidden: "inspecting"},
		{name: "settled", status: agent.ToolSuccess, want: "▶ List .", hidden: "inspected"},
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
	if got := strings.Count(plain, "Reasoning"); got != 1 {
		t.Fatalf("settled reasoning rows = %d, want 1:\n%s", got, plain)
	}
}

func TestReasoningExpandsToItsThinkingText(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(8)
	list.SetItems([]agent.Block{
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "thinking-1", Kind: assistant.KindReasoning}, Kind: assistant.KindReasoning, Complete: true, Thinking: &assistant.ThinkingPayload{Content: "first idea"}},
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "thinking-2", Kind: assistant.KindReasoning}, Kind: assistant.KindReasoning, Complete: true, Thinking: &assistant.ThinkingPayload{Content: "second idea"}},
	})

	compact := ansi.Strip(strings.Join(list.renderItem(0), "\n"))
	if !strings.Contains(compact, "▶ Reasoning") || strings.Contains(compact, "idea") {
		t.Fatalf("reasoning did not start collapsed on its header:\n%s", compact)
	}
	if _, ok := list.AccordionAt(0); !ok {
		t.Fatal("reasoning header is not clickable")
	}

	list.ToggleDisclosure(list.view[0].id)
	expanded := ansi.Strip(strings.Join(list.renderItem(0), "\n"))
	for _, want := range []string{"▼ Reasoning", "first idea", "second idea"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded reasoning missing %q:\n%s", want, expanded)
		}
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
	if !strings.Contains(plain, "Reasoning.") || strings.Contains(plain, "✓ Reasoning") {
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
		gap    int
	}{
		{
			name:   "write then exec",
			gap:    1,
			blocks: []agent.Block{layoutToolBlock("write_file", `{"path":"one.txt"}`), layoutToolBlock("exec_command", `{"cmd":"pwd"}`)},
			first:  "Wrote one.txt",
			second: "Ran pwd",
		},
		{
			name:   "exec then write",
			gap:    1,
			blocks: []agent.Block{layoutToolBlock("exec_command", `{"cmd":"pwd"}`), layoutToolBlock("write_file", `{"path":"one.txt"}`)},
			first:  "Ran pwd",
			second: "Wrote one.txt",
		},
		{
			name:   "write then edit",
			blocks: []agent.Block{layoutToolBlock("write_file", `{"path":"one.txt"}`), layoutToolBlock("edit_file", `{"path":"one.txt"}`)},
			first:  "Wrote one.txt",
			second: "Edited one.txt",
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
			if got, want := second-first-1, test.gap; got != want {
				t.Fatalf("blank rows between tools = %d, want %d:\n%s", got, want, strings.Join(rows, "\n"))
			}
		})
	}
}

// Failed calls remain visible beneath their shared group header.
func TestInspectionGroupRowsCarryTheirOwnOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		blocks []agent.Block
	}{
		{"mixed", "✗ Files · 2 calls · 1 failed", []agent.Block{
			inspectBlock("1", "read_file", `{"path":"README.md"}`, agent.ToolSuccess, "readme body"),
			inspectBlock("2", "read_file", `{"path":"missing.md"}`, agent.ToolError, "open missing.md: no such file"),
		}},
		{"all failed", "✗ Files · 2 calls · 2 failed", []agent.Block{
			inspectBlock("1", "read_file", `{"path":"one"}`, agent.ToolError, "first error\nwith detail"),
			inspectBlock("2", "read_file", `{"path":"two"}`, agent.ToolError, "second error"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := NewList()
			list.SetStyles(DefaultStyles(true))
			list.SetWidth(80)
			list.SetHeight(12)
			list.SetItems(tc.blocks)
			plain := ansi.Strip(list.Render())
			if !strings.Contains(plain, tc.header) {
				t.Fatalf("group header is not %q:\n%s", tc.header, plain)
			}
			for _, block := range tc.blocks {
				if block.Tool.Status != agent.ToolError {
					continue
				}
				want := collapseWS(block.Tool.Output)
				if !strings.Contains(plain, want) {
					t.Fatalf("group row missing %q:\n%s", want, plain)
				}
			}
		})
	}

	t.Run("lifecycle and width", func(t *testing.T) {
		list := NewList()
		list.SetStyles(DefaultStyles(true))
		list.SetWidth(40)
		list.SetHeight(12)
		list.SetItems([]agent.Block{
			inspectBlock("1", "read_file", `{"path":"a.go"}`, agent.ToolDenied, ""),
			inspectBlock("2", "read_file", `{"path":"b.go"}`, agent.ToolCancelled, ""),
			inspectBlock("3", "read_file", `{"path":"c.go"}`, agent.ToolError, strings.Repeat("very long error ", 10)),
			inspectBlock("4", "read_file", `{"path":"d.go"}`, agent.ToolError, ""),
		})
		lines := strings.Split(strings.TrimRight(list.Document(), "\n"), "\n")
		plain := ansi.Strip(strings.Join(lines, "\n"))
		for _, want := range []string{"Read a.go · denied", "Read b.go · stopped", "very long error", "failed"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("group row missing %q:\n%s", want, plain)
			}
		}
		if len(lines) != 7 {
			t.Fatalf("group rendered %d lines, want the header, four calls and two errors:\n%s", len(lines), plain)
		}
	})
}

func TestInspectionGroupCacheInvalidatesWhenMemberIsAdded(t *testing.T) {
	first := inspectBlock("1", "read_file", `{"path":"one"}`, agent.ToolSuccess, "")
	second := inspectBlock("2", "read_file", `{"path":"two"}`, agent.ToolSuccess, "")
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(8)
	list.SetItems([]agent.Block{first})
	if got := ansi.Strip(list.Render()); !strings.Contains(got, "Read one") {
		t.Fatalf("initial group render = %q", got)
	}

	list.SetItems([]agent.Block{first, second})
	if got := ansi.Strip(list.Render()); !strings.Contains(got, "Read one") || !strings.Contains(got, "Read two") {
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
	list.SetItems([]agent.Block{disclosableToolBlock("1"), disclosableToolBlock("2")})
	list.ToggleAllDisclosure() // expand everything
	list.Render()              // populate the render cache ToggleAllDisclosure reads through

	list.offsetIdx, list.offsetLine = 0, 2 // scrolled two lines into item 0's body

	list.ToggleAllDisclosure() // collapse everything

	if list.offsetIdx != 0 {
		t.Fatalf("collapsing walked the viewport to item %d, want it to stay anchored on item 0", list.offsetIdx)
	}
	if h := list.itemHeight(0); list.offsetLine >= h {
		t.Fatalf("offsetLine %d falls outside item 0's collapsed height %d", list.offsetLine, h)
	}
}

// TestGutterReservation: single tools and reasoning groups reserve the
// accordion gutter, and only when the viewport leaves room for content beside
// it.
func TestGutterReservation(t *testing.T) {
	reasoning := agent.Block{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "thinking", Kind: assistant.KindReasoning}, Kind: assistant.KindReasoning, Complete: true, Thinking: &assistant.ThinkingPayload{Content: "plan"}}
	tool := toolBlockOf(agent.ToolSuccess)
	tool.ID = agent.BlockID{Scope: agent.ScopeTool, Key: "call-1"}
	gutterWidth := components.AccordionWidth(DefaultStyles(true).Accordion)
	for _, tc := range []struct {
		name  string
		block agent.Block
		width int
		want  int
	}{
		{"single tool", tool, 80, gutterWidth},
		{"reasoning group", reasoning, 80, gutterWidth},
		{"no room left for content", tool, gutterWidth, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := NewList()
			list.SetStyles(DefaultStyles(true))
			list.SetWidth(tc.width)
			list.SetHeight(8)
			list.SetItems([]agent.Block{tc.block})
			if got := list.gutter(list.view[0]); got != tc.want {
				t.Fatalf("gutter() = %d, want %d", got, tc.want)
			}
		})
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

// TestEveryToolExpandsToItsOutput: a default tool shows only its header until
// expanded, even when there is nothing to show.
func TestEveryToolExpandsToItsOutput(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
	}{
		{"no output", "", "(no output)"},
		{"multi-line output", disclosableOutput, disclosableOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := listWithTool(agent.ToolSuccess)
			list.items[0].Tool.Output = tc.output
			list.SetItems(list.items)
			id := list.view[0].id

			if got := ansi.Strip(strings.Join(list.renderItem(0), "\n")); !strings.Contains(got, "▶") || strings.Contains(got, "\n") {
				t.Fatalf("tool did not start collapsed on its header:\n%s", got)
			}
			list.ToggleDisclosure(id)
			got := list.renderItem(0)
			for _, want := range append([]string{"▼"}, strings.Split(tc.want, "\n")...) {
				if !strings.Contains(ansi.Strip(strings.Join(got, "\n")), want) {
					t.Fatalf("expanded view missing %q:\n%s", want, ansi.Strip(strings.Join(got, "\n")))
				}
			}
			if height := list.itemHeight(0); height != len(got) {
				t.Fatalf("expanded height = %d, rendered %d rows", height, len(got))
			}
			list.ToggleDisclosure(id)
			if got := list.itemHeight(0); got != 1 {
				t.Fatalf("re-collapsed height = %d, want the header only", got)
			}
		})
	}
}

// TestNothingToExpandDrawsNoGutter: a block without a full view renders at the
// full width with no gutter, is not clickable, and ignores ctrl+o.
func TestNothingToExpandDrawsNoGutter(t *testing.T) {
	question := agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: "call-1"},
		Kind: assistant.KindToolResult, Complete: true,
		Tool: &agent.ToolBlock{
			Name: spec.AskUserQuestion, IsClientSide: true, Status: agent.ToolRunning,
			Input: `{"questions":[{"question":"One?","options":[{"label":"A","description":""}]}]}`,
		},
	}
	reasoning := agent.Block{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "thinking", Kind: assistant.KindReasoning}, Kind: assistant.KindReasoning, Complete: true, Thinking: &assistant.ThinkingPayload{Content: "  "}}
	for _, tc := range []struct {
		name  string
		block agent.Block
	}{
		{"question tool", question},
		{"reasoning without text", reasoning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := NewList()
			list.SetStyles(DefaultStyles(true))
			list.SetWidth(80)
			list.SetHeight(8)
			list.SetItems([]agent.Block{tc.block})

			if _, ok := list.AccordionAt(0); ok {
				t.Fatal("block with nothing to expand is clickable")
			}
			bare := list.renderPresentationItem(list.view[0], renderContext{width: list.width, sty: list.sty, frame: list.frame})
			if got := strings.Join(list.renderItem(0), "\n"); got != bare {
				t.Fatalf("block with nothing to expand was not rendered bare at full width:\n%s\nwant:\n%s", ansi.Strip(got), ansi.Strip(bare))
			}
			height := list.itemHeight(0)
			list.ToggleAllDisclosure()
			if got := list.itemHeight(0); got != height {
				t.Fatalf("ctrl+o changed the height from %d to %d", height, got)
			}
		})
	}
}

// TestDocumentAgreesWithVisibleSurfaceAcrossCollapseAndScroll compares each
// visible row against the document row it corresponds to (surface.Top + i)
// with a collapsed item and a non-zero scroll offset.
func TestDocumentAgreesWithVisibleSurfaceAcrossCollapseAndScroll(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(3)

	keys := []string{"call-0", "call-1", "call-2"}
	blocks := make([]agent.Block, len(keys))
	for i, key := range keys {
		blocks[i] = disclosableToolBlock(key)
	}
	list.SetItems(blocks)

	list.ToggleDisclosure(list.view[0].id)
	list.ScrollToTop()
	list.ScrollBy(1)

	surface := list.VisibleSurface()
	if surface.Top == 0 {
		t.Fatal("fixture did not scroll into a non-zero document offset")
	}
	documentLines := strings.Split(list.Document(), "\n")
	for i, line := range strings.Split(surface.Content, "\n") {
		row := surface.Top + i
		if row >= len(documentLines) {
			break // viewport-fill padding past the document's end
		}
		if line != documentLines[row] {
			t.Fatalf("surface row %d (document row %d) differs — surface %q, document %q", i, row, line, documentLines[row])
		}
	}
}

// Dark-theme hover fills, as SGR background parameters.
const (
	headerHoverFill = "48;2;34;36;43" // accordionHover (#22242B)
	detailHoverFill = "48;2;28;30;38" // accordionDetailHover (#1C1E26)
)

func hasHoverFill(line string) bool {
	return strings.Contains(line, headerHoverFill) || strings.Contains(line, detailHoverFill)
}

// listWithEditDiff returns a sized list holding one edit_file block whose
// collapsed view renders a multi-line diff preview below its header.
func listWithEditDiff() *List {
	var before, after strings.Builder
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&before, "line-%d\n", i)
		fmt.Fprintf(&after, "LINE-%d\n", i)
	}
	diff, display := filediff.BuildWithDisplay("a/f.go", "b/f.go", before.String(), after.String())
	block := editorToolBlock("edit_file", display, &filediff.State{
		Phase:  filediff.PhaseApplied,
		Change: &filediff.Change{Path: "f.go", Op: filediff.OpEdit, State: filediff.ChangeApplied, Diff: &diff},
	})
	block.ID = agent.BlockID{Scope: agent.ScopeTool, Key: "call-1"}

	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(20)
	list.SetItems([]agent.Block{block})
	return list
}

// TestAccordionZoneIsTheWholeRenderedBlock: whatever a guttered block renders
// (a lone header, a collapsed preview such as exec_command or edit_file, or
// the full disclosure) is both its click target and its hover region. From
// any of its rows, hover paints the header fill on row 0 and the detail fill
// below, and neither targeting nor painting reaches past its last row.
func TestAccordionZoneIsTheWholeRenderedBlock(t *testing.T) {
	for _, tc := range []struct {
		name     string
		list     func() *List
		expanded bool
	}{
		{"collapsed header only", func() *List { return listWithTool(agent.ToolSuccess) }, false},
		{"expanded detail", func() *List { l := listWithTool(agent.ToolSuccess); l.SetHeight(24); return l }, true},
		{"collapsed preview", listWithEditDiff, false},
		{"expanded preview", listWithEditDiff, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := tc.list()
			if tc.expanded {
				list.ToggleDisclosure(list.view[0].id)
			}
			list.ScrollToTop()
			height := list.itemHeight(0)
			if height >= list.height {
				t.Fatalf("fixture is %d rows in a %d-row viewport, want room below it", height, list.height)
			}

			for row := range height {
				if id, ok := list.AccordionAt(row); !ok || id != list.view[0].id {
					t.Fatalf("AccordionAt(%d) = %v, %t, want the block", row, id, ok)
				}
			}
			if _, ok := list.AccordionAt(height); ok {
				t.Fatalf("AccordionAt(%d) matched past the block's last row", height)
			}

			for _, pointer := range []int{0, height - 1} {
				list.SetPointerRow(pointer)
				lines := strings.Split(list.Render(), "\n")
				for row := range height {
					want := detailHoverFill
					if row == 0 {
						want = headerHoverFill
					}
					if !strings.Contains(lines[row], want) {
						t.Fatalf("pointer on row %d: row %d missing its hover fill %q:\n%s", pointer, row, want, lines[row])
					}
				}
				if hasHoverFill(lines[height]) {
					t.Fatalf("pointer on row %d: hover leaked past the block into row %d:\n%s", pointer, height, lines[height])
				}
			}
		})
	}
}

// TestAccordionZoneSurvivesTheHeaderScrollingOff: the header is not the
// hit-test anchor, so scrolling it off must keep the rest targeted, and the
// rows left on screen keep the detail fill rather than taking the header's.
func TestAccordionZoneSurvivesTheHeaderScrollingOff(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	list.ToggleDisclosure(list.view[0].id)
	list.SetHeight(1)
	list.ScrollBy(1) // scroll past the header; no chevron remains visible

	if id, ok := list.AccordionAt(0); !ok || id != list.view[0].id {
		t.Fatalf("AccordionAt(0) = %v, %t, want the block even with its header scrolled off", id, ok)
	}
	if _, ok := list.AccordionAt(1); ok {
		t.Fatal("AccordionAt matched a row outside the viewport")
	}

	list.SetPointerRow(0)
	line := list.Render()
	if !strings.Contains(line, detailHoverFill) || strings.Contains(line, headerHoverFill) {
		t.Fatalf("visible detail row not painted with the detail fill alone:\n%s", line)
	}
}

func TestHoverPaintsOnlyTheSurfaceNotTheDocument(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	list.ScrollToTop()
	resting := list.Render()
	document := list.Document()
	height := list.itemHeight(0)

	list.SetPointerRow(0)
	if !list.Hovered() {
		t.Fatal("pointer over the header did not hover it")
	}
	if list.Render() == resting {
		t.Fatal("hover did not change the rendered surface")
	}
	if list.Document() != document || list.itemHeight(0) != height {
		t.Fatal("hover leaked into the document or the item height")
	}

	list.SetPointerRow(1)
	if list.Hovered() || list.Render() != resting {
		t.Fatal("moving off the header row did not clear hover")
	}
}

// TestToggleAllDisclosure: the first ctrl+o expands, the next collapses, and
// each press discards individual toggles.
func TestToggleAllDisclosure(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	compact := list.itemHeight(0)
	list.ToggleDisclosure(list.view[0].id)
	full := list.itemHeight(0)

	list.ToggleAllDisclosure()
	if got := list.itemHeight(0); got != full {
		t.Fatalf("expand-all height = %d, want %d", got, full)
	}
	list.ToggleDisclosure(list.view[0].id) // collapsed individually
	list.ToggleAllDisclosure()
	if got := list.itemHeight(0); got != compact {
		t.Fatalf("collapse-all height = %d, want %d (individual collapse discarded)", got, compact)
	}
}

func TestExpandAllAppliesToBlocksArrivingLater(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	compact := list.itemHeight(0)
	list.ToggleAllDisclosure()

	list.SetItems(append(list.items, disclosableToolBlock("call-2")))
	if got := list.itemHeight(1); got <= compact {
		t.Fatalf("a block arriving after expand-all rendered %d lines, want more than the compact %d", got, compact)
	}
}

func TestResetClearsDisclosureState(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	compact := list.itemHeight(0)
	blocks := list.items
	for _, expand := range []func(){
		list.ToggleAllDisclosure,
		func() { list.ToggleDisclosure(list.view[0].id) },
	} {
		expand()
		list.Reset()
		list.SetItems(blocks)
		if got := list.itemHeight(0); got != compact {
			t.Fatalf("after Reset height = %d, want the default compact %d", got, compact)
		}
	}
}

// TestHoverFollowsTheTailPinBeforeRender: a streamed append while following
// moves content under a stationary pointer. Hit-testing runs before the next
// render (pointer-shape reconciliation), so it must see the re-anchored tail.
func TestHoverFollowsTheTailPinBeforeRender(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(5) // fits one compact item
	blocks := make([]agent.Block, 0, 4)
	for i := range 4 {
		blocks = append(blocks, disclosableToolBlock(fmt.Sprintf("call-%d", i)))
	}
	list.SetItems(blocks[:3])
	chevronRow := func() int {
		for i, line := range strings.Split(list.Render(), "\n") {
			if strings.ContainsAny(ansi.Strip(line), "▼▶") {
				return i
			}
		}
		t.Fatal("no chevron visible")
		return -1
	}
	row := chevronRow()
	list.SetPointerRow(row)
	if !list.Hovered() {
		t.Fatal("pointer over the header did not hover it")
	}

	// Streamed append of a one-line (header-only) tool: the tail pin shifts
	// content up by an odd row count, so a detail row lands under the pointer.
	blocks[3] = agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: "call-3"},
		Kind: assistant.KindToolResult,
		Tool: &agent.ToolBlock{Name: "list_monitors", Status: agent.ToolSuccess},
	}
	list.SetItems(blocks)
	hovered := list.Hovered()
	if want := chevronRow() == row; hovered != want {
		t.Fatalf("Hovered() before render = %t, but the rendered row under the pointer is a header: %t", hovered, want)
	}
}

func TestViewportStaysFilledAfterGrowing(t *testing.T) {
	blocks := make([]agent.Block, 10)
	for i := range blocks {
		blocks[i] = agent.Block{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: fmt.Sprint(i), Kind: assistant.KindText}, Role: assistant.RoleAssistant, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: fmt.Sprintf("row %d", i)}}
	}
	grown := func(follow bool) *List {
		list := NewList()
		list.SetStyles(DefaultStyles(true))
		list.SetWidth(24)
		list.SetHeight(10)
		list.SetItems(blocks)
		list.SetHeight(3)
		list.Render()
		list.follow = follow
		list.SetHeight(10)
		return list
	}

	for _, follow := range []bool{true, false} {
		rows := strings.Split(ansi.Strip(grown(follow).Render()), "\n")
		if !strings.Contains(rows[1], "row 5") || !strings.Contains(rows[9], "row 9") {
			t.Fatalf("follow=%t: viewport not filled to the end:\n%s", follow, strings.Join(rows, "\n"))
		}
	}

	list := grown(true)
	list.ScrollBy(-1)
	if list.Following() || !strings.Contains(ansi.Strip(list.Render()), "row 4") {
		t.Fatal("scrolling before the next render did not start from the drawn rows")
	}

	// A tall first item can still be the viewport's first visible item at
	// the bottom. Scrolling away and back must restore follow before the
	// last block grows.
	list = NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(8)
	headerRows := make([]string, 20)
	for i := range headerRows {
		headerRows[i] = fmt.Sprintf("splash row %d", i)
	}
	list.SetHeader(strings.Join(headerRows, "\n"))
	last := textBlock("last", "before")
	list.SetItems([]agent.Block{last})
	list.ScrollToBottom()
	if list.offsetIdx != 0 {
		t.Fatal("fixture needs the viewport to start inside the tall header")
	}

	list.ScrollBy(-3)
	if list.Following() {
		t.Fatal("scrolling up should stop following")
	}
	list.ScrollBy(3)
	if !list.Following() || !list.AtBottom() {
		t.Fatal("scrolling back to the visible bottom should resume following")
	}

	last.Rev++
	last.Markdown.Content = "before\nafter\ntail"
	list.SetItems([]agent.Block{last})
	if got := ansi.Strip(list.Render()); !strings.Contains(got, "tail") {
		t.Fatalf("grown block tail is hidden below the viewport:\n%s", got)
	}
	if !list.Following() || !list.AtBottom() {
		t.Fatal("growing the last block lost the bottom pin")
	}
}

// TestNoticeTextCarriesItsOwnColor is the regression guard for the notice body
// having been wrapped with a nil style: it then drew in the terminal's own
// foreground over a row this theme paints, which washed out in light mode.
func TestNoticeTextCarriesItsOwnColor(t *testing.T) {
	for _, test := range []struct {
		name   string
		isDark bool
	}{
		{name: "dark", isDark: true},
		{name: "light", isDark: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			sty := DefaultStyles(test.isDark)
			rendered := renderNotice(Notice{Level: NoticeWarn, Text: "something to report"}, 40, sty, false)

			r, g, b, _ := sty.NoticeText.GetForeground().RGBA()
			want := fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
			if !strings.Contains(rendered, want) {
				t.Errorf("notice text foreground %s missing from %q", want, rendered)
			}
		})
	}
}

func TestServerStackKeepsIndependentDisclosureOnCompletion(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(100)
	list.SetHeight(40)
	first := disclosableToolBlock("first")
	first.Tool.Title = "Searching logs"
	first.Tool.IsClientSide = false
	first.Tool.Status = agent.ToolRunning
	second := disclosableToolBlock("second")
	second.Tool.Title = "Fetched metric"
	second.Tool.IsClientSide = false
	list.SetItems([]agent.Block{first, second})
	if len(list.view) != 3 || list.gapAfter(0) != 0 {
		t.Fatal("server calls should stack tightly under a shared header")
	}
	list.ToggleDisclosure(first.ID)
	first.Rev++
	first.Tool.Title = "Searched logs"
	first.Tool.Status = agent.ToolSuccess
	list.SetItems([]agent.Block{first, second})
	if !list.expanded(first.ID) || list.expanded(second.ID) {
		t.Fatal("completion changed disclosure")
	}
	if got := strings.Join(list.renderItem(1), "\n"); !strings.Contains(got, "Searched logs") || !strings.Contains(got, "Output:") {
		t.Fatalf("completed expansion: %s", got)
	}
	if got := strings.Join(list.renderItem(2), "\n"); strings.Contains(got, "Output:") {
		t.Fatal("sibling expanded")
	}
	skill := disclosableToolBlock("skill")
	skill.Tool = &agent.ToolBlock{Name: spec.Skill, Input: `{"skill_name":"logs"}`, Status: agent.ToolSuccess}
	first.Tool.IsClientSide, second.Tool.IsClientSide = true, true
	list.SetItems([]agent.Block{first, skill, second}) // Standalone client calls bound the skill.
	if list.gapAfter(0) != 0 || list.gapAfter(1) != 0 {
		t.Fatal("standalone skill loading added padding")
	}
}

func TestToolGroupsKeepReasoningAndChildDisclosure(t *testing.T) {
	for _, family := range []struct {
		name          string
		first, second spec.Identity
		input         string
	}{
		{"Analyzing", spec.Identity{Name: "search_datadog_dashboards"}, spec.Identity{Name: "fetch_one"}, `{}`},
		{"Files", spec.ClientReadFile, spec.ClientReadFile, `{"path":"a.go"}`},
		{"Edits", spec.ClientEditFile, spec.ClientWriteFile, `{"path":"a.go"}`},
	} {
		t.Run(family.name, func(t *testing.T) {
			list := NewList()
			list.SetStyles(DefaultStyles(true))
			list.SetWidth(100)
			list.SetHeight(80)
			first, second := disclosableToolBlock("first"), disclosableToolBlock("second")
			first.Tool = &agent.ToolBlock{Name: family.first.Name, IsClientSide: family.first.ClientSide, Input: family.input, Status: agent.ToolSuccess, Output: "first output"}
			second.Tool = &agent.ToolBlock{Name: family.second.Name, IsClientSide: family.second.ClientSide, Input: family.input, Status: agent.ToolSuccess, Output: "second output"}
			reasoning := agent.Block{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "reasoning"}, Kind: assistant.KindReasoning, Thinking: &assistant.ThinkingPayload{Content: "working it out"}}
			list.SetItems([]agent.Block{first})
			list.ToggleDisclosure(first.ID)
			skill := disclosableToolBlock("skill")
			skill.Tool = &agent.ToolBlock{Name: spec.Skill, Input: `{"skill_name":"logs"}`, Status: agent.ToolSuccess}
			leading := skill
			leading.ID.Key = "leading-skill"
			blocks := []agent.Block{leading, first, reasoning, skill, second}
			list.SetItems(blocks)
			if len(list.view) != 6 || list.view[0].kind != itemToolGroup || !list.expanded(first.ID) {
				t.Fatal("group lost its children or existing disclosure")
			}
			if _, ok := list.AccordionAt(0); ok {
				t.Fatal("group header must not be collapsible")
			}
			got := ansi.Strip(list.Document())
			for _, want := range []string{family.name, "Reasoning", "first output", "Loaded skill logs"} {
				if !strings.Contains(got, want) {
					t.Fatalf("missing %q:\n%s", want, got)
				}
			}
			if family.name == "Edits" && !strings.Contains(got, "2 changes across 1 file") {
				t.Fatalf("edit file count:\n%s", got)
			}
			if !list.HasAnimated() {
				t.Fatal("unfinished reasoning should keep group active")
			}
			list.ToggleDisclosure(first.ID)
			list.ToggleDisclosure(second.ID)
			if !list.expanded(second.ID) || list.expanded(first.ID) {
				t.Fatal("child disclosure must remain independent")
			}
			blocks[2].Complete = true
			blocks[2].Rev++
			list.SetItems(blocks)
			if list.HasAnimated() || !list.expanded(second.ID) {
				t.Fatal("completion changed disclosure or retained animation")
			}
			if family.name == "Analyzing" && !strings.Contains(ansi.Strip(list.Document()), "Analyzed · 4 calls") {
				t.Fatal("completed server group should say Analyzed")
			}
			list.ToggleAllDisclosure()
			if got := ansi.Strip(list.Document()); !strings.Contains(got, "working it out") {
				t.Fatalf("reasoning did not expand:\n%s", got)
			}
			list.ToggleAllDisclosure()
			if len(list.view) != 6 {
				t.Fatal("collapsing details must not hide group children")
			}
			for _, width := range []int{1, 8, 40} {
				list.SetWidth(width)
				for _, row := range strings.Split(list.Document(), "\n") {
					if ansi.StringWidth(row) > width {
						t.Fatalf("row exceeds %d cells: %q", width, row)
					}
				}
			}
		})
	}
}

func TestServerGroupingBoundaries(t *testing.T) {
	first := disclosableToolBlock("first")
	first.Tool.Name, first.Tool.Namespace, first.Tool.IsClientSide = "search_datadog_logs", nil, false
	last := disclosableToolBlock("last")
	last.Tool.Name, last.Tool.Namespace, last.Tool.IsClientSide = "search_datadog_docs", nil, false
	for _, tc := range []struct {
		name    string
		tool    agent.ToolBlock
		grouped bool
	}{
		// Backend naming conventions do not affect generic grouping.
		{"same family", agent.ToolBlock{Name: "search_datadog_metrics", Input: `{"telemetry":{"intent":"Discover what metrics exist in the org for an overview"},"max_tokens":4000}`}, true},
		{"metric wire name", agent.ToolBlock{Name: "get_datadog_metric"}, true},
		{"fetch wire name", agent.ToolBlock{Name: "fetch_one"}, true},
		{"new server tool", agent.ToolBlock{Name: "future_backend_tool"}, true},
		{"logs wire name", agent.ToolBlock{Name: "analyze_datadog_logs"}, true},
		{"web catalog alias", agent.ToolBlock{Name: "mcp_datadog_search_datadog_metrics"}, true},
		{"code explorer", agent.ToolBlock{Name: "mcp_code-explorer_View"}, true},
		{"server mutation", agent.ToolBlock{Name: "update_monitor"}, true},
		{"client", agent.ToolBlock{Name: spec.ReadFile, IsClientSide: true}, false},
		{"namespace", agent.ToolBlock{Name: "search_datadog_logs", Namespace: new("other")}, true},
		{"approval", agent.ToolBlock{Name: "search_datadog_logs", Status: agent.ToolAwaitingApproval}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			middle := agent.Block{ID: agent.BlockID{Scope: agent.ScopeTool, Key: "middle"}, Kind: assistant.KindToolCall, Tool: &tc.tool}
			view := buildPresentation("", []agent.Block{first, middle, last})
			want := 3
			if tc.grouped {
				want = 4
			}
			if len(view) != want {
				t.Fatalf("got %d items, want %d", len(view), want)
			}
		})
	}
}
