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
		for _, level := range []NoticeLevel{NoticeInfo, NoticeWarn, NoticeError} {
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
