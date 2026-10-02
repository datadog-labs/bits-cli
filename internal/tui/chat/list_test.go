package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/components"
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
	gutter := strings.Repeat(" ", list.gutterWidth)
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

	// Use the same grouped presentation to check narrow rows and span colors.
	for _, test := range []struct {
		name        string
		input       string
		bodyWidth   int
		wantRows    []string
		firstPrefix bool
		wantMuted   string
		wantAccent  string
	}{
		{
			name: "wrapped search context", bodyWidth: 35,
			input:       `{"pattern":"NewToolSet\\(|ClientTools\\(|Definition:|Name:.*navigate|navigate|ask_user_question","path":"internal"}`,
			wantRows:    []string{"search NewToolSet", "in internal"},
			firstPrefix: true,
			wantMuted:   "in",
			wantAccent:  "internal",
		},
		{
			name: "argument continuation", bodyWidth: 6,
			input:      `{"pattern":"alpha beta gamma","path":"."}`,
			wantRows:   []string{"search alpha", "beta", "gamma"},
			wantAccent: "gamma",
		},
		{
			name: "hyphen break", bodyWidth: 7,
			input:      `{"pattern":"foo -bar baz","path":"."}`,
			wantRows:   []string{"search foo -", "bar baz"},
			wantAccent: "bar baz",
		},
		{
			name: "exact width ASCII", bodyWidth: 1,
			input:      `{"pattern":"a b","path":"."}`,
			wantRows:   []string{"search a", "b"},
			wantAccent: "b",
		},
		{
			name: "exact width wide rune", bodyWidth: 2,
			input:      `{"pattern":"a 界","path":"."}`,
			wantRows:   []string{"search a", "界"},
			wantAccent: "界",
		},
	} {
		for _, dark := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/dark=%t", test.name, dark), func(t *testing.T) {
				sty := DefaultStyles(dark)
				list := NewList()
				list.SetStyles(sty)
				// Child indentation and "search " take 11 cells before the body.
				list.SetWidth(test.bodyWidth + 11)
				list.SetItems([]agent.Block{
					inspectBlock("read", "read_file", `{"path":"a.go"}`, agent.ToolSuccess, "contents"),
					inspectBlock("search", "grep_files", test.input, agent.ToolSuccess, "matches"),
				})
				rows := list.renderItem(0)
				if len(rows) != 2+len(test.wantRows) {
					t.Fatalf("group rows = %q, want %d rows", rows, 2+len(test.wantRows))
				}
				for i, want := range test.wantRows {
					got := strings.TrimSpace(ansi.Strip(rows[2+i]))
					matches := got == want
					if i == 0 && test.firstPrefix {
						matches = strings.HasPrefix(got, want)
					}
					if !matches {
						t.Errorf("search row %d = %q, want %q", i, got, want)
					}
				}
				last := rows[len(rows)-1]
				argument, _, _ := strings.Cut(sty.ToolArgument.Render(test.wantAccent), test.wantAccent)
				if !strings.Contains(last, argument+test.wantAccent) {
					t.Errorf("last row lost argument color: %q", last)
				}
				if test.wantMuted != "" {
					muted, _, _ := strings.Cut(sty.ToolDetail.Render(test.wantMuted), test.wantMuted)
					if !strings.Contains(last, muted+test.wantMuted) {
						t.Errorf("last row lost muted color: %q", last)
					}
				}
			})
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

			gutter := strings.Repeat(" ", list.gutterWidth)
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
	if got := strings.Count(plain, "✓ reasoning"); got != 1 {
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
	if !strings.Contains(plain, "thinking.") || strings.Contains(plain, "✓ reasoning") {
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

// TestGutterReservation: only a single tool reserves the accordion gutter,
// and only when the viewport leaves room for content beside it.
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
		{"reasoning group", reasoning, 80, 0},
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

	if height := list.itemHeight(0); height != 1 {
		t.Fatalf("header-only tool rendered %d lines, want 1", height)
	}
	if _, ok := list.HeaderAt(0); ok {
		t.Fatal("header-only tool is clickable")
	}
	lines := list.renderItem(0)
	gutter := strings.Repeat(" ", list.gutterWidth)
	// The gutter sits right after the fixed-width status glyph, not before it,
	// so the glyph stays the leftmost cell on the row.
	if got := ansi.Strip(ansi.Cut(lines[0], statusGlyphWidth, statusGlyphWidth+list.gutterWidth)); got != gutter {
		t.Fatalf("header-only tool did not reserve the gutter after the status glyph: %q", lines[0])
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
		block := toolBlockOf(agent.ToolSuccess)
		block.ID = agent.BlockID{Scope: agent.ScopeTool, Key: key}
		blocks[i] = block
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

func TestHeaderAtOnlyMatchesAVisibleHeaderRow(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	list.ScrollToTop() // drop follow so the header starts visible
	list.SetHeight(2)
	if id, ok := list.HeaderAt(0); !ok || id != list.view[0].id {
		t.Fatalf("HeaderAt(0) = %v, %t, want the tool's header", id, ok)
	}
	if _, ok := list.HeaderAt(1); ok {
		t.Fatal("HeaderAt matched a detail row")
	}
	if _, ok := list.HeaderAt(2); ok {
		t.Fatal("HeaderAt matched a row outside the viewport")
	}

	list.SetHeight(1)
	list.ScrollBy(1)
	if _, ok := list.HeaderAt(0); ok {
		t.Fatal("HeaderAt matched after the header scrolled off the top")
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

// TestToggleAllDisclosure: the first ctrl+o collapses, the next expands, and
// each press discards individual toggles.
func TestToggleAllDisclosure(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	full := list.itemHeight(0)

	list.ToggleDisclosure(list.view[0].id) // collapsed individually
	list.ToggleAllDisclosure()
	if got := list.itemHeight(0); got != 1 {
		t.Fatalf("collapse-all height = %d, want 1", got)
	}
	list.ToggleAllDisclosure()
	if got := list.itemHeight(0); got != full {
		t.Fatalf("expand-all height = %d, want %d (individual collapse discarded)", got, full)
	}
}

func TestCollapseAllAppliesToBlocksArrivingLater(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	list.ToggleAllDisclosure()

	later := toolBlockOf(agent.ToolSuccess)
	later.ID = agent.BlockID{Scope: agent.ScopeTool, Key: "call-2"}
	list.SetItems(append(list.items, later))
	if got := list.itemHeight(1); got != 1 {
		t.Fatalf("a block arriving after collapse-all rendered %d lines, want 1", got)
	}
}

func TestResetClearsDisclosureState(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	full := list.itemHeight(0)
	blocks := list.items
	for _, collapse := range []func(){
		list.ToggleAllDisclosure,
		func() { list.ToggleDisclosure(list.view[0].id) },
	} {
		collapse()
		list.Reset()
		list.SetItems(blocks)
		if got := list.itemHeight(0); got != full {
			t.Fatalf("after Reset height = %d, want the default expanded %d", got, full)
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
	list.SetHeight(3)
	blocks := make([]agent.Block, 0, 4)
	for i := range 4 {
		block := toolBlockOf(agent.ToolSuccess)
		block.ID = agent.BlockID{Scope: agent.ScopeTool, Key: fmt.Sprintf("call-%d", i)}
		blocks = append(blocks, block)
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
}
