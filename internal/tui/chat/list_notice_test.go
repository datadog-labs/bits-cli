package chat

import (
	"errors"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/browser"
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

// A task's blocks show after the conversation block each follows; one whose
// conversation block is not shown goes last.
func TestTaskBlocksFollowTheirConversationBlocks(t *testing.T) {
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
	meanwhile := textBlock("b", "MEANWHILE")
	gone := agent.BlockID{Scope: agent.ScopeMessage, Key: "gone"}
	l.SetTranscript(
		[]agent.Block{textBlock("a", "LAUNCHING"), start, meanwhile, textBlock("c", "LATER")},
		[]agent.Task{
			{Blocks: []agent.Block{update("u3", "ORPHAN")}, Places: []agent.Place{{After: gone, Arrival: 3}}},
			{Blocks: []agent.Block{update("u1", "GATHERING"), update("u2", "FOUND")}, Places: []agent.Place{{After: start.ID, Arrival: 1}, {After: meanwhile.ID, Arrival: 2}}},
		},
		nil,
	)
	doc := ansi.Strip(l.Document())
	order := []string{"LAUNCHING", "GATHERING", "MEANWHILE", "FOUND", "LATER", "ORPHAN"}
	for i := 1; i < len(order); i++ {
		if !strings.Contains(doc, order[i-1]) || strings.Index(doc, order[i]) <= strings.Index(doc, order[i-1]) {
			t.Fatalf("blocks are not in order %q:\n%s", order, doc)
		}
	}
}

// A link in a task update's markdown is a link, like one in assistant text,
// and an app route follows the web origin, even in blocks a render settled.
func TestTaskUpdateLinksAreLinks(t *testing.T) {
	l := NewList()
	l.SetWidth(60)
	l.SetHeight(10)
	update := agent.Block{
		ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "u1", Kind: assistant.KindProgress}, Role: assistant.RoleAssistant,
		Kind: assistant.KindProgress, Complete: true,
		Progress: &assistant.ProgressPayload{Content: "I'm launching an **[investigation](/inv/1?tab=a#b)**\n\nStep one.\n\nStep two.\n\nStep three."},
	}
	l.SetTranscript([]agent.Block{update}, nil, nil)
	for _, tc := range []struct{ origin, want string }{
		{"", "/inv/1?tab=a#b"},
		{"https://acme.datadoghq.eu", "https://acme.datadoghq.eu/inv/1?tab=a#b"},
	} {
		l.SetLinks(browser.Links{Origin: tc.origin})
		var hit LinkHit
	scan:
		for y := range 10 {
			for x := range 60 {
				if h, ok := l.LinkAt(x, y); ok {
					hit = h
					break scan
				}
			}
		}
		if hit.URL != tc.want || hit.BlockID != update.ID {
			t.Fatalf("origin %q: link = %+v, want %q in:\n%s", tc.origin, hit, tc.want, ansi.Strip(l.Document()))
		}
	}
}

func TestNoticeRowsUseEditorBackground(t *testing.T) {
	for _, isDark := range []bool{true, false} {
		sty := DefaultStyles(isDark)
		for _, level := range []NoticeLevel{NoticeInfo, NoticeWarn, NoticeError, NoticeFork} {
			rows := strings.Split(renderNotice(Notice{Level: level, Text: "A notice that wraps onto another line"}, 20, sty), "\n")
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

func TestForkMarkerWithMissingAnchorIsOmitted(t *testing.T) {
	l := headerList(t, 60, 10)
	l.SetTranscript([]agent.Block{textBlock("new", "CURRENT")}, nil, []NoticeItem{{After: agent.BlockID{Scope: agent.ScopeMessage, Key: "missing"}, Notice: Notice{Level: NoticeFork, Text: "Forked conversation"}}})
	doc := l.Document()
	if strings.Contains(doc, "Forked conversation") || !strings.Contains(doc, "CURRENT") {
		t.Fatalf("invented marker: %s", doc)
	}
}
