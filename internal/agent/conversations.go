package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/DataDog/bits-cli/internal/assistant"
)

var (
	ErrConversationListUnsupported = errors.New("backend does not support listing conversations")
	ErrInvalidConversationID       = errors.New("conversation id is invalid")
	ErrMalformedConversationList   = errors.New("conversation list response is malformed")
	ErrMalformedHistory            = errors.New("conversation history response is malformed")
)

// ConversationListBackend is the optional Assistant API list capability.
// The current API returns the complete list in one response; it has no paging
// request or response fields.
type ConversationListBackend interface {
	UserConversations(ctx context.Context) (*assistant.UserConversationsResponse, error)
}

type ConversationListResult struct {
	Conversations []assistant.ConversationSummary
	Omitted       int
	Err           error
}

// ConversationSwitchResult is a fully loaded but not yet active conversation.
// Commit is the only mutation point; Discard releases the engine gate without
// changing identity. This handshake prevents a cancelled/stale UI result from
// switching the engine behind the visible transcript.
type ConversationSwitchResult struct {
	ConversationID string
	Blocks         []Block
	Err            error
	candidate      *conversationCandidate
}

type conversationCandidate struct {
	transcript *Transcript
	decision   chan bool
	done       chan struct{}
	err        error
	once       sync.Once
}

func (r ConversationSwitchResult) Commit() error  { return r.finish(true) }
func (r ConversationSwitchResult) Discard() error { return r.finish(false) }

func (r ConversationSwitchResult) finish(commit bool) error {
	if r.candidate == nil {
		return r.Err
	}
	r.candidate.once.Do(func() { r.candidate.decision <- commit })
	<-r.candidate.done
	return r.candidate.err
}

// ListConversations owns the engine operation gate until the backend call has
// returned. Results are copied so the UI can safely retain them.
func (e *Engine) ListConversations(ctx context.Context) <-chan ConversationListResult {
	out := make(chan ConversationListResult, 1)
	if !e.begin() {
		out <- ConversationListResult{Err: ErrOperationActive}
		close(out)
		return out
	}
	go func() {
		defer close(out)
		released := false
		release := func() {
			if !released {
				e.active.Store(false)
				released = true
			}
		}
		defer release()
		publish := func(result ConversationListResult) {
			// Completion includes releasing ownership: after receive, the caller
			// may immediately start the next engine operation.
			release()
			out <- result
		}
		backend, ok := e.backend.(ConversationListBackend)
		if !ok {
			publish(ConversationListResult{Err: ErrConversationListUnsupported})
			return
		}
		response, err := backend.UserConversations(ctx)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			publish(ConversationListResult{Err: err})
			return
		}
		if response == nil {
			publish(ConversationListResult{Err: ErrMalformedConversationList})
			return
		}
		if response.Data.Type != "user-conversations-response" {
			publish(ConversationListResult{Err: fmt.Errorf("%w: unexpected response type %q", ErrMalformedConversationList, response.Data.Type)})
			return
		}
		conversations := make([]assistant.ConversationSummary, 0, len(response.Data.Attributes.Conversations))
		omitted := 0
		for _, summary := range response.Data.Attributes.Conversations {
			id := strings.TrimSpace(summary.ConversationID)
			if !validConversationID(id) || id != summary.ConversationID || summary.ID != summary.ConversationID {
				omitted++
				continue
			}
			conversations = append(conversations, summary)
		}
		publish(ConversationListResult{Conversations: conversations, Omitted: omitted})
	}()
	return out
}

// SwitchConversation loads into a temporary transcript and changes the
// engine's identity only after the entire response has folded successfully.
func (e *Engine) SwitchConversation(ctx context.Context, conversationID string) <-chan ConversationSwitchResult {
	conversationID = strings.TrimSpace(conversationID)
	if !validConversationID(conversationID) {
		return switchResult(ConversationSwitchResult{Err: ErrInvalidConversationID})
	}
	if !e.begin() {
		return switchResult(ConversationSwitchResult{Err: ErrOperationActive})
	}
	out := make(chan ConversationSwitchResult, 1)
	go e.switchConversation(ctx, conversationID, out)
	return out
}

func switchResult(result ConversationSwitchResult) <-chan ConversationSwitchResult {
	out := make(chan ConversationSwitchResult, 1)
	out <- result
	close(out)
	return out
}

func (e *Engine) switchConversation(ctx context.Context, conversationID string, out chan<- ConversationSwitchResult) {
	defer close(out)
	released := false
	release := func() {
		if !released {
			e.active.Store(false)
			released = true
		}
	}
	defer release()
	publishTerminal := func(result ConversationSwitchResult) {
		release()
		out <- result
	}
	if err := ctx.Err(); err != nil {
		publishTerminal(ConversationSwitchResult{Err: err})
		return
	}

	if conversationID == e.ConversationID() {
		publishTerminal(ConversationSwitchResult{ConversationID: conversationID, Blocks: e.snapshot()})
		return
	}

	backend, ok := e.backend.(HistoryBackend)
	if !ok {
		publishTerminal(ConversationSwitchResult{Err: ErrHistoryUnsupported})
		return
	}
	response, err := backend.ConversationHistory(ctx, assistant.ConversationHistoryInput{ConversationID: conversationID})
	if ctx.Err() != nil {
		publishTerminal(ConversationSwitchResult{Err: ctx.Err()})
		return
	}
	if err != nil {
		publishTerminal(ConversationSwitchResult{Err: err})
		return
	}
	if response == nil {
		publishTerminal(ConversationSwitchResult{Err: ErrMalformedHistory})
		return
	}
	if response.Data.Type != "conversation-history-response" {
		publishTerminal(ConversationSwitchResult{Err: fmt.Errorf("%w: unexpected response type %q", ErrMalformedHistory, response.Data.Type)})
		return
	}

	temporary := NewTranscript()
	for i, message := range response.Data.Attributes.Messages {
		if err := validateHistoryMessage(message); err != nil {
			publishTerminal(ConversationSwitchResult{Err: fmt.Errorf("%w: message %d: %v", ErrMalformedHistory, i, err)})
			return
		}
		temporary.AppendMessage(message)
	}
	temporary.FinalizeAll()
	if err := ctx.Err(); err != nil {
		publishTerminal(ConversationSwitchResult{Err: err})
		return
	}
	blocks := append([]Block(nil), temporary.Blocks()...)

	candidate := &conversationCandidate{
		transcript: temporary, decision: make(chan bool, 1), done: make(chan struct{}),
	}
	out <- ConversationSwitchResult{ConversationID: conversationID, Blocks: blocks, candidate: candidate}
	commit := false
	select {
	case commit = <-candidate.decision:
	case <-ctx.Done():
		candidate.err = ctx.Err()
	}
	if commit && candidate.err == nil {
		if err := ctx.Err(); err != nil {
			candidate.err = err
		} else {
			// The response's data.id is intentionally ignored: it is a fresh
			// response UUID, not the selected conversation identity.
			e.opts.ConversationID = conversationID
			e.opts.MessageHistory = nil
			e.transcript = temporary
		}
	}
	// candidate.done is the completion barrier observed by Commit/Discard. The
	// operation gate must already be free when that receive unblocks.
	release()
	close(candidate.done)
}

// Snapshot returns a copy of the current transcript. Call it only while the
// engine is idle or after synchronizing with the operation's result channel.
func (e *Engine) Snapshot() []Block     { return e.snapshot() }
func (e *Engine) OperationActive() bool { return e.active.Load() }

// validConversationID implements the canonical lowercase UUID spelling emitted
// by the backend's uuid.UUID route/model without adding another dependency.
func validConversationID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || id != strings.ToLower(id) {
		return false
	}
	for i, r := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func validateHistoryMessage(message assistant.Message) error {
	if strings.TrimSpace(message.Content.Type) == "" {
		// History can include bookkeeping-only records (for example usage) with
		// no renderable content. Transcript.fold discards these no-ops, matching
		// startup restore behavior.
		return nil
	}
	if strings.TrimSpace(message.MessageID) == "" {
		return errors.New("missing message id")
	}
	switch message.Role {
	case "user", "assistant", "system":
	default:
		return fmt.Errorf("unknown role %q", message.Role)
	}
	switch message.Content.Kind() {
	case assistant.KindText:
		if message.Content.Markdown == nil {
			return errors.New("text content has no payload")
		}
	case assistant.KindReasoning:
		if message.Content.Thinking == nil {
			return errors.New("reasoning content has no payload")
		}
	case assistant.KindToolCall, assistant.KindToolResult:
		if message.Content.Tool == nil {
			return errors.New("tool content has no payload")
		}
	case assistant.KindWidget:
		if message.Content.Widget == nil {
			return errors.New("widget content has no payload")
		}
	case assistant.KindDashboard:
		if message.Content.Dashboard == nil {
			return errors.New("dashboard content has no payload")
		}
	case assistant.KindProgress:
		if message.Content.Progress == nil {
			return errors.New("progress content has no payload")
		}
	case assistant.KindTurnMarker:
		if message.Content.TurnStatus == nil {
			return errors.New("turn marker has no payload")
		}
	case assistant.KindStop:
		if message.Content.Stop == nil {
			return errors.New("stop marker has no payload")
		}
	case assistant.KindInternal:
		if message.Content.Compaction == nil {
			return errors.New("internal content has no payload")
		}
	case assistant.KindUnknown:
		// Future discriminators are retained as safe fallback labels. Their raw
		// fields are intentionally unavailable to the renderer.
	}
	return nil
}
