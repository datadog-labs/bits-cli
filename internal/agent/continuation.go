package agent

import (
	"context"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// CanResumeTools requires every unanswered call to opt into replay. A mixed
// batch must not silently re-execute a file write, command, or approval gate.
func (e *Engine) CanResumeTools(tools *ToolSet) bool {
	return get(e, func() bool { return e.canResumeTools(tools) })
}

func (e *Engine) canResumeTools(tools *ToolSet) bool {
	if len(e.continuation) == 0 || tools == nil {
		return false
	}
	for _, call := range e.continuation {
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
	changed := false
	_ = e.idle(func() error { changed = e.settleRestoredTools(tools); return nil })
	return changed
}

func (e *Engine) settleRestoredTools(tools *ToolSet) bool {
	resuming := map[string]bool{}
	if e.canResumeTools(tools) {
		for _, call := range e.continuation {
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
// original conversation.
func (e *Engine) ResumePendingTools(ctx context.Context, in TurnInput) <-chan Event {
	return e.startTurn(ctx, in, true)
}

// continuationFromHistory returns the client tool calls left unanswered by the
// last user turn.
func continuationFromHistory(messages []assistant.Message) []assistant.Content {
	var pending []assistant.Content
	for _, message := range messages {
		content := message.Content
		kind := content.Kind()
		if kind == assistant.KindTurnMarker || kind == assistant.KindInternal || strings.TrimSpace(content.Type) == "" {
			continue
		}
		if kind == assistant.KindStop {
			pending = nil
			continue
		}
		if message.Role == "user" && kind != assistant.KindToolResult {
			pending = nil
		}
		if content.Tool == nil || content.Tool.ToolCallID == "" {
			continue
		}
		switch content.Type {
		case assistant.ContentClientToolCall:
			// A repeated final call updates the same pending entry.
			found := false
			for i, call := range pending {
				if call.Tool.ToolCallID == content.Tool.ToolCallID {
					pending[i], found = content, true
					break
				}
			}
			if !found {
				pending = append(pending, content)
			}
		case assistant.ContentClientToolResponse, assistant.ContentToolResponse:
			for i, call := range pending {
				if call.Tool.ToolCallID == content.Tool.ToolCallID {
					pending = append(pending[:i], pending[i+1:]...)
					break
				}
			}
		}
	}
	return pending
}
