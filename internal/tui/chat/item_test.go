package chat

import (
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

func TestItemIDOf(t *testing.T) {
	text := assistant.AssistantMessage("m1", assistant.TextContent("hi"))
	thinking := assistant.AssistantMessage("m1", assistant.ThinkingContent("why"))
	if a, b := ItemIDOf(text), ItemIDOf(thinking); a == b {
		t.Errorf("text and thinking from one message must key separately, both %s", a)
	}
	if got := ItemIDOf(text); got != (ItemID{Scope: ScopeMessage, Key: "m1", Kind: assistant.KindText}) {
		t.Errorf("ItemIDOf(text) = %s", got)
	}

	// A call and its result arrive on different messages but share the tool call
	// id, so they must key to the same block.
	call := assistant.AssistantMessage("m2", assistant.ToolCallContent("tc1", "run_bash", ""))
	result := assistant.AssistantMessage("m3",
		assistant.ToolResultContent("tc1", "", assistant.ToolStatusSuccess, "ok"))
	if a, b := ItemIDOf(call), ItemIDOf(result); a != b {
		t.Errorf("call/result must share a key: %s vs %s", a, b)
	}

	bare := assistant.AssistantMessage("m4", assistant.Content{Type: assistant.ContentToolCall})
	if got := ItemIDOf(bare); got != (ItemID{Scope: ScopeTool, Key: "m4"}) {
		t.Errorf("ItemIDOf(call with no tool call id) = %s, want tool:m4", got)
	}
}

func TestItemIDOf_ScopesDoNotCollide(t *testing.T) {
	msg := ItemID{Scope: ScopeMessage, Key: "x"}
	if msg == (ItemID{Scope: ScopeTool, Key: "x"}) || msg == (ItemID{Scope: ScopeLocal, Key: "x"}) {
		t.Error("scopes must namespace the key")
	}
}

func TestToolViewOf(t *testing.T) {
	tv := ToolViewOf(&assistant.ToolPayload{
		Status:   "success",
		Metadata: &assistant.ToolMetadata{Name: "run_bash", Input: `{"cmd":"ls"}`, Output: "a"},
	})
	if tv.Name != "run_bash" || tv.Input != `{"cmd":"ls"}` || tv.Output != "a" || tv.Status != ToolSuccess {
		t.Errorf("ToolViewOf = %+v", tv)
	}

	// tool_call_started has no metadata; the name comes from ToolName.
	started := ToolViewOf(&assistant.ToolPayload{ToolCallID: "tc1", ToolName: "search"})
	if started.Name != "search" {
		t.Errorf("name = %q, want search from ToolName", started.Name)
	}
	if started.Status != ToolUnknown {
		t.Errorf("status = %v, want ToolUnknown so UpsertTool marks it running", started.Status)
	}

	if got := ToolViewOf(nil); got != (ToolView{}) {
		t.Errorf("ToolViewOf(nil) = %+v, want zero", got)
	}
}
