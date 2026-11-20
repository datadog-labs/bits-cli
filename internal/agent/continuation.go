package agent

import (
	"context"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
)

// CanResumeTools requires every unanswered call to opt into replay. A mixed
// batch must not silently re-execute a file write, command, or approval gate.
func (e *Engine) CanResumeTools(tools *ToolSet) bool {
	return get(e, func() bool { return e.canResumeTools(tools) })
}

func (e *Engine) canResumeTools(tools *ToolSet) bool {
	if len(e.approved) > 0 && tools != nil {
		return true
	}
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
// in the local transcript, leaving conversation history unchanged, and
// publishes the state if a block changed. It does nothing while an operation
// is active.
func (e *Engine) SettleRestoredTools(tools *ToolSet) {
	_ = e.idle(func() error {
		if e.settleRestoredTools(tools) {
			e.publish()
		}
		return nil
	})
}

func (e *Engine) settleRestoredTools(tools *ToolSet) bool {
	resuming := map[string]bool{}
	// A stopped turn's calls wait for their decision or their run.
	for id := range e.stoppedCalls {
		resuming[id] = true
	}
	for id := range e.approved {
		resuming[id] = true
	}
	if len(e.approved) == 0 && e.canResumeTools(tools) {
		for _, call := range e.continuation {
			resuming[call.Tool.ToolCallID] = true
		}
	}
	// A call the conversation shows unanswered that cannot resume by itself
	// is offered for a decision, as after a stop: the conversation still
	// waits for its answer, so prompts wait too.
	changed := false
	if tools != nil {
		for _, content := range e.continuation {
			call := toolCallOf(content)
			if resuming[call.ID] || !tools.Has(call.Name) && call.Name != spec.ApprovalRequest {
				continue
			}
			if e.stoppedCalls == nil {
				e.stoppedCalls = map[string]ToolCall{}
			}
			e.stoppedCalls[call.ID] = call
			resuming[call.ID] = true
			changed = true
		}
		if changed {
			e.tools = tools
			e.continuation = nil
			e.offerStopped()
		}
	}
	if e.follower != nil && !e.follower.shown {
		// Until the replay shows whether the turn still runs, an unfinished
		// tool may be running: the replay settles them.
		e.unsettled = func() { e.settleRestoredTools(tools) }
		return changed
	}
	e.unsettled = nil
	if e.turnState == TurnRunning {
		// The conversation still runs the turn: its unfinished tools are
		// running, and their results and the turn's end follow.
		return changed
	}
	return e.transcript.settleTools(resuming) || changed
}

// settleTools interrupts unfinished tools except those selected for replay.
func (t *Transcript) settleTools(resuming map[string]bool) bool {
	changed := false
	for _, block := range t.Blocks() {
		tool := block.Tool
		if tool == nil || resuming[block.ID.Key] || tool.Status.IsTerminal() {
			continue
		}
		if _, updated := t.MarkToolExecuted(block.ID.Key, ToolResult{
			Title: "Interrupted", Output: "the session ended before this call was answered", IsError: true, Cancelled: true,
		}); updated {
			changed = true
		}
	}
	return changed
}

// ResumePendingTools starts a run that restores safe handlers and sends their
// responses on the original conversation, as StartTurn does for a new turn.
func (e *Engine) ResumePendingTools(ctx context.Context, in TurnInput) (RunID, error) {
	return e.startTurn(ctx, in, true)
}

// continuationFromHistory returns the client tool calls left unanswered by the
// last user turn.
func continuationFromHistory(messages []assistant.Message) []assistant.Content {
	var pending []assistant.Content
	for _, message := range messages {
		if taskIDOf(message) != "" {
			// A background task's messages are its own: they neither answer
			// nor end the conversation's pending calls.
			continue
		}
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

// decideStopped answers a call a confirmed stop left unanswered: a denial is
// held for the next prompt, and an approval for the run that answers it,
// which ResumePendingTools starts. It fails with ErrStale for any other call.
func (e *Engine) decideStopped(id string, decision ApprovalDecision) error {
	err := ErrClosed
	e.do(func() {
		call, ok := e.stoppedCalls[id]
		if !ok || e.turn != nil {
			err = ErrStale
			return
		}
		err = nil
		delete(e.stoppedCalls, id)
		if decision == ApprovalDeny {
			result := approvalDeniedResult(call)
			e.steering = append(e.steering, toolResponse(call, result))
			e.transcript.MarkToolExecuted(id, e.tools.NormalizeResult(call, result))
		} else {
			if e.approved == nil {
				e.approved = map[string]ToolCall{}
			}
			e.approved[id] = call
		}
		e.publish()
	})
	return err
}
