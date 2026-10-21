package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func TestDuplexRetainsPartialTranscriptDuringCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	failure := errors.New("backend failed during cancellation")
	backend := &duplexBackend{stream: func(ctx context.Context, emit func(assistant.AssistantResponse) error) error {
		var response assistant.AssistantResponse
		response.Data.Attributes.ConversationID = "persisted-conversation"
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("partial answer"))
		if err := emit(response); err != nil {
			return err
		}
		<-ctx.Done()
		return failure
	}}
	engine := New(backend, Config{})
	for event := range engine.StartTurn(ctx, TurnInput{Message: "question"}) {
		if event.Kind == EventTranscript && event.Origin == TranscriptOriginRemote {
			cancel()
		}
	}
	blocks := engine.Snapshot()
	if ctx.Err() != context.Canceled {
		t.Fatalf("context error = %v", ctx.Err())
	}
	if engine.ConversationID() != "persisted-conversation" || len(blocks) != 2 || !blocks[1].Complete {
		t.Fatalf("partial transcript = %+v, conversation = %q", blocks, engine.ConversationID())
	}
}

func TestDuplexRetainsConversationDiscoveredDuringCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &duplexBackend{stream: func(ctx context.Context, emit func(assistant.AssistantResponse) error) error {
		<-ctx.Done()
		var response assistant.AssistantResponse
		response.Data.Attributes.ConversationID = "persisted-after-cancel"
		_ = emit(response)
		return ctx.Err()
	}}
	engine := New(backend, Config{})
	for event := range engine.StartTurn(ctx, TurnInput{Message: "question"}) {
		if event.Kind == EventTranscript && event.Origin == TranscriptOriginLocal {
			cancel()
		}
	}
	if ctx.Err() != context.Canceled || engine.ConversationID() != "persisted-after-cancel" {
		t.Fatalf("context error = %v, conversation = %q", ctx.Err(), engine.ConversationID())
	}
}

func TestDuplexCancellationSettlesRunningAndAwaitingTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{})
	backend := &duplexBackend{stream: func(ctx context.Context, emit func(assistant.AssistantResponse) error) error {
		for _, response := range []assistant.AssistantResponse{duplexCall("read", "read"), duplexCall("write", "write")} {
			if err := emit(response); err != nil {
				return err
			}
		}
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("partial answer"))
		if err := emit(response); err != nil {
			return err
		}
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	tools, err := NewToolSet(ModeManual,
		Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
			<-ctx.Done()
			return ToolResult{}, ctx.Err()
		}},
		Tool{Definition: assistant.ClientTool{Name: "write"}, Handler: func(context.Context, ToolCall) (ToolResult, error) {
			t.Error("unapproved tool executed")
			return ToolResult{}, nil
		}, Approval: func(ToolCall) (ApprovalRequirement, bool) {
			return ApprovalRequirement{Key: ApprovalKey{Tool: "write"}}, true
		}, InputReducer: func(context.Context, ToolInputUpdate, any) any { return "speculative preview" }},
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, Config{})
	go func() { <-started; cancel() }()
	collect(engine.StartTurn(ctx, TurnInput{Message: "read and write", Tools: tools}))
	if ctx.Err() != context.Canceled {
		t.Fatalf("context error = %v", ctx.Err())
	}
	blocks := engine.Snapshot()
	if len(blocks) != 4 || !blocks[3].Complete {
		t.Fatalf("partial blocks = %+v", blocks)
	}
	for _, block := range blocks[1:3] {
		if block.Tool == nil || block.Tool.Status != ToolCancelled || block.Tool.Approval != nil || block.Tool.RenderState != nil {
			t.Fatalf("unsettled tool %s: %+v", block.ToolCallID(), block.Tool)
		}
	}
}

func TestDuplexTerminalSettlesToolInputPreview(t *testing.T) {
	failure := errors.New("connection lost before tool batch")
	for _, terminal := range []struct {
		name string
		err  error
	}{{"completed", nil}, {"backend failure", failure}} {
		t.Run(terminal.name, func(t *testing.T) {
			backend := &duplexBackend{stream: func(_ context.Context, emit func(assistant.AssistantResponse) error) error {
				for _, content := range []assistant.Content{
					{Type: assistant.ContentToolCallStarted, Tool: &assistant.ToolPayload{ToolCallID: "preview", ToolName: "read", IsClientSide: true, HasClientSide: true}},
					{Type: assistant.ContentToolCallInputDelta, Tool: &assistant.ToolPayload{ToolCallID: "preview", PartialJSON: `{"path":`}},
				} {
					var response assistant.AssistantResponse
					response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("message", content)
					if err := emit(response); err != nil {
						return err
					}
				}
				return terminal.err
			}}
			tools, err := NewToolSet(ModeSkipPermissions, Tool{
				Definition: assistant.ClientTool{Name: "read"},
				Handler: func(context.Context, ToolCall) (ToolResult, error) {
					t.Error("preview executed without final input")
					return ToolResult{}, nil
				},
				InputReducer: func(context.Context, ToolInputUpdate, any) any { return "speculative preview" },
			})
			if err != nil {
				t.Fatal(err)
			}
			engine := New(backend, Config{})
			events := collect(engine.StartTurn(context.Background(), TurnInput{Message: "read", Tools: tools}))
			err = events[len(events)-1].Err
			blocks := engine.Snapshot()
			if !errors.Is(err, terminal.err) {
				t.Fatalf("error=%v, want %v", err, terminal.err)
			}
			if len(blocks) != 2 {
				t.Fatalf("blocks=%+v", blocks)
			}
			tool := blocks[1].Tool
			if tool == nil || !tool.Status.IsTerminal() || tool.RenderState != nil {
				t.Fatalf("unfinished preview after terminal: %+v", tool)
			}
		})
	}
}
