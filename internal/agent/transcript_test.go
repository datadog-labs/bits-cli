package agent

import (
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// Text fragments sharing a message id concatenate into a single block.
func TestTextFragmentsConcatenate(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(assistant.AssistantMessage("m1", assistant.TextContent("Hello")))
	tr.AppendMessage(assistant.AssistantMessage("m1", assistant.TextContent(" world")))

	blocks := tr.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if got := blocks[0].Markdown.Content; got != "Hello world" {
		t.Fatalf("content = %q, want %q", got, "Hello world")
	}
}

// A tool_call and its tool_response merge into one block: name/input from the
// call, output and decoded status from the result.
func TestToolCallMergesWithResult(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(assistant.AssistantMessage("mc", assistant.ToolCallContent("tc1", "search", `{"q":"x"}`)))
	tr.AppendMessage(assistant.AssistantMessage("mr", assistant.ToolResultContent("tc1", "", assistant.ToolStatusSuccess, "result")))

	blocks := tr.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	tool := blocks[0].Tool
	if tool == nil {
		t.Fatal("Tool payload is nil")
	}
	if tool.Name != "search" || tool.Input != `{"q":"x"}` || tool.Output != "result" {
		t.Fatalf("merged tool = %+v", tool)
	}
	if tool.Status != ToolSuccess {
		t.Fatalf("status = %v, want ToolSuccess", tool.Status)
	}
}

// A client tool persists its result as a client_tool_response in restored
// history. It must merge into the client_tool_call block.
func TestClientToolResponseMergesWithCall(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(assistant.AssistantMessage("mc", assistant.ToolCallContent("tc1", "show_content", "{\"mode\":\"html\"}")))
	call := tr.Blocks()[0]
	if call.Tool == nil || call.Tool.Status != ToolRunning {
		t.Fatalf("client tool call should start running, got %+v", call.Tool)
	}

	result := assistant.Message{
		Role: "user",
		Content: assistant.Content{
			Type: assistant.ContentClientToolResponse,
			Tool: &assistant.ToolPayload{
				ToolCallID: "tc1",
				Status:     string(assistant.ToolStatusSuccess),
				Metadata:   &assistant.ToolMetadata{Output: "ok"},
			},
		},
	}
	tr.AppendMessage(result)

	blocks := tr.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1 (call and response share a block)", len(blocks))
	}
	tool := blocks[0].Tool
	if tool.Status != ToolSuccess {
		t.Fatalf("status = %v, want ToolSuccess (result never merged)", tool.Status)
	}
	if tool.Output != "ok" || tool.Name != "show_content" {
		t.Fatalf("merged client tool = %+v", tool)
	}
}

// A snapshot taken before a later fold must not observe that fold: payloads are
// replaced, never mutated in place. This is what keeps Engine.snapshot safe.
func TestSnapshotImmutableAcrossFolds(t *testing.T) {
	tr := NewTranscript()
	first, _ := tr.AppendMessage(assistant.AssistantMessage("m1", assistant.TextContent("Hello")))
	tr.AppendMessage(assistant.AssistantMessage("m1", assistant.TextContent(" world")))

	if got := first.Markdown.Content; got != "Hello" {
		t.Fatalf("earlier block content = %q, want %q (later fold leaked)", got, "Hello")
	}
}

// Reasoning takes its own path and preserves the Redacted flag rather than
// flattening into plain text.
func TestReasoningPreservesRedacted(t *testing.T) {
	tr := NewTranscript()
	c := assistant.ThinkingContent("")
	c.Thinking.Redacted = true
	b, ok := tr.AppendMessage(assistant.AssistantMessage("m1", c))
	if !ok {
		t.Fatal("redacted-but-empty thinking produced no block")
	}
	if b.Kind != assistant.KindReasoning {
		t.Fatalf("kind = %v, want KindReasoning", b.Kind)
	}
	if b.Thinking == nil || !b.Thinking.Redacted {
		t.Fatalf("Redacted not preserved: %+v", b.Thinking)
	}
}

// A streamed block is finalized as soon as a fragment lands on a different
// block id, rather than staying open until FinalizeAll at turn end.
func TestPriorStreamFinalizedOnNewBlock(t *testing.T) {
	tr := NewTranscript()
	think, _ := tr.AppendMessage(assistant.AssistantMessage("m1", assistant.ThinkingContent("hmm")))
	if think.Complete {
		t.Fatal("reasoning block should start incomplete")
	}
	tr.AppendMessage(assistant.AssistantMessage("m2", assistant.TextContent("answer")))

	blocks := tr.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	if !blocks[0].Complete {
		t.Fatal("reasoning block not finalized after a different block started")
	}
	if blocks[1].Complete {
		t.Fatal("newest streaming block should stay open until something follows it")
	}
}

// Consecutive fragments on the same id keep the block open (still streaming).
func TestSameIdFragmentsStayOpen(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(assistant.AssistantMessage("m1", assistant.ThinkingContent("a")))
	tr.AppendMessage(assistant.AssistantMessage("m1", assistant.ThinkingContent("b")))
	if tr.Blocks()[0].Complete {
		t.Fatal("block should stay open while its own fragments stream")
	}
}

// A tool call after reasoning closes the reasoning block and bumps its revision
// so a per-revision render cache refreshes the status glyph.
func TestToolCallFinalizesPriorReasoning(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(assistant.AssistantMessage("m1", assistant.ThinkingContent("plan")))
	before := tr.Blocks()[0].Rev
	tr.AppendMessage(assistant.AssistantMessage("mc", assistant.ToolCallContent("tc1", "search", "{}")))
	got := tr.Blocks()[0]
	if !got.Complete {
		t.Fatal("reasoning not finalized when tool call started")
	}
	if got.Rev == before {
		t.Fatal("Rev not bumped on implicit finalize; cached render would not refresh")
	}
}

// Interleaved streaming self-corrects: a fragment resuming a block that
// closePrior optimistically finalized reopens it, rather than leaving it marked
// done while still growing.
func TestInterleavedStreamReopens(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(assistant.AssistantMessage("m1", assistant.TextContent("A1")))
	tr.AppendMessage(assistant.AssistantMessage("m2", assistant.TextContent("B1"))) // closes m1
	if !tr.Blocks()[0].Complete {
		t.Fatal("m1 should be closed once m2 started")
	}
	tr.AppendMessage(assistant.AssistantMessage("m1", assistant.TextContent("A2"))) // resumes m1

	if b := tr.Blocks()[0]; b.Complete {
		t.Fatal("resumed m1 should not be marked complete")
	} else if b.Markdown.Content != "A1A2" {
		t.Fatalf("content = %q, want %q", b.Markdown.Content, "A1A2")
	}
	if !tr.Blocks()[1].Complete {
		t.Fatal("m2 should be closed once m1 resumed")
	}
}

// A user message closes the open assistant stream that preceded it, e.g. across
// turns in restored history.
func TestUserMessageFinalizesPriorStream(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(assistant.AssistantMessage("m1", assistant.TextContent("partial")))
	tr.AppendUser("next turn")
	if !tr.Blocks()[0].Complete {
		t.Fatal("assistant block not finalized when a user message followed it")
	}
}

// Widget/dashboard/progress kinds now produce blocks instead of being dropped.
func TestPassthroughKindProducesBlock(t *testing.T) {
	tr := NewTranscript()
	c := assistant.Content{Type: assistant.ContentWidget, Widget: &assistant.WidgetPayload{Title: "CPU"}}
	b, ok := tr.AppendMessage(assistant.AssistantMessage("m1", c))
	if !ok {
		t.Fatal("widget produced no block")
	}
	if b.Kind != assistant.KindWidget || b.Widget == nil || b.Widget.Title != "CPU" {
		t.Fatalf("widget block = %+v", b)
	}
}
