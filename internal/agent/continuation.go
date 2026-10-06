package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/assistant"
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

// SettleRestoredTools marks unfinished calls that cannot resume as cancelled
// in the local transcript, leaving conversation history unchanged. It reports
// whether any block changed and does nothing while an operation is active.
func (e *Engine) SettleRestoredTools(tools *ToolSet) bool {
	if !e.active.CompareAndSwap(false, true) {
		return false
	}
	defer e.active.Store(false)
	resuming := map[string]bool{}
	if e.CanResumeTools(tools) {
		for _, call := range e.continuation.calls {
			resuming[call.Tool.ToolCallID] = true
		}
	}
	changed := false
	for _, block := range e.transcript.Blocks() {
		tool := block.Tool
		if tool == nil || resuming[block.ID.Key] || tool.Status.IsTerminal() {
			continue
		}
		if _, updated := e.transcript.MarkToolExecuted(block.ID.Key, ToolResult{
			Title: "Interrupted", Output: "the session ended before this call was answered", IsError: true, Cancelled: true,
		}); updated {
			changed = true
		}
	}
	return changed
}

// ResumePendingTools restores safe handlers and sends their responses on the
// original conversation, using the original user turn's context.
func (e *Engine) ResumePendingTools(ctx context.Context, in TurnInput) <-chan Event {
	if !e.CanResumeTools(in.Tools) {
		return eventResult(Event{Kind: EventError, Err: ErrNoPendingTools})
	}
	if e.continuation.err != nil {
		return eventResult(Event{Kind: EventError, Err: e.continuation.err})
	}
	in.Context = e.continuation.context
	calls := make([]ToolCall, len(e.continuation.calls))
	for i, content := range e.continuation.calls {
		calls[i] = toolCallOf(content)
	}
	return e.beginTurnWithCalls(ctx, in, calls).events
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
