package chat

import (
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

func TestAppendText_ConcatenatesSameID(t *testing.T) {
	tr := NewTranscript()
	tr.AppendText("m1", assistant.RoleAssistant, assistant.KindText, "Hel")
	tr.AppendText("m1", assistant.RoleAssistant, assistant.KindText, "lo")

	items := tr.Items()
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Text != "Hello" {
		t.Errorf("text = %q, want %q", items[0].Text, "Hello")
	}
	if items[0].Version != 1 {
		t.Errorf("version = %d, want 1 after one concat", items[0].Version)
	}
	if !items[0].Streaming {
		t.Errorf("streaming = false, want true before finalize")
	}
}

func TestAppendText_SeparateIDsKeepDistinctItems(t *testing.T) {
	tr := NewTranscript()
	tr.AppendText("think:1", assistant.RoleAssistant, assistant.KindReasoning, "why")
	tr.AppendText("msg:1", assistant.RoleAssistant, assistant.KindText, "answer")

	items := tr.Items()
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Kind != assistant.KindReasoning || items[1].Kind != assistant.KindText {
		t.Errorf("kinds not preserved: %v, %v", items[0].Kind, items[1].Kind)
	}
}

func TestAppendText_UnknownKindStored(t *testing.T) {
	tr := NewTranscript()
	tr.AppendText("x", assistant.RoleAssistant, assistant.KindUnknown, "?")
	if got := tr.Items()[0].Kind; got != assistant.KindUnknown {
		t.Errorf("kind = %v, want KindUnknown", got)
	}
}

func TestUpsertTool_CreateThenMerge(t *testing.T) {
	tr := NewTranscript()
	tr.UpsertTool("tool:1", ToolView{Name: "run_bash", Input: `{"cmd":"ls"}`})

	items := tr.Items()
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Kind != assistant.KindToolCall {
		t.Errorf("kind = %v, want KindToolCall", items[0].Kind)
	}
	if items[0].Tool.Status != ToolRunning {
		t.Errorf("status = %v, want ToolRunning on create", items[0].Tool.Status)
	}

	// Result arrives on the same ID: merge output+status, keep input, one item.
	tr.UpsertTool("tool:1", ToolView{Output: "a\nb", Status: ToolSuccess})

	items = tr.Items()
	if len(items) != 1 {
		t.Fatalf("result must merge into same item, got %d items", len(items))
	}
	got := items[0].Tool
	if got.Input != `{"cmd":"ls"}` {
		t.Errorf("input = %q, want kept from call", got.Input)
	}
	if got.Output != "a\nb" || got.Status != ToolSuccess {
		t.Errorf("merge = %+v, want output/success set", got)
	}
	if items[0].Version != 1 {
		t.Errorf("version = %d, want 1 after merge", items[0].Version)
	}
}

func TestFinalizeAll_ClearsStreamingAndBumpsVersionOnce(t *testing.T) {
	tr := NewTranscript()
	tr.AppendText("m1", assistant.RoleAssistant, assistant.KindText, "hi")
	v0 := tr.Items()[0].Version

	tr.FinalizeAll()
	it := tr.Items()[0]
	if it.Streaming {
		t.Errorf("streaming not cleared")
	}
	if it.Version != v0+1 {
		t.Errorf("version = %d, want %d after finalize", it.Version, v0+1)
	}

	// Idempotent: a second finalize must not bump versions again.
	tr.FinalizeAll()
	if got := tr.Items()[0].Version; got != v0+1 {
		t.Errorf("version = %d, want unchanged on second finalize", got)
	}
}

func TestToolStatusOf(t *testing.T) {
	cases := map[string]ToolStatus{
		"running": ToolRunning,
		"success": ToolSuccess,
		"error":   ToolError,
		"":        ToolUnknown,
		"weird":   ToolUnknown,
	}
	for in, want := range cases {
		if got := ToolStatusOf(in); got != want {
			t.Errorf("ToolStatusOf(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPush_DuplicateIDPanics(t *testing.T) {
	tr := NewTranscript()
	tr.push(Item{ID: "dup"})

	defer func() {
		if recover() == nil {
			t.Errorf("push of duplicate id should panic")
		}
	}()
	tr.push(Item{ID: "dup"}) // programmer error: must panic
}

func TestAppendUser_UniqueIDsAndRole(t *testing.T) {
	tr := NewTranscript()
	tr.AppendUser("first")
	tr.AppendUser("second")

	items := tr.Items()
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].ID == items[1].ID {
		t.Errorf("user ids not unique: both %q", items[0].ID)
	}
	if items[0].Role != assistant.RoleUser {
		t.Errorf("role = %v, want RoleUser", items[0].Role)
	}
}
