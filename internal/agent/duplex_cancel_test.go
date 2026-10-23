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
	for event := range turnEvents(engine, ctx, TurnInput{Message: "question"}) {
		if event.Kind == EventState && event.Round > 0 {
			cancel()
		}
	}
	blocks := engine.State().Blocks
	if ctx.Err() != context.Canceled {
		t.Fatalf("context error = %v", ctx.Err())
	}
	if engine.State().ConversationID != "persisted-conversation" || len(blocks) != 2 || !blocks[1].Complete {
		t.Fatalf("partial transcript = %+v, conversation = %q", blocks, engine.State().ConversationID)
	}
}

func TestDuplexRetainsConversationDiscoveredDuringCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &duplexBackend{stream: func(streamCtx context.Context, emit func(assistant.AssistantResponse) error) error {
		cancel() // the turn is cancelled once its stream is open
		<-streamCtx.Done()
		var response assistant.AssistantResponse
		response.Data.Attributes.ConversationID = "persisted-after-cancel"
		_ = emit(response)
		return streamCtx.Err()
	}}
	engine := New(backend, Config{})
	_ = collect(turnEvents(engine, ctx, TurnInput{Message: "question"}))
	if ctx.Err() != context.Canceled || engine.State().ConversationID != "persisted-after-cancel" {
		t.Fatalf("context error = %v, conversation = %q", ctx.Err(), engine.State().ConversationID)
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
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
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
	collect(turnEvents(engine, ctx, TurnInput{Message: "read and write", Tools: tools}))
	if ctx.Err() != context.Canceled {
		t.Fatalf("context error = %v", ctx.Err())
	}
	blocks := engine.State().Blocks
	if len(blocks) != 4 || !blocks[3].Complete {
		t.Fatalf("partial blocks = %+v", blocks)
	}
	for _, block := range blocks[1:3] {
		if block.Tool == nil || block.Tool.Status != ToolCancelled || block.Tool.Approval != nil || block.Tool.RenderState != nil {
			t.Fatalf("unsettled tool %s: %+v", block.ToolCallID(), block.Tool)
		}
	}
}

// The stream ends with its run, so neither a client preview that never became
// execution-ready nor a server tool without a result can complete afterwards.
func TestDuplexTerminalSettlesUnfinishedTools(t *testing.T) {
	failure := errors.New("connection lost before tool batch")
	for _, terminal := range []struct {
		name string
		err  error
	}{{"completed", nil}, {"backend failure", failure}} {
		t.Run(terminal.name, func(t *testing.T) {
			backend := &duplexBackend{stream: func(_ context.Context, emit func(assistant.AssistantResponse) error) error {
				for _, message := range []assistant.Message{
					assistant.AssistantMessage("message", assistant.Content{Type: assistant.ContentToolCallStarted, Tool: &assistant.ToolPayload{ToolCallID: "preview", ToolName: "read", IsClientSide: true, HasClientSide: true}}),
					assistant.AssistantMessage("message", assistant.Content{Type: assistant.ContentToolCallInputDelta, Tool: &assistant.ToolPayload{ToolCallID: "preview", PartialJSON: `{"path":`}}),
					serverCallMsg("server", "search_logs", `{}`),
				} {
					var response assistant.AssistantResponse
					response.Data.Attributes.StructuredMessage = message
					if err := emit(response); err != nil {
						return err
					}
				}
				return terminal.err
			}}
			tools, err := NewToolSet(Tool{
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
			engine := New(backend, Config{PermissionsMode: ModeSkipPermissions})
			events := collect(turnEvents(engine, context.Background(), TurnInput{Message: "read", Tools: tools}))
			err = events[len(events)-1].Err
			blocks := engine.State().Blocks
			if !errors.Is(err, terminal.err) {
				t.Fatalf("error=%v, want %v", err, terminal.err)
			}
			if len(blocks) != 3 {
				t.Fatalf("blocks=%+v", blocks)
			}
			if tool := blocks[1].Tool; tool == nil || tool.Status != ToolCancelled || tool.RenderState != nil {
				t.Fatalf("unfinished preview after terminal: %+v", tool)
			}
			if tool := blocks[2].Tool; tool == nil || tool.Status != ToolCancelled {
				t.Fatalf("unfinished server tool after terminal: %+v", tool)
			}
		})
	}
}
