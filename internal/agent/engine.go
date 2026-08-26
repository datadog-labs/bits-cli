// Package agent drives the remote assistant turn loop and emits fine-grained
// events on a channel. It imports only the assistant client and has no Bubble
// Tea / UI dependency, so it is reusable by a future headless surface and
// testable without a program.
package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/DataDog/bits-cli/internal/assistant"
)

var (
	ErrMaxTurns           = errors.New("exceeded max turns")
	ErrHistoryUnsupported = errors.New("backend does not support loading conversation history")
	ErrOperationActive    = errors.New("another conversation operation is active")
)

// Backend is the minimal transport the engine drives. *assistant.Client
// satisfies it; tests can substitute a fake.
type Backend interface {
	Send(ctx context.Context, message any, opts assistant.SendOptions,
		fn func(assistant.AssistantResponse) error) (string, error)
}

// HistoryBackend adds conversation history loading
type HistoryBackend interface {
	ConversationHistory(ctx context.Context, in assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error)
}

// EventKind discriminates the events the engine streams for a turn.
type EventKind int

const (
	EventBlock        EventKind = iota // a block was created or updated; read Block
	EventUsage                         // token usage update; read Usage
	EventConversation                  // server-assigned/confirmed conversation id
	EventTurnDone                      // the turn completed with no pending tool calls
	EventError                         // the turn failed
)

// Event is one thing that happened during a turn. It is a plain value carried
// on a channel, with only the fields relevant to Kind populated.
type Event struct {
	Kind   EventKind
	Update TranscriptUpdate // for EventBlock
	Usage  *assistant.Usage // for EventUsage
	ConvID string           // for EventConversation
	Err    error            // for EventError
}

// TranscriptUpdate is the snapshot delivered on each block change: the full
// ordered block list plus the block that changed.
type TranscriptUpdate struct {
	Blocks  []Block
	Changed Block
}

// maxTurns caps the client-tool loop so a misbehaving backend can't spin
// forever.
const maxTurns = 20

// Engine drives the assistant turn loop over a Backend and streams events. It
// owns the aggregated conversation transcript, folding streamed deltas into it
// and emitting snapshots.
//
// It is not designed to run concurrent turns / restore and left to the consumer
// to make sure it does not concurrently starts either of those in parallel.
type Engine struct {
	backend                Backend
	mu                     sync.RWMutex // protects opts, transcript, and previousConversationID
	opts                   assistant.SendOptions
	transcript             *Transcript
	previousConversationID string
	// active is true while a turn or restore runs; overlapping them is a bug.
	active atomic.Bool
}

// New returns an Engine. opts carries the per-request defaults (profile, model,
// conversation id, …); ConversationID is updated as turns run.
func New(b Backend, opts assistant.SendOptions) *Engine {
	return &Engine{
		backend:    b,
		opts:       opts,
		transcript: NewTranscript(),
	}
}

// TurnInput is everything needed to start one turn. Tools are the client tools
// permitted for this turn; the caller resolves them from its permission model
// each time. They are fixed for the whole turn, including client-tool
// round-trips.
type TurnInput struct {
	Message string
	Tools   []assistant.Tool
}

// StartTurn runs one user turn (plus any client-tool round-trips) in a goroutine
// and streams events. The channel is closed when the turn ends. Cancel ctx to
// interrupt; cancellation ends the turn quietly (no error event). An overlap
// returns a one-event ErrOperationActive channel.
func (e *Engine) StartTurn(ctx context.Context, in TurnInput) <-chan Event {
	if !e.begin() {
		return eventResult(Event{Kind: EventError, Err: ErrOperationActive})
	}
	out := make(chan Event, 64)
	go e.run(ctx, in, out)
	return out
}

// begin claims the engine for one operation. The operation owns the paired
// release, including a loaded conversation awaiting the UI's commit decision.
func (e *Engine) begin() bool { return e.active.CompareAndSwap(false, true) }

func eventResult(ev Event) <-chan Event {
	out := make(chan Event, 1)
	out <- ev
	close(out)
	return out
}

func (e *Engine) run(ctx context.Context, in TurnInput, out chan<- Event) {
	defer close(out)
	defer e.active.Store(false)

	// Tools are fixed for the whole turn: index them once by name for execution
	// and derive the definitions resent to the server each round-trip.
	tools := make(map[string]assistant.Tool, len(in.Tools))
	for _, t := range in.Tools {
		tools[t.Name] = t
	}
	defs := toolDefs(in.Tools)

	// send is cancellation-aware so a stalled consumer during cancel can't
	// wedge the engine goroutine.
	send := func(ev Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	fold := func(msg assistant.Message) bool {
		e.mu.Lock()
		b, ok := e.transcript.AppendMessage(msg)
		blocks := append([]Block(nil), e.transcript.Blocks()...)
		e.mu.Unlock()
		if ok {
			if !send(Event{Kind: EventBlock, Update: TranscriptUpdate{Blocks: blocks, Changed: b}}) {
				return false
			}
		}
		if msg.Results != nil && msg.Results.Usage != nil {
			if !send(Event{Kind: EventUsage, Usage: msg.Results.Usage}) {
				return false
			}
		}
		return true
	}

	// The user's turn opens the transcript; the engine owns the user block too.
	e.mu.Lock()
	userBlock := e.transcript.AppendUser(in.Message)
	userBlocks := append([]Block(nil), e.transcript.Blocks()...)
	e.mu.Unlock()
	if !send(Event{Kind: EventBlock, Update: TranscriptUpdate{Blocks: userBlocks, Changed: userBlock}}) {
		return
	}

	var next any = in.Message
	convID := e.ConversationID()

	for range maxTurns {
		var calls []assistant.Content

		e.mu.RLock()
		opts := e.opts
		e.mu.RUnlock()
		opts.ConversationID = convID
		opts.ClientTools = defs

		id, err := e.backend.Send(ctx, next, opts, func(ar assistant.AssistantResponse) error {
			msg := ar.Data.Attributes.StructuredMessage
			// A client_tool_call pauses the stream until we answer it; collect it
			// for execTools below.
			if msg.Content.Type == assistant.ContentClientToolCall {
				calls = append(calls, msg.Content)
			}
			if !fold(msg) {
				return ctx.Err()
			}
			return nil
		})
		if ctx.Err() != nil {
			// Send can discover a server-assigned ID before cancellation tears
			// down the stream. Retain it so /new can preserve a resumable handle
			// for the conversation being left behind.
			if id != "" {
				e.mu.Lock()
				e.opts.ConversationID = id
				e.mu.Unlock()
			}
			return // cancelled: end the turn quietly
		}
		if err != nil {
			e.finalizeTranscript()
			send(Event{Kind: EventError, Err: err})
			return
		}

		convID = id
		e.mu.Lock()
		e.opts.ConversationID = convID
		e.mu.Unlock()
		send(Event{Kind: EventConversation, ConvID: convID})

		if len(calls) == 0 {
			e.finalizeTranscript()
			send(Event{Kind: EventTurnDone})
			return
		}
		next = execTools(ctx, tools, calls)
	}
	e.finalizeTranscript()
	send(Event{Kind: EventError, Err: ErrMaxTurns})
}

// snapshot returns a copy of the current blocks, safe to send on the channel and
// retain: the engine keeps mutating its own transcript on later folds.
func (e *Engine) snapshot() []Block {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]Block(nil), e.transcript.Blocks()...)
}

func (e *Engine) finalizeTranscript() {
	e.mu.Lock()
	e.transcript.FinalizeAll()
	e.mu.Unlock()
}

// ConversationID reports the conversation the engine is bound to. It is set
// from SendOptions and updated as turns run; empty means a new conversation.
func (e *Engine) ConversationID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.opts.ConversationID
}

// PreviousConversationID reports the most recent non-empty conversation that
// NewConversation left behind. It remains available after reset so navigation
// and lifecycle checks can recover the prior persisted conversation.
func (e *Engine) PreviousConversationID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.previousConversationID
}

// NewConversation clears the conversation-scoped engine state without making a
// backend request. Non-history request configuration and the backend/client
// itself are retained; the next StartTurn sends an empty conversation ID and no
// injected history, which asks the Assistant API to create the new conversation
// on first turn.
//
// Callers must cancel and drain an active turn or restore before resetting.
// The operation gate makes a premature reset fail without mutation.
func (e *Engine) NewConversation() error {
	if !e.active.CompareAndSwap(false, true) {
		return ErrOperationActive
	}
	defer e.active.Store(false)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.opts.ConversationID != "" {
		e.previousConversationID = e.opts.ConversationID
	}
	e.opts.ConversationID = ""
	e.opts.MessageHistory = nil
	e.transcript = NewTranscript()
	return nil
}

// Restore fetches the persisted history for the engine's conversation, folds it
// into the transcript.
func (e *Engine) Restore(ctx context.Context) <-chan Event {
	if !e.begin() {
		return eventResult(Event{Kind: EventError, Err: ErrOperationActive})
	}
	out := make(chan Event, 64)
	go e.restore(ctx, out)
	return out
}

func (e *Engine) restore(ctx context.Context, out chan<- Event) {
	defer close(out)
	defer e.active.Store(false)

	send := func(ev Event) {
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}

	conversationID := e.ConversationID()
	if conversationID == "" {
		return
	}
	hb, ok := e.backend.(HistoryBackend)
	if !ok {
		send(Event{Kind: EventError, Err: ErrHistoryUnsupported})
		return
	}
	resp, err := hb.ConversationHistory(ctx, assistant.ConversationHistoryInput{ConversationID: conversationID})
	if ctx.Err() != nil {
		return // cancelled: end quietly
	}
	if err != nil {
		send(Event{Kind: EventError, Err: err})
		return
	}
	if resp == nil {
		return
	}
	e.mu.Lock()
	for _, msg := range resp.Data.Attributes.Messages {
		e.transcript.AppendMessage(msg)
	}
	e.transcript.FinalizeAll()
	e.mu.Unlock()
	if blocks := e.snapshot(); len(blocks) > 0 {
		send(Event{Kind: EventBlock, Update: TranscriptUpdate{Blocks: blocks}})
	}
}

// toolDefs returns the client tool definitions to resend each turn (nil when
// none are registered).
func toolDefs(tools []assistant.Tool) []assistant.ClientTool {
	if len(tools) == 0 {
		return nil
	}
	defs := make([]assistant.ClientTool, 0, len(tools))
	for _, t := range tools {
		defs = append(defs, t.ClientTool)
	}
	return defs
}

// execTools runs each client tool call locally and builds the responses to post
// back. Unregistered tools answer with an error result rather than aborting the
// loop.
func execTools(ctx context.Context, tools map[string]assistant.Tool, calls []assistant.Content) []assistant.ClientToolResponse {
	responses := make([]assistant.ClientToolResponse, 0, len(calls))
	for _, call := range calls {
		var name, input string
		if call.Tool != nil && call.Tool.Metadata != nil {
			name, input = call.Tool.Metadata.Name, call.Tool.Metadata.Input
		}
		toolCallID := ""
		if call.Tool != nil {
			toolCallID = call.Tool.ToolCallID
		}
		resp := assistant.ClientToolResponse{
			Type:       "client_tool_response",
			ToolCallID: toolCallID,
			Status:     assistant.ToolStatusSuccess,
			Metadata:   assistant.ClientToolMetadata{Name: name, Input: input},
		}
		switch tool, ok := tools[name]; {
		case !ok:
			resp.Status = assistant.ToolStatusError
			resp.Title = "Unknown tool"
			resp.Metadata.Output = "no client tool named " + name + " is registered"
		default:
			if out, err := tool.Run(ctx, input); err != nil {
				resp.Status = assistant.ToolStatusError
				resp.Title = "Tool error"
				resp.Metadata.Output = err.Error()
			} else {
				resp.Title = "Ran " + name
				resp.Metadata.Output = out
			}
		}
		responses = append(responses, resp)
	}
	return responses
}
