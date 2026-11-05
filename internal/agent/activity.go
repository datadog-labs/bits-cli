package agent

import (
	"context"
	"errors"

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

// SyncConversationTranscript never takes ownership away from a local run.
// Existing task observers survive replacement of the foreground transcript.
func (e *Engine) SyncConversationTranscript(id string, replay *ActivityReplay, active bool) (State, error) {
	var state State
	err := e.idle(func() error {
		if id == "" || id != e.opts.ConversationID || replay == nil {
			return ErrConversationChanged
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
func (e *Engine) ResumeToolTurn(ctx context.Context, in ToolRecoveryInput) (RunID, error) {
	var run RunID
	var calls []ToolCall
	err := e.idle(func() (err error) {
		if in.ConversationID == "" || in.ConversationID != e.opts.ConversationID {
			return ErrConversationChanged
		}
		if len(in.Calls) == 0 {
			return ErrNoPendingTools
		}
		for _, content := range in.Calls {
			call := toolCallOf(content)
			if owned, ok := e.recoveryCalls[call.ID]; !ok || owned != call {
				return ErrNoPendingTools
			}
			calls = append(calls, call)
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
