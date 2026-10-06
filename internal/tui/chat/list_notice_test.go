package chat

import (
	"errors"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
)

func TestLocalNoticesStayBetweenAgentBlocks(t *testing.T) {
	l := NewList()
	l.SetWidth(32)
	l.SetHeight(10)
	first := textBlock("a", "FIRST BLOCK")
	second := textBlock("b", "SECOND BLOCK")
	n := NoticeItem{ID: 1, After: 1, Notice: Notice{
		Level: NoticeWarn, Text: "A warning that wraps across more than one line", Err: errors.New("private backend detail"),
	}}
	l.SetTranscript([]agent.Block{first}, []NoticeItem{n})
	l.SetTranscript([]agent.Block{first, second}, []NoticeItem{n})
	doc := ansi.Strip(l.Document())
	if a, b, c := strings.Index(doc, "FIRST BLOCK"), strings.Index(doc, "A warning"), strings.Index(doc, "SECOND BLOCK"); a < 0 || b <= a || c <= b {
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
