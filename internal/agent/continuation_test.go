package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent/env"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func savedCall(id, name string) assistant.Message {
	call := assistant.ToolCallContent(id, name, `{}`)
	call.Type = assistant.ContentClientToolCall
	return assistant.AssistantMessage(id, call)
}

func TestContinuationHistoryBoundaries(t *testing.T) {
	first, second := savedCall("one", "input"), savedCall("two", "input")
	response := assistant.AssistantMessage("result", assistant.ToolResultContent("one", "input", assistant.ToolStatusSuccess, "done"))
	response.Role = "user"
	response.Content.Type = assistant.ContentClientToolResponse
	stop := assistant.AssistantMessage("stop", assistant.Content{Type: assistant.ContentUserStop})
	user := assistant.Message{Role: "user", Content: assistant.TextContent("new turn")}
	for _, tc := range []struct {
		name     string
		messages []assistant.Message
		ids      []string
	}{
		{"batch with bookkeeping", []assistant.Message{first, second, {Results: &assistant.Results{}}, assistant.AssistantMessage("end", assistant.Content{Type: assistant.ContentTurnStatus}), assistant.AssistantMessage("internal", assistant.Content{Type: assistant.ContentProviderCompaction})}, []string{"one", "two"}},
		{"partial response", []assistant.Message{first, second, response}, []string{"two"}},
		{"answered", []assistant.Message{first, response}, nil},
		{"stopped", []assistant.Message{first, second, stop}, nil},
		{"new user turn", []assistant.Message{first, user}, nil},
		{"duplicate final call", []assistant.Message{first, first}, []string{"one"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pending := continuationFromHistory(tc.messages)
			var ids []string
			for _, c := range pending {
				ids = append(ids, c.Tool.ToolCallID)
			}
			if !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("pending IDs = %v, want %v", ids, tc.ids)
			}
		})
	}
}

// optionsBackend records the options of the last Send.
type optionsBackend struct{ opts assistant.SendOptions }

func (b *optionsBackend) Send(_ context.Context, _ any, opts assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	b.opts = opts
	return "conversation", nil
}

func TestResumedToolsDoNotResendTheTurnContext(t *testing.T) {
	user := assistant.Message{Role: "user", Content: assistant.TextContent("investigate"), ContextEntities: json.RawMessage(`[{"type":"service","id":"api","label":"API"}]`), ContextResources: json.RawMessage(`[{"name":"dashboard","value":42}]`)}
	set, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "input"}, Resumable: true,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &optionsBackend{}
	e := New(backend, Config{ConversationID: "conversation"})
	e.continuation = continuationFromHistory([]assistant.Message{user, savedCall("one", "input")})
	drain(e.ResumePendingTools(t.Context(), TurnInput{Tools: set}))
	if got := backend.opts.Context; got != nil {
		t.Fatalf("resumed tool answers carried context %+v", got)
	}
}

func TestStoppingAResumedRoundDrainsWithoutInstructionsOrContext(t *testing.T) {
	dir := t.TempDir()
	writeInstructionTestFile(t, dir, "AGENTS.md", "local instructions")
	started := make(chan struct{})
	set, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "input"}, Resumable: true,
		Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
			close(started)
			<-ctx.Done()
			return ToolResult{}, ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &optionsBackend{}
	e := New(backend, Config{ConversationID: "conversation", ProjectInstructions: env.NewInstructions(dir)})
	user := assistant.Message{Role: "user", Content: assistant.TextContent("investigate"), ContextEntities: json.RawMessage(`[{"type":"service","id":"api"}]`)}
	e.continuation = continuationFromHistory([]assistant.Message{user, savedCall("one", "input")})

	events := e.ResumePendingTools(t.Context(), TurnInput{Tools: set})
	<-started
	if !e.StopTools() {
		t.Fatal("StopTools was not accepted during the resumed round")
	}
	drain(events)

	if backend.opts.ConversationID != "conversation" {
		t.Fatal("the stopped round was not drained")
	}
	if backend.opts.CustomUserContext != "" || backend.opts.Context != nil {
		t.Fatalf("drain carried user context %q and context %+v", backend.opts.CustomUserContext, backend.opts.Context)
	}
}

func TestResumeRequiresEveryHandlerToOptIn(t *testing.T) {
	handler := func(context.Context, ToolCall) (ToolResult, error) {
		t.Error("unsafe handler executed")
		return ToolResult{}, nil
	}
	set, err := NewToolSet(ModeSkipPermissions,
		Tool{Definition: assistant.ClientTool{Name: "input"}, Handler: handler, Resumable: true},
		Tool{Definition: assistant.ClientTool{Name: "write"}, Handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"write", "unknown", assistant.ApprovalRequestTool} {
		e := New(&scriptBackend{}, Config{})
		e.continuation = continuationFromHistory([]assistant.Message{savedCall("one", "input"), savedCall("two", name)})
		if e.CanResumeTools(set) {
			t.Fatalf("batch containing %s is resumable", name)
		}
		events := drain(e.ResumePendingTools(t.Context(), TurnInput{Tools: set}))
		if len(events) != 1 || !errors.Is(events[0].Err, ErrNoPendingTools) {
			t.Fatalf("events = %+v", events)
		}
	}
}

func TestCancelledClientToolPublishesTerminalSnapshot(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(fmt.Sprintf("resumed=%t", resumed), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			started, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			set, err := NewToolSet(ModeManual, Tool{
				Definition: assistant.ClientTool{Name: "input"}, Resumable: true,
				Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
					close(started)
					<-ctx.Done()
					// Keep worker results out of the way so teardown itself must
					// publish the final state through the public event stream.
					<-release
					return cancelledResult(), nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			call := savedCall("call-input", "input")
			e := New(&scriptBackend{msgs: []assistant.Message{call}, convID: "conversation"}, Config{ConversationID: "conversation"})
			var events <-chan Event
			if resumed {
				e.transcript.AppendMessage(call)
				e.continuation = continuationFromHistory([]assistant.Message{call})
				events = e.ResumePendingTools(ctx, TurnInput{Tools: set})
			} else {
				events = e.StartTurn(ctx, TurnInput{Message: "ask", Tools: set})
			}
			<-started
			cancel()
			var last *ToolBlock
			for event := range events {
				if event.Kind != EventTranscript {
					continue
				}
				for _, b := range event.Transcript.Blocks {
					if b.ToolCallID() == "call-input" {
						last = b.Tool
					}
				}
			}
			if last == nil || last.Status != ToolCancelled {
				t.Fatalf("last delivered tool = %+v, want cancelled", last)
			}
		})
	}
}
