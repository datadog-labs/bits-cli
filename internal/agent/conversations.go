package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// Conversation is a validated history ready to install. Loading it does not
// read or change the engine's current conversation. Install transfers ownership
// to the engine; a Conversation must only be installed once.
type Conversation struct {
	id           string
	transcript   *Transcript
	continuation *toolContinuation
}

// ListConversations reads and validates summaries without taking the engine's
// operation gate. The returned slice does not alias backend storage.
func (e *Engine) ListConversations(ctx context.Context) ConversationListResult {
	backend, ok := e.backend.(ConversationListBackend)
	if !ok {
		return ConversationListResult{Err: ErrConversationListUnsupported}
	}
	response, err := backend.UserConversations(ctx)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return ConversationListResult{Err: err}
	}
	if response == nil {
		return ConversationListResult{Err: ErrMalformedConversationList}
	}
	if response.Data.Type != "user-conversations-response" {
		return ConversationListResult{Err: fmt.Errorf("%w: unexpected response type %q",
			ErrMalformedConversationList, response.Data.Type)}
	}
	conversations := make([]assistant.ConversationSummary, 0, len(response.Data.Attributes.Conversations))
	omitted := 0
	for _, summary := range response.Data.Attributes.Conversations {
		id := strings.TrimSpace(summary.ConversationID)
		if !ValidConversationID(id) || id != summary.ConversationID || summary.ID != summary.ConversationID {
			omitted++
			continue
		}
		conversations = append(conversations, summary)
	}
	return ConversationListResult{Conversations: conversations, Omitted: omitted}
}

// LoadConversation fetches and folds history without claiming engine state.
// Callers may drop a stale result without any cleanup or rollback.
func (e *Engine) LoadConversation(ctx context.Context, conversationID string) (*Conversation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conversationID = strings.TrimSpace(conversationID)
	if !ValidConversationID(conversationID) {
		return nil, ErrInvalidConversationID
	}
	backend, ok := e.backend.(HistoryBackend)
	if !ok {
		return nil, ErrHistoryUnsupported
	}
	response, err := backend.ConversationHistory(ctx, assistant.ConversationHistoryInput{ConversationID: conversationID})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, ErrMalformedHistory
	}
	if response.Data.Type != "conversation-history-response" {
		return nil, fmt.Errorf("%w: unexpected response type %q", ErrMalformedHistory, response.Data.Type)
	}
	transcript := NewTranscript()
	for i, message := range response.Data.Attributes.Messages {
		if err := validateHistoryMessage(message); err != nil {
			return nil, fmt.Errorf("%w: message %d: %w", ErrMalformedHistory, i, err)
		}
		transcript.AppendMessage(message)
	}
	transcript.FinalizeAll()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Conversation{
		id: conversationID, transcript: transcript,
		continuation: continuationFromHistory(response.Data.Attributes.Messages),
	}, nil
}

// InstallConversation replaces the current conversation while idle. The caller
// must first check that the load still belongs to its current request.
func (e *Engine) InstallConversation(ctx context.Context, conversation *Conversation) error {
	if !e.begin() {
		return ErrOperationActive
	}
	defer e.active.Store(false)
	if err := ctx.Err(); err != nil {
		return err
	}
	e.installConversation(conversation)
	return nil
}

// installConversation runs only while the caller owns the operation gate.
func (e *Engine) installConversation(conversation *Conversation) {
	e.opts.ConversationID = conversation.id
	e.opts.MessageHistory = nil
	e.sentUserContext = nil
	e.projectInstructionsManager.reset()
	e.transcript = conversation.transcript
	e.continuation = conversation.continuation
}

// Snapshot returns a copy of the current transcript. Call it only while the
// engine is idle or after synchronizing with the operation's result channel.
func (e *Engine) Snapshot() []Block     { return e.snapshot().Blocks }
func (e *Engine) OperationActive() bool { return e.active.Load() }

// ValidConversationID reports the canonical lowercase UUID spelling emitted
// by the backend's uuid.UUID route/model, without adding another dependency.
func ValidConversationID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || id != strings.ToLower(id) {
		return false
	}
	for i, r := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
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
