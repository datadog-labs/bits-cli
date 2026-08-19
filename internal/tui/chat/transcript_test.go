package chat

import (
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// msgID and toolID build keys directly; ItemIDOf covers deriving them from a
// message.
func msgID(key string, kind assistant.ContentKind) ItemID {
	return ItemID{Scope: ScopeMessage, Key: key, Kind: kind}
}

func toolID(key string) ItemID { return ItemID{Scope: ScopeTool, Key: key} }

func TestAppendText_ConcatenatesSameID(t *testing.T) {
	tr := NewTranscript()
	tr.appendText(msgID("m1", assistant.KindText), assistant.RoleAssistant, assistant.KindText, "Hel")
	tr.appendText(msgID("m1", assistant.KindText), assistant.RoleAssistant, assistant.KindText, "lo")

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

// One message that reasons and then answers must yield two blocks: the keys
// differ only by Kind.
func TestAppendText_SameMessageDifferentKindsKeepDistinctItems(t *testing.T) {
	tr := NewTranscript()
	tr.appendText(msgID("m1", assistant.KindReasoning), assistant.RoleAssistant, assistant.KindReasoning, "why")
	tr.appendText(msgID("m1", assistant.KindText), assistant.RoleAssistant, assistant.KindText, "answer")

	items := tr.Items()
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Kind != assistant.KindReasoning || items[1].Kind != assistant.KindText {
		t.Errorf("kinds not preserved: %v, %v", items[0].Kind, items[1].Kind)
	}
}

func TestAppendText_EmptyDeltaIsNoOp(t *testing.T) {
	tr := NewTranscript()
	id := msgID("m1", assistant.KindText)

	// The stream's final fragment is empty (it carries usage): no blank block.
	tr.appendText(id, assistant.RoleAssistant, assistant.KindText, "")
	if got := len(tr.Items()); got != 0 {
		t.Fatalf("empty delta created %d items, want 0", got)
	}

	// Nor a version bump on an existing item, which would force a re-render.
	tr.appendText(id, assistant.RoleAssistant, assistant.KindText, "hi")
	v0 := tr.Items()[0].Version
	tr.appendText(id, assistant.RoleAssistant, assistant.KindText, "")
	it := tr.Items()[0]
	if it.Text != "hi" || it.Version != v0 {
		t.Errorf("item = (%q, v%d), want (%q, v%d) unchanged", it.Text, it.Version, "hi", v0)
	}
}

func TestAppendText_UnknownKindStored(t *testing.T) {
	tr := NewTranscript()
	tr.appendText(msgID("x", assistant.KindUnknown), assistant.RoleAssistant, assistant.KindUnknown, "?")
	if got := tr.Items()[0].Kind; got != assistant.KindUnknown {
		t.Errorf("kind = %v, want KindUnknown", got)
	}
}

func TestUpsertTool_CreateThenMerge(t *testing.T) {
	tr := NewTranscript()
	tr.upsertTool(toolID("1"), ToolView{Name: "run_bash", Input: `{"cmd":"ls"}`})

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
	tr.upsertTool(toolID("1"), ToolView{Output: "a\nb", Status: ToolSuccess})

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
	tr.appendText(msgID("m1", assistant.KindText), assistant.RoleAssistant, assistant.KindText, "hi")
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
	dup := msgID("dup", assistant.KindText)
	tr.push(Item{ID: dup})

	defer func() {
		if recover() == nil {
			t.Errorf("push of duplicate id should panic")
		}
	}()
	tr.push(Item{ID: dup}) // programmer error: must panic
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
		t.Errorf("user ids not unique: both %s", items[0].ID)
	}
	if items[0].Role != assistant.RoleUser {
		t.Errorf("role = %v, want RoleUser", items[0].Role)
	}
}
