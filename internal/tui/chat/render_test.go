package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// plain strips styling so assertions test content/layout, not ANSI codes.
func plain(s string) string { return ansi.Strip(s) }

func TestItem_AssistantTextShowsContent(t *testing.T) {
	it := Item{Kind: assistant.KindText, Role: assistant.RoleAssistant, Text: "hello world"}
	if got := plain(it.Render(80, DefaultStyles(true))); !strings.Contains(got, "hello world") {
		t.Errorf("output %q missing text", got)
	}
}

func TestItem_TextWrapsWithinWidth(t *testing.T) {
	it := Item{Kind: assistant.KindText, Role: assistant.RoleAssistant, Text: "aaaa bbbb cccc dddd"}
	got := plain(it.Render(9, DefaultStyles(true)))
	if lines := strings.Count(got, "\n") + 1; lines < 2 {
		t.Fatalf("expected wrap into >=2 lines, got %d: %q", lines, got)
	}
	for _, ln := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(ln); w > 9 {
			t.Errorf("line exceeds width 9: %q (%d)", ln, w)
		}
	}
}

func TestItem_UserHasMarkerAndText(t *testing.T) {
	it := Item{Kind: assistant.KindText, Role: assistant.RoleUser, Text: "do the thing"}
	got := plain(it.Render(80, DefaultStyles(true)))
	if !strings.HasPrefix(got, "›") {
		t.Errorf("user line should start with marker, got %q", got)
	}
	if !strings.Contains(got, "do the thing") {
		t.Errorf("missing text: %q", got)
	}
}

func TestItem_ReasoningShowsText(t *testing.T) {
	it := Item{Kind: assistant.KindReasoning, Role: assistant.RoleAssistant, Text: "let me think"}
	if got := plain(it.Render(80, DefaultStyles(true))); !strings.Contains(got, "let me think") {
		t.Errorf("missing reasoning text: %q", got)
	}
}

func TestItem_ToolBlockShowsNameStatusInputOutput(t *testing.T) {
	it := Item{
		Kind: assistant.KindToolCall,
		Tool: ToolView{
			Name:   "run_bash",
			Input:  `{"command":"ls"}`,
			Output: "a\nb",
			Status: ToolSuccess,
		},
	}
	got := plain(it.Render(80, DefaultStyles(true)))
	for _, want := range []string{"run_bash", "success", "ls", "a", "b"} {
		if !strings.Contains(got, want) {
			t.Errorf("tool output %q missing %q", got, want)
		}
	}
}

func TestItem_ToolOutputTruncated(t *testing.T) {
	it := Item{
		Kind: assistant.KindToolCall,
		Tool: ToolView{Name: "x", Output: strings.Repeat("line\n", 50), Status: ToolSuccess},
	}
	got := plain(it.Render(80, DefaultStyles(true)))
	if !strings.Contains(got, "more") {
		t.Errorf("expected truncation marker, got %q", got)
	}
	if lines := strings.Count(got, "\n") + 1; lines > toolOutputMaxLines+3 {
		t.Errorf("output not clamped: %d lines", lines)
	}
}

func TestItem_FallbackForDeferredKinds(t *testing.T) {
	for _, k := range []assistant.ContentKind{
		assistant.KindWidget, assistant.KindDashboard, assistant.KindUnknown,
	} {
		it := Item{Kind: k}
		got := plain(it.Render(80, DefaultStyles(true)))
		if want := "[" + k.String() + "]"; !strings.Contains(got, want) {
			t.Errorf("kind %v: fallback %q missing %q", k, got, want)
		}
	}
}
