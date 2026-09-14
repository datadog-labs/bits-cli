package agent

import (
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

func streamedToolMessage(messageID, contentType, toolCallID, toolName, partialJSON, input string) assistant.Message {
	return assistant.AssistantMessage(messageID, assistant.Content{
		Type: contentType,
		Tool: &assistant.ToolPayload{
			ToolCallID:  toolCallID,
			ToolName:    toolName,
			PartialJSON: partialJSON,
			Metadata:    &assistant.ToolMetadata{Input: input, Name: toolName},
		},
	})
}

func streamedToolDelta(messageID, toolCallID, partialJSON string) assistant.Message {
	return assistant.AssistantMessage(messageID, assistant.Content{
		Type: assistant.ContentToolCallInputDelta,
		Tool: &assistant.ToolPayload{ToolCallID: toolCallID, PartialJSON: partialJSON},
	})
}

func streamedToolStarted(messageID, toolCallID, toolName string) assistant.Message {
	return assistant.AssistantMessage(messageID, assistant.Content{
		Type: assistant.ContentToolCallStarted,
		Tool: &assistant.ToolPayload{ToolCallID: toolCallID, ToolName: toolName},
	})
}

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
	if !tool.HasFinalInput {
		t.Fatal("final tool input should be marked complete")
	}
}

func TestStreamedToolInputFoldsIntoOneOrderedBlock(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(streamedToolStarted("start", "tc1", "write_file"))
	tr.AppendMessage(streamedToolDelta("delta-1", "tc1", `{"path":`))
	tr.AppendMessage(streamedToolDelta("delta-2", "tc1", `"x"}`))
	tr.AppendMessage(streamedToolMessage("final", assistant.ContentClientToolCall, "tc1", "write_file", "", `{"path":"x"}`))

	blocks := tr.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	tool := blocks[0].Tool
	if tool == nil {
		t.Fatal("tool payload is nil")
	}
	if tool.Name != "write_file" || tool.Input != `{"path":"x"}` {
		t.Fatalf("tool = %+v", tool)
	}
	if !tool.HasFinalInput {
		t.Fatal("final input should be marked complete")
	}
	if tool.InputPartial != "" || tool.InputPreviewTruncated {
		t.Fatalf("final input retained speculative preview = %q (truncated=%v)", tool.InputPartial, tool.InputPreviewTruncated)
	}
}

func TestStreamedToolInputCapsEachCallPreview(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(streamedToolStarted("start", "tc1", "write_file"))
	tr.AppendMessage(streamedToolDelta("delta", "tc1", strings.Repeat("x", maxToolInputPreviewBytes+1)))
	tr.AppendMessage(streamedToolDelta("delta", "tc1", "ignored after cap"))

	tool := tr.Blocks()[0].Tool
	if len(tool.InputPartial) != maxToolInputPreviewBytes {
		t.Fatalf("preview length = %d, want %d", len(tool.InputPartial), maxToolInputPreviewBytes)
	}
	if !tool.InputPreviewTruncated {
		t.Fatal("preview should be marked truncated")
	}
}

func TestStreamedToolInputInterleavesByCallID(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(streamedToolStarted("a-start", "a", "tool-a"))
	tr.AppendMessage(streamedToolStarted("b-start", "b", "tool-b"))
	tr.AppendMessage(streamedToolDelta("a-1", "a", "a1"))
	tr.AppendMessage(streamedToolDelta("b-1", "b", "b1"))
	tr.AppendMessage(streamedToolDelta("a-2", "a", "a2"))

	blocks := tr.Blocks()
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	if blocks[0].Tool.InputPartial != "a1a2" || blocks[1].Tool.InputPartial != "b1" {
		t.Fatalf("interleaved previews = %q, %q", blocks[0].Tool.InputPartial, blocks[1].Tool.InputPartial)
	}
	if blocks[0].Tool.Name != "tool-a" || blocks[1].Tool.Name != "tool-b" {
		t.Fatalf("block order/names = %q, %q", blocks[0].Tool.Name, blocks[1].Tool.Name)
	}
}

func TestStreamedToolInputRejectsMissingAndUnknownIDs(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(streamedToolStarted("missing-start", "", "bad"))
	tr.AppendMessage(streamedToolDelta("missing-delta", "", "bad"))
	tr.AppendMessage(streamedToolDelta("unknown-delta", "unknown", "bad"))
	if len(tr.Blocks()) != 0 {
		t.Fatalf("invalid streamed events produced blocks: %+v", tr.Blocks())
	}

	tr.AppendMessage(streamedToolStarted("valid-start", "valid", "good"))
	tr.AppendMessage(streamedToolDelta("valid-delta", "valid", "ok"))
	if len(tr.Blocks()) != 1 || tr.Blocks()[0].Tool.InputPartial != "ok" {
		t.Fatalf("valid streamed event did not remain isolated: %+v", tr.Blocks())
	}
}

func TestStreamedToolInputFinalEmptyIsPresent(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(streamedToolStarted("start", "tc1", "write_file"))
	tr.AppendMessage(streamedToolDelta("delta", "tc1", "partial"))
	tr.AppendMessage(streamedToolMessage("final", assistant.ContentClientToolCall, "tc1", "write_file", "", ""))

	tool := tr.Blocks()[0].Tool
	if tool.Input != "" {
		t.Fatalf("final input = %q, want empty", tool.Input)
	}
	if !tool.HasFinalInput {
		t.Fatal("empty final input was not marked present")
	}
	if tool.InputPartial != "" || tool.InputPreviewTruncated {
		t.Fatalf("final input retained speculative preview = %q (truncated=%v)", tool.InputPartial, tool.InputPreviewTruncated)
	}
}

func TestStreamedToolInputSnapshotIsImmutable(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(streamedToolStarted("start", "tc1", "write_file"))
	first, _ := tr.AppendMessage(streamedToolDelta("delta-1", "tc1", "one"))
	tr.AppendMessage(streamedToolDelta("delta-2", "tc1", "two"))

	if got := first.Tool.InputPartial; got != "one" {
		t.Fatalf("earlier partial input = %q, want one", got)
	}
	if got := tr.Blocks()[0].Tool.InputPartial; got != "onetwo" {
		t.Fatalf("current partial input = %q, want onetwo", got)
	}
}

func TestMarkToolExecutedAppliesRenderStateUpdate(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(assistant.AssistantMessage("call", assistant.ToolCallContent("tc1", "tool", "{}")))
	state := &struct{ Value string }{Value: "preview"}
	tr.SetToolRenderState("tc1", state)
	previous := tr.Blocks()[0]

	tr.MarkToolExecuted("tc1", ToolResult{RenderState: &RenderStateUpdate{State: nil}})
	if got := tr.Blocks()[0].Tool.RenderState; got != nil {
		t.Fatalf("render state = %#v, want nil after explicit clear", got)
	}
	if got := previous.Tool.RenderState; got != state {
		t.Fatalf("published snapshot render state changed: %#v", got)
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

func TestTechnicalMarkersAreDroppedButUnknownContentIsRetained(t *testing.T) {
	tr := NewTranscript()
	technical := []assistant.Content{
		{Type: assistant.ContentTurnStatus, TurnStatus: &assistant.TurnStatusPayload{Status: "ended"}},
		{Type: assistant.ContentUserStop, Stop: &assistant.StopPayload{Content: "stopped"}},
		{Type: assistant.ContentProviderCompaction, Compaction: &assistant.CompactionPayload{Summary: "private"}},
	}
	for i, content := range technical {
		if block, ok := tr.AppendMessage(assistant.AssistantMessage("technical", content)); ok {
			t.Fatalf("technical content %d produced block %+v", i, block)
		}
	}
	if block, ok := tr.AppendMessage(assistant.AssistantMessage("metadata", assistant.Content{})); ok {
		t.Fatalf("metadata-only content produced block %+v", block)
	}
	unknown, ok := tr.AppendMessage(assistant.AssistantMessage("future", assistant.Content{Type: "future_content"}))
	if !ok || unknown.Kind != assistant.KindUnknown {
		t.Fatalf("unknown content = (%+v, %v), want safe fallback block", unknown, ok)
	}
	if blocks := tr.Blocks(); len(blocks) != 1 || blocks[0].Kind != assistant.KindUnknown {
		t.Fatalf("transcript blocks = %+v", blocks)
	}
}

func TestFinalizeAllReleasesStreamingAccumulator(t *testing.T) {
	tr := NewTranscript()
	tr.AppendMessage(assistant.AssistantMessage("m1", assistant.TextContent("partial")))

	tr.FinalizeAll()

	if tr.accumulator != nil {
		t.Fatal("FinalizeAll retained the completed stream accumulator")
	}
	if got := tr.Blocks()[0]; !got.Complete {
		t.Fatalf("final block = %+v, want complete", got)
	}
}

func TestClientToolCallDerivedClientSideWithoutEngineFold(t *testing.T) {
	// Restore and replay fold history straight into the transcript, never
	// passing through the engine's live-stream fold, so client-side derivation
	// must not depend on the wire is_client_side field.
	tr := NewTranscript()
	content := assistant.ToolCallContent("call-1", "get_local_time", "{}")
	content.Type = assistant.ContentClientToolCall
	tr.AppendMessage(assistant.AssistantMessage("m1", content))

	blocks := tr.Blocks()
	if len(blocks) != 1 || blocks[0].Tool == nil {
		t.Fatalf("transcript blocks = %+v", blocks)
	}
	if !blocks[0].Tool.IsClientSide {
		t.Fatal("replayed client tool call was not derived as client-side")
	}
}

func TestToolNamespaceMergesByPresence(t *testing.T) {
	namespace, empty := "datadog", ""
	tr := NewTranscript()
	tr.AppendMessage(assistant.AssistantMessage("call", assistant.Content{
		Type: assistant.ContentToolCall,
		Tool: &assistant.ToolPayload{ToolCallID: "tc", Metadata: &assistant.ToolMetadata{
			Name: "search_logs", Namespace: &namespace,
		}},
	}))
	// An omitted namespace is not an identity update and must preserve the call's
	// namespace. An explicit empty namespace is an update and must clear it.
	tr.AppendMessage(assistant.AssistantMessage("result-omitted", assistant.Content{
		Type: assistant.ContentToolResponse,
		Tool: &assistant.ToolPayload{ToolCallID: "tc", Metadata: &assistant.ToolMetadata{Name: "search_logs"}},
	}))
	if got := tr.Blocks()[0].Tool.Namespace; got == nil || *got != namespace {
		t.Fatalf("omitted namespace changed to %v, want %q", got, namespace)
	}
	tr.AppendMessage(assistant.AssistantMessage("result-empty", assistant.Content{
		Type: assistant.ContentToolResponse,
		Tool: &assistant.ToolPayload{ToolCallID: "tc", Metadata: &assistant.ToolMetadata{Name: "search_logs", Namespace: &empty}},
	}))
	tool := tr.Blocks()[0].Tool
	if tool.Namespace == nil || *tool.Namespace != "" {
		t.Fatalf("explicit empty namespace did not clear identity: %+v", tool)
	}
}
