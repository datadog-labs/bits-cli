package agent

import (
	"context"
	"errors"
	"reflect"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

var (
	ErrActivitySubscriptionUnsupported = errors.New("backend does not support conversation activity subscriptions")
	ErrConversationChanged             = errors.New("conversation changed while synchronizing subscription transcript")
)

type activityBackend interface {
	SubscribeConversation(context.Context, string, func(assistant.ConversationActivity) error) error
}

func (e *Engine) SupportsActivity() bool {
	_, ok := e.backend.(activityBackend)
	return ok
}

type ActivityUpdate struct {
	Activity assistant.ConversationActivity
	Err      error
}

// SubscribeActivity observes one attachment. The surface owns its cancellation
// and reconnect policy, independently of the engine's task observers.
func (e *Engine) SubscribeActivity(ctx context.Context, id string) <-chan ActivityUpdate {
	out := make(chan ActivityUpdate, 16)
	backend, ok := e.backend.(activityBackend)
	if !ok {
		out <- ActivityUpdate{Err: ErrActivitySubscriptionUnsupported}
		close(out)
		return out
	}
	started := false
	e.do(func() {
		if e.life.Err() == nil {
			e.workers.Add(1)
			started = true
		}
	})
	if !started {
		out <- ActivityUpdate{Err: ErrClosed}
		close(out)
		return out
	}
	e.work(ctx, func(ctx context.Context) {
		defer close(out)
		err := backend.SubscribeConversation(ctx, id, func(a assistant.ConversationActivity) error {
			select {
			case out <- ActivityUpdate{Activity: a}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if err != nil {
			select {
			case out <- ActivityUpdate{Err: err}:
			case <-ctx.Done():
			}
		}
	})
	return out
}

// ActivityReplay buffers one subscription attachment. The UI feeds messages;
// the engine folds them on its actor when the foreground is free. A fresh
// attachment replaces history once, then live messages append incrementally.
// Its caller must serialize Append and SyncConversationTranscript.
type ActivityReplay struct {
	messages []assistant.Message
	applied  int
}

func NewActivityReplay(messages ...assistant.Message) *ActivityReplay {
	return &ActivityReplay{messages: append([]assistant.Message(nil), messages...)}
}

func (r *ActivityReplay) Append(message assistant.Message) {
	r.messages = append(r.messages, message)
}

// extends reports whether r begins with everything already applied from prev.
func (r *ActivityReplay) extends(prev *ActivityReplay) bool {
	if prev.applied == 0 || len(r.messages) < prev.applied {
		return false
	}
	for i := range prev.applied {
		if !reflect.DeepEqual(r.messages[i], prev.messages[i]) {
			return false
		}
	}
	return true
}

// SyncConversationTranscript never takes ownership away from a local run.
// Existing task observers survive replacement of the foreground transcript.
func (e *Engine) SyncConversationTranscript(id string, replay *ActivityReplay, active bool) (State, error) {
	var state State
	err := e.idle(func() error {
		if id == "" || id != e.opts.ConversationID || replay == nil {
			return ErrConversationChanged
		}
		if prev := e.activityReplay; prev != nil && prev != replay && replay.extends(prev) {
			// A reconnect replayed what is already shown; keep the transcript
			// and fold only the new tail.
			replay.applied = prev.applied
			e.activityReplay = replay
		}
		if e.activityReplay != replay {
			conversation := conversationFromSnapshot(e.forkSource, replay.messages, active)
			transcript := conversation.transcript
			transcript.userSeq = e.transcript.userSeq
			transcript.supersede(e.transcript)
			e.transcript, e.fork = transcript, conversation.fork
			// Preserve live observers and their approvals. Add tasks first seen
			// in this replay, including orphaned and historical task output.
			for _, t := range conversation.tasks {
				if findTask(e.tasks, t.ID) == nil {
					e.addTasks([]*task{t})
				}
			}
			e.activityReplay = replay
		} else {
			for _, msg := range replay.messages[replay.applied:] {
				if taskIDOf(msg) == "" {
					e.transcript.AppendMessage(msg)
				}
			}
		}
		replay.applied = len(replay.messages)
		if !active {
			e.transcript.FinalizeAll()
		}
		e.discoverTasks()
		e.publish()
		state = e.state()
		return nil
	})
	return state, err
}

// PendingActivityTools returns unanswered calls observed on this CLI's request
// stream. A later remote turn, or a changed call with the same ID, cannot gain
// local execution authority merely by appearing in a replay.
func (e *Engine) PendingActivityTools(replay *ActivityReplay) []assistant.Content {
	if replay == nil {
		return nil
	}
	return get(e, func() []assistant.Content {
		pending := continuationFromHistory(replay.messages[historyForkSplit(e.forkSource, replay.messages):])
		var owned []assistant.Content
		for _, content := range pending {
			call := toolCallOf(content)
			if original, ok := e.recoveryCalls[call.ID]; ok && original == call {
				owned = append(owned, content)
			}
		}
		return owned
	})
}

type ToolRecoveryInput struct {
	ConversationID string
	Calls          []assistant.Content
	Tools          *ToolSet
	OnDeny         DenyPolicy
}

type recoveredResult struct {
	call   ToolCall
	result ToolResult
}

// ResumeToolTurn reuses cached results for calls whose response POST failed.
// A call whose handler started but never finished is answered as interrupted
// unless its tool is resumable, so side effects are not repeated.
func (e *Engine) ResumeToolTurn(ctx context.Context, in ToolRecoveryInput) (RunID, error) {
	var run RunID
	var calls []ToolCall
	err := e.idle(func() (err error) {
		if in.ConversationID == "" || in.ConversationID != e.opts.ConversationID {
			return ErrConversationChanged
		}
		if len(in.Calls) == 0 || in.Tools == nil {
			return ErrNoPendingTools
		}
		for _, content := range in.Calls {
			call := toolCallOf(content)
			if owned, ok := e.recoveryCalls[call.ID]; !ok || owned != call {
				return ErrNoPendingTools
			}
			calls = append(calls, call)
		}
		for _, call := range calls {
			if _, cached := e.recoveryResults[call.ID]; cached {
				continue
			}
			if started, ok := e.recoveryStarted[call.ID]; !ok || started != call || in.Tools.tools[call.Name].resumable {
				continue
			}
			if e.recoveryResults == nil {
				e.recoveryResults = make(map[string]recoveredResult)
			}
			e.recoveryResults[call.ID] = recoveredResult{call: call, result: interruptedResult()}
		}
		e.continuation = nil
		run, err = e.claim()
		return err
	})
	if err != nil {
		return 0, err
	}
	e.work(ctx, func(ctx context.Context) { e.run(ctx, run, TurnInput{Tools: in.Tools, OnDeny: in.OnDeny}, calls) })
	return run, nil
}
