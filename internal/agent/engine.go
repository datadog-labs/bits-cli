// Package agent drives the remote assistant turn loop and emits fine-grained
// events on a channel. It imports only the assistant client and has no Bubble
// Tea / UI dependency, so it is reusable by a future headless surface and
// testable without a program.
package agent

import (
	"context"
	"errors"
	"fmt"
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
// The current design is a serialized operation/actor model: a claimed operation
// exclusively owns all mutable conversation state, including opts, transcript,
// previousConversationID, and session grants. State crosses goroutine boundaries
// through copied events/results and channel synchronization. Only Decide,
// CancelTool, and explicitly atomic methods may be called concurrently with an
// operation. Do not add a mutex around Engine state without first changing this
// ownership model and identifying accesses that its existing operation gate and
// channel boundaries do not order.
type Engine struct {
	backend                Backend
	opts                   assistant.SendOptions
	transcript             *Transcript
	previousConversationID string
	commands               chan toolCommand
	sessionGrants          map[ApprovalKey]struct{}
	active                 atomic.Bool
	operationGeneration    atomic.Uint64
}

type toolCommand struct {
	generation uint64
	id         string
	decision   ApprovalDecision
	cancel     bool
}

type pendingTool struct {
	call  ToolCall
	index int
	key   ApprovalKey
}

type toolDone struct {
	call   ToolCall
	index  int
	result ToolResult
	err    error
}

// New returns an Engine. opts carries the per-request defaults (profile, model,
// conversation id, …); ConversationID is updated as turns run.
func New(b Backend, opts assistant.SendOptions) *Engine {
	return &Engine{
		backend:       b,
		opts:          opts,
		transcript:    NewTranscript(),
		commands:      make(chan toolCommand, 64),
		sessionGrants: make(map[ApprovalKey]struct{}),
	}
}

type TurnInput struct {
	Message string
	Tools   *ToolSet
}

// StartTurn runs one user turn (plus any client-tool round-trips) in a goroutine
// and streams events. The channel is closed when the turn ends. Cancel ctx to
// interrupt; cancellation ends the turn quietly (no error event). An overlap
// returns a one-event ErrOperationActive channel.
func (e *Engine) StartTurn(ctx context.Context, in TurnInput) <-chan Event {
	if !e.begin() {
		return eventResult(Event{Kind: EventError, Err: ErrOperationActive})
	}
	generation := e.operationGeneration.Load()
	out := make(chan Event, 64)
	go e.run(ctx, in, out, generation)
	return out
}

// begin claims the engine for one operation. The operation owns the paired
// release, including a loaded conversation awaiting the UI's commit decision.
func (e *Engine) begin() bool {
	if !e.active.CompareAndSwap(false, true) {
		return false
	}
	e.operationGeneration.Add(1)
	return true
}

func eventResult(ev Event) <-chan Event {
	out := make(chan Event, 1)
	out <- ev
	close(out)
	return out
}

func (e *Engine) Decide(toolCallID string, decision ApprovalDecision) bool {
	if !decision.valid() {
		return false
	}
	return e.command(toolCommand{
		generation: e.operationGeneration.Load(),
		id:         toolCallID,
		decision:   decision,
	})
}

func (e *Engine) CancelTool(toolCallID string) bool {
	return e.command(toolCommand{
		generation: e.operationGeneration.Load(),
		id:         toolCallID,
		cancel:     true,
	})
}

func (e *Engine) command(command toolCommand) bool {
	select {
	case e.commands <- command:
		return true
	default:
		return false
	}
}

func (e *Engine) run(ctx context.Context, in TurnInput, out chan<- Event, generation uint64) {
	defer close(out)
	defer e.active.Store(false)

	tools := in.Tools
	defs := tools.Definitions()

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
		b, ok := e.transcript.AppendMessage(msg)
		blocks := append([]Block(nil), e.transcript.Blocks()...)
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
	userBlock := e.transcript.AppendUser(in.Message)
	userBlocks := append([]Block(nil), e.transcript.Blocks()...)
	if !send(Event{Kind: EventBlock, Update: TranscriptUpdate{Blocks: userBlocks, Changed: userBlock}}) {
		return
	}

	var next any = in.Message
	convID := e.ConversationID()

	for range maxTurns {
		var calls []assistant.Content

		opts := e.opts
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
				e.opts.ConversationID = id
			}
			return // cancelled: end the turn quietly
		}
		if err != nil {
			e.finalizeTranscript()
			send(Event{Kind: EventError, Err: err})
			return
		}

		convID = id
		e.opts.ConversationID = convID
		send(Event{Kind: EventConversation, ConvID: convID})

		if len(calls) == 0 {
			e.finalizeTranscript()
			send(Event{Kind: EventTurnDone})
			return
		}
		responses, complete, err := e.runTools(ctx, tools, calls, generation, send)
		if err != nil {
			e.finalizeTranscript()
			send(Event{Kind: EventError, Err: err})
			return
		}
		if !complete {
			if ctx.Err() == nil {
				e.finalizeTranscript()
				send(Event{Kind: EventTurnDone})
			}
			return
		}
		next = responses
	}
	e.finalizeTranscript()
	send(Event{Kind: EventError, Err: ErrMaxTurns})
}

// snapshot returns a copy of the current blocks, safe to send on the channel and
// retain: the engine keeps mutating its own transcript on later folds.
func (e *Engine) snapshot() []Block {
	return append([]Block(nil), e.transcript.Blocks()...)
}

func (e *Engine) finalizeTranscript() {
	e.transcript.FinalizeAll()
}

// ConversationID reports the conversation the engine is bound to. It is set
// from SendOptions and updated as turns run; empty means a new conversation.
func (e *Engine) ConversationID() string { return e.opts.ConversationID }

// PreviousConversationID reports the most recent non-empty conversation that
// NewConversation left behind. It remains available after reset so navigation
// and lifecycle checks can recover the prior persisted conversation.
func (e *Engine) PreviousConversationID() string { return e.previousConversationID }

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

	if e.opts.ConversationID != "" {
		e.previousConversationID = e.opts.ConversationID
	}
	e.opts.ConversationID = ""
	e.opts.MessageHistory = nil
	e.transcript = NewTranscript()
	clear(e.sessionGrants)
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
	for _, msg := range resp.Data.Attributes.Messages {
		e.transcript.AppendMessage(msg)
	}
	e.transcript.FinalizeAll()
	if blocks := e.snapshot(); len(blocks) > 0 {
		send(Event{Kind: EventBlock, Update: TranscriptUpdate{Blocks: blocks}})
	}
}

// Workers only execute handlers. The engine goroutine owns approvals,
// transcript changes, and response ordering.
func (e *Engine) runTools(
	ctx context.Context,
	tools *ToolSet,
	contents []assistant.Content,
	generation uint64,
	send func(Event) bool,
) ([]assistant.ClientToolResponse, bool, error) {
	work := make([]pendingTool, len(contents))
	seen := make(map[string]struct{}, len(contents))
	byID := make(map[string]pendingTool, len(contents))
	for i, content := range contents {
		call := toolCallOf(content)
		if call.ID == "" {
			return nil, false, errors.New("client tool call has no id")
		}
		if _, exists := seen[call.ID]; exists {
			return nil, false, fmt.Errorf("duplicate client tool call id %q", call.ID)
		}
		seen[call.ID] = struct{}{}
		work[i] = pendingTool{call: call, index: i}
		byID[call.ID] = work[i]
	}

	responses := make([]assistant.ClientToolResponse, len(work))
	pending := make(map[string]pendingTool)
	running := make(map[string]context.CancelFunc)
	resolved := make(map[string]bool, len(work))
	results := make(chan toolDone, len(work))
	outstanding := len(work)

	emit := func(block Block, ok bool) bool {
		return !ok || send(Event{
			Kind: EventBlock,
			Update: TranscriptUpdate{
				Blocks:  e.snapshot(),
				Changed: block,
			},
		})
	}
	finishBlock := func(item pendingTool, result ToolResult) bool {
		if resolved[item.call.ID] {
			return true
		}
		resolved[item.call.ID] = true
		outstanding--
		block, ok := e.transcript.MarkToolExecuted(item.call.ID, result)
		return emit(block, ok)
	}
	record := func(item pendingTool, result ToolResult) bool {
		responses[item.index] = toolResponse(item.call, result)
		return finishBlock(item, result)
	}
	launch := func(item pendingTool, approved bool) bool {
		toolCtx, cancel := context.WithCancel(ctx)
		running[item.call.ID] = cancel
		if approved {
			block, ok := e.transcript.MarkToolRunning(item.call.ID)
			if !emit(block, ok) {
				cancel()
				delete(running, item.call.ID)
				return false
			}
		}
		go func() {
			result, err := tools.Run(toolCtx, item.call)
			results <- toolDone{call: item.call, index: item.index, result: result, err: err}
		}()
		return true
	}
	cancelRunning := func() {
		for _, cancel := range running {
			cancel()
		}
	}
	stopRound := func(failed *pendingTool, err error) {
		cancelRunning()
		for _, item := range work {
			if resolved[item.call.ID] {
				continue
			}
			result := cancelledResult()
			if failed != nil && item.call.ID == failed.call.ID {
				result = ToolResult{Title: "Tool failed", Output: err.Error(), IsError: true}
			}
			finishBlock(item, result)
		}
	}

	for _, item := range work {
		requirement, needsApproval := tools.Approval(item.call)
		_, granted := e.sessionGrants[requirement.Key]
		if needsApproval && !granted {
			item.key = requirement.Key
			pending[item.call.ID] = item
			block, ok := e.transcript.MarkAwaitingApproval(item.call.ID, requirement.Prompt)
			if !emit(block, ok) {
				cancelRunning()
				return nil, false, nil
			}
			continue
		}
		if !launch(item, false) {
			cancelRunning()
			return nil, false, nil
		}
	}

	for outstanding > 0 {
		select {
		case command := <-e.commands:
			if command.generation != generation {
				continue
			}
			if command.cancel {
				if item, ok := pending[command.id]; ok {
					delete(pending, command.id)
					if !record(item, cancelledResult()) {
						cancelRunning()
						return nil, false, nil
					}
					continue
				}
				if cancel, ok := running[command.id]; ok {
					cancel()
					delete(running, command.id)
					if !record(byID[command.id], cancelledResult()) {
						cancelRunning()
						return nil, false, nil
					}
				}
				continue
			}

			item, ok := pending[command.id]
			if !ok {
				continue
			}
			delete(pending, command.id)
			if command.decision == ApprovalDeny {
				finishBlock(item, deniedResult())
				stopRound(nil, nil)
				return nil, false, nil
			}
			if command.decision == ApprovalAllowSession {
				e.sessionGrants[item.key] = struct{}{}
			}
			if !launch(item, true) {
				cancelRunning()
				return nil, false, nil
			}
			for id, sibling := range pending {
				if _, granted := e.sessionGrants[sibling.key]; !granted {
					continue
				}
				delete(pending, id)
				if !launch(sibling, true) {
					cancelRunning()
					return nil, false, nil
				}
			}

		case done := <-results:
			if resolved[done.call.ID] {
				continue
			}
			if cancel, ok := running[done.call.ID]; ok {
				cancel()
				delete(running, done.call.ID)
			}
			item := work[done.index]
			if done.err != nil {
				stopRound(&item, done.err)
				return nil, false, done.err
			}
			if !record(item, done.result) {
				cancelRunning()
				return nil, false, nil
			}

		case <-ctx.Done():
			cancelRunning()
			return nil, false, nil
		}
	}
	return responses, true, nil
}

func toolCallOf(content assistant.Content) ToolCall {
	var call ToolCall
	if content.Tool == nil {
		return call
	}
	call.ID = content.Tool.ToolCallID
	if content.Tool.Metadata != nil {
		call.Name = content.Tool.Metadata.Name
		call.Input = content.Tool.Metadata.Input
	}
	return call
}

func toolResponse(call ToolCall, result ToolResult) assistant.ClientToolResponse {
	title := result.Title
	if title == "" {
		title = call.Name
		if title == "" {
			title = "tool"
		}
	}
	response := assistant.ClientToolResponse{
		Type:       "client_tool_response",
		ToolCallID: call.ID,
		Status:     assistant.ToolStatusSuccess,
		Title:      title,
		Metadata: assistant.ClientToolMetadata{
			Name:   call.Name,
			Input:  call.Input,
			Output: result.Output,
		},
	}
	if result.IsError {
		response.Status = assistant.ToolStatusError
	}
	return response
}
