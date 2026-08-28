package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

type toolTurnBackend struct {
	t           *testing.T
	toolName    string
	responses   []assistant.ClientToolResponse
	definitions []assistant.ClientTool
	calls       int
}

func (b *toolTurnBackend) Send(_ context.Context, message any, opts assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.t.Helper()
	b.definitions = opts.ClientTools
	b.calls++
	if b.calls == 1 {
		content := assistant.ToolCallContent("call-1", b.toolName, `{"value":42}`)
		content.Type = assistant.ContentClientToolCall
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("tool-message", content)
		return "conversation-1", emit(response)
	}

	var ok bool
	b.responses, ok = message.([]assistant.ClientToolResponse)
	if !ok {
		b.t.Fatalf("tool follow-up has type %T", message)
	}
	var response assistant.AssistantResponse
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("done"))
	return "conversation-1", emit(response)
}

func TestEngineToolTurnE2E(t *testing.T) {
	harnessErr := errors.New("tool harness failed")
	tests := []struct {
		name       string
		toolName   string
		result     ToolResult
		handlerErr error
		wantStatus assistant.ToolStatus
		wantTitle  string
		wantOutput string
		wantErr    error
	}{
		{name: "success", toolName: "calculate", result: ToolResult{Output: "42"}, wantStatus: assistant.ToolStatusSuccess, wantTitle: "calculate", wantOutput: "42"},
		{name: "expected failure", toolName: "calculate", result: ToolResult{Title: "Invalid input", Output: "value is required", IsError: true}, wantStatus: assistant.ToolStatusError, wantTitle: "Invalid input", wantOutput: "value is required"},
		{name: "unknown tool", toolName: "missing", wantStatus: assistant.ToolStatusError, wantTitle: "Unknown tool", wantOutput: "no client tool named missing is registered"},
		{name: "harness failure", toolName: "calculate", handlerErr: harnessErr, wantErr: harnessErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &toolTurnBackend{t: t, toolName: tt.toolName}
			var tools *ToolSet
			if tt.toolName != "missing" {
				var err error
				tools, err = NewToolSet(Tool{
					Definition: assistant.ClientTool{Name: "calculate"},
					Handler: func(_ context.Context, call ToolCall) (ToolResult, error) {
						if call.ID != "call-1" || call.Input != `{"value":42}` {
							t.Fatalf("tool call = %+v", call)
						}
						return tt.result, tt.handlerErr
					},
				})
				if err != nil {
					t.Fatal(err)
				}
			}

			events := drain(New(backend, assistant.SendOptions{}).StartTurn(context.Background(), TurnInput{
				Message: "use a tool",
				Tools:   tools,
			}))
			if tt.wantErr != nil {
				if got := events[len(events)-1]; got.Kind != EventError || !errors.Is(got.Err, tt.wantErr) {
					t.Fatalf("last event = %+v", got)
				}
				if backend.calls != 1 {
					t.Fatalf("backend calls = %d, want 1", backend.calls)
				}
				return
			}

			if len(backend.responses) != 1 {
				t.Fatalf("tool responses = %d, want 1", len(backend.responses))
			}
			response := backend.responses[0]
			if response.ToolCallID != "call-1" || response.Status != tt.wantStatus || response.Title != tt.wantTitle || response.Metadata.Output != tt.wantOutput {
				t.Fatalf("tool response = %+v", response)
			}
			if len(events) == 0 || events[len(events)-1].Kind != EventTurnDone {
				t.Fatalf("turn did not complete: %v", kinds(events))
			}
		})
	}
}
