package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// toolContinuation preserves the wire calls and request context needed to
// answer an interrupted client-tool round. It contains no tool-specific data.
type toolContinuation struct {
	calls   []assistant.Content
	context *assistant.AssistantContext
	err     error
}

// CanResumeTools requires every unanswered call to opt into replay. A mixed
// batch must not silently re-execute a file write, command, or approval gate.
func (e *Engine) CanResumeTools(tools *ToolSet) bool {
	if e.continuation == nil || len(e.continuation.calls) == 0 || tools == nil {
		return false
	}
	for _, call := range e.continuation.calls {
		if call.Tool == nil || call.Tool.Metadata == nil || !tools.tools[call.Tool.Metadata.Name].resumable {
			return false
		}
	}
	return true
}

// ResumePendingTools restores safe handlers and sends their responses on the
// original conversation, using the original user turn's context.
func (e *Engine) ResumePendingTools(ctx context.Context, in TurnInput) <-chan Event {
	if !in.Interactive || !e.CanResumeTools(in.Tools) {
		return eventResult(Event{Kind: EventError, Err: ErrNoPendingTools})
	}
	if e.continuation.err != nil {
		return eventResult(Event{Kind: EventError, Err: e.continuation.err})
	}
	in.Context = e.continuation.context
	return e.beginTurnWithCalls(ctx, in, e.continuation.calls).events
}

func continuationFromHistory(messages []assistant.Message) *toolContinuation {
	pending := &toolContinuation{}
	for _, message := range messages {
		content := message.Content
		kind := content.Kind()
		if kind == assistant.KindTurnMarker || kind == assistant.KindInternal || strings.TrimSpace(content.Type) == "" {
			continue
		}
		if kind == assistant.KindStop {
			pending = &toolContinuation{}
			continue
		}
		if message.Role == "user" && kind != assistant.KindToolResult {
			pending = &toolContinuation{}
			turnContext := &assistant.AssistantContext{}
			if len(message.ContextEntities) > 0 {
				if err := json.Unmarshal(message.ContextEntities, &turnContext.Entities); err != nil {
					pending.err = fmt.Errorf("restore turn entities: %w", err)
				}
			}
			if len(message.ContextResources) > 0 {
				if err := json.Unmarshal(message.ContextResources, &turnContext.Resources); err != nil {
					pending.err = fmt.Errorf("restore turn resources: %w", err)
				}
			}
			if len(turnContext.Entities) > 0 || len(turnContext.Resources) > 0 {
				pending.context = turnContext
			}
		}
		if content.Tool == nil || content.Tool.ToolCallID == "" {
			continue
		}
		switch content.Type {
		case assistant.ContentClientToolCall:
			// A repeated final call updates the same pending entry.
			found := false
			for i, call := range pending.calls {
				if call.Tool.ToolCallID == content.Tool.ToolCallID {
					pending.calls[i], found = content, true
					break
				}
			}
			if !found {
				pending.calls = append(pending.calls, content)
			}
		case assistant.ContentClientToolResponse, assistant.ContentToolResponse:
			for i, call := range pending.calls {
				if call.Tool.ToolCallID == content.Tool.ToolCallID {
					pending.calls = append(pending.calls[:i], pending.calls[i+1:]...)
					break
				}
			}
		}
	}
	if len(pending.calls) == 0 {
		return nil
	}
	return pending
}
