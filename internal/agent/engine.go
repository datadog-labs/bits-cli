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
	// Round is the 1-based backend send the event belongs to (0 for
	// out-of-turn events). Drained rounds after a denial count but emit
	// no content events.
	Round int
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
	// OnDeny is the turn's policy after a denial is answered on the wire.
	OnDeny DenyPolicy
}

// turnCompletion is the final state captured before an engine operation
// releases ownership. It remains authoritative even if cancellation prevents a
// corresponding event from reaching the consumer. Its snapshots are read-only.
type turnCompletion struct {
	ConversationID string
	Blocks         []Block
	Usage          *assistant.Usage
	Completed      bool
	Denied         bool
	Err            error
}

type turnOperation struct {
	events     <-chan Event
	completion <-chan turnCompletion
}

// beginTurn starts one user turn and its client-tool round trips. The event
// channel closes only after the completion snapshot has been captured and the
// engine operation has been released.
func (e *Engine) beginTurn(ctx context.Context, in TurnInput) turnOperation {
	if !e.begin() {
		return completedTurnOperation(ErrOperationActive)
	}
	generation := e.operationGeneration.Load()
	events := make(chan Event, 64)
	completion := make(chan turnCompletion, 1)
	go e.run(ctx, in, events, completion, generation)
	return turnOperation{events: events, completion: completion}
}

// StartTurn preserves the event-only API used by interactive surfaces.
func (e *Engine) StartTurn(ctx context.Context, in TurnInput) <-chan Event {
	return e.beginTurn(ctx, in).events
}

func completedTurnOperation(err error) turnOperation {
	events := eventResult(Event{Kind: EventError, Err: err})
	completion := make(chan turnCompletion, 1)
	completion <- turnCompletion{Err: err}
	close(completion)
	return turnOperation{events: events, completion: completion}
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

func (e *Engine) run(
	ctx context.Context,
	in TurnInput,
	out chan<- Event,
	completionOut chan<- turnCompletion,
	generation uint64,
) {
	completion := turnCompletion{}
	defer func() {
		if completion.Err == nil && !completion.Completed && ctx.Err() != nil {
			completion.Err = ctx.Err()
		}
		completion.ConversationID = e.opts.ConversationID
		completion.Blocks = e.snapshot()
		e.active.Store(false)
		completionOut <- completion
		close(completionOut)
		close(out)
	}()

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

	// The user's turn opens the transcript; the engine owns the user block too.
	userBlock := e.transcript.AppendUser(in.Message)
	userBlocks := append([]Block(nil), e.transcript.Blocks()...)
	if !send(Event{Kind: EventBlock, Round: 1, Update: TranscriptUpdate{Blocks: userBlocks, Changed: userBlock}}) {
		return
	}

	var next any = in.Message
	convID := e.ConversationID()

	for round := 1; round <= maxTurns; round++ {
		var calls []assistant.Content

		emit := func(ev Event) bool {
			ev.Round = round
			return send(ev)
		}
		fold := func(msg assistant.Message) bool {
			if msg.Results != nil && msg.Results.Usage != nil {
				completion.Usage = cloneUsage(msg.Results.Usage)
			}
			b, ok := e.transcript.AppendMessage(msg)
			blocks := append([]Block(nil), e.transcript.Blocks()...)
			if ok {
				if !emit(Event{Kind: EventBlock, Update: TranscriptUpdate{Blocks: blocks, Changed: b}}) {
					return false
				}
			}
			if msg.Results != nil && msg.Results.Usage != nil {
				if !emit(Event{Kind: EventUsage, Usage: msg.Results.Usage}) {
					return false
				}
			}
			return true
		}

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
		// Send may discover a server-assigned ID before a later stream failure or
		// cancellation. Preserve it so every surface can report a resumable handle.
		if id != "" {
			e.opts.ConversationID = id
		}
		if err != nil {
			// Context-derived cancellation remains quiet. An independent backend
			// failure wins even if the caller canceled at the same time, preserving
			// the more specific diagnostic in the authoritative completion.
			if ctx.Err() == nil || !errors.Is(err, ctx.Err()) {
				completion.Err = err
				e.finalizeTranscript()
				if id != "" && !emit(Event{Kind: EventConversation, ConvID: id}) {
					return
				}
				emit(Event{Kind: EventError, Err: err})
				return
			}
		}
		if ctx.Err() != nil {
			return // cancelled: end the turn quietly
		}

		convID = id
		e.opts.ConversationID = convID
		emit(Event{Kind: EventConversation, ConvID: convID})

		if len(calls) == 0 {
			e.finalizeTranscript()
			completion.Completed = emit(Event{Kind: EventTurnDone})
			return
		}
		toolRound, err := e.runTools(ctx, tools, calls, generation, emit, in.OnDeny)
		if err != nil {
			completion.Err = err
			completion.Denied = completion.Denied || toolRound.denied
			e.finalizeTranscript()
			emit(Event{Kind: EventError, Err: err})
			return
		}
		completion.Denied = completion.Denied || toolRound.denied
		if toolRound.stopped {
			// A denial was answered on the wire; discard the follow-up and end the turn.
			if ctx.Err() == nil {
				opts.ConversationID = convID
				id, drainRounds, err := e.drainStoppedToolCalls(ctx, toolRound.responses, opts)
				terminalRound := round + drainRounds
				if id != "" {
					e.opts.ConversationID = id
				}
				if err != nil && (ctx.Err() == nil || !errors.Is(err, ctx.Err())) {
					completion.Err = err
					e.finalizeTranscript()
					send(Event{Kind: EventError, Round: terminalRound, Err: err})
					return
				}
				if ctx.Err() != nil {
					return // cancelled: end the turn quietly
				}
				e.finalizeTranscript()
				completion.Completed = send(Event{Kind: EventTurnDone, Round: terminalRound})
			}
			return
		}
		if !toolRound.complete {
			if ctx.Err() == nil {
				e.finalizeTranscript()
				completion.Completed = emit(Event{Kind: EventTurnDone})
			}
			return
		}
		next = toolRound.responses
	}
	completion.Err = ErrMaxTurns
	e.finalizeTranscript()
	send(Event{Kind: EventError, Round: maxTurns, Err: ErrMaxTurns})
}

// drainStoppedToolCalls cancels follow-up client calls after a stopped
// round so the backend is never left waiting.
func (e *Engine) drainStoppedToolCalls(
	ctx context.Context,
	responses []assistant.ClientToolResponse,
	opts assistant.SendOptions,
) (string, int, error) {
	payload := any(responses)
	conversationID := opts.ConversationID
	rounds := 0
	for range maxTurns {
		rounds++
		var calls []ToolCall
		seen := make(map[string]struct{})
		id, err := e.backend.Send(ctx, payload, opts, func(response assistant.AssistantResponse) error {
			content := response.Data.Attributes.StructuredMessage.Content
			if content.Type != assistant.ContentClientToolCall {
				return nil
			}
			call := toolCallOf(content)
			if call.ID == "" {
				return errors.New("client tool call has no id")
			}
			if _, duplicate := seen[call.ID]; !duplicate {
				seen[call.ID] = struct{}{}
				calls = append(calls, call)
			}
			return nil
		})
		if id != "" {
			conversationID = id
			opts.ConversationID = id
		}
		if err != nil {
			return conversationID, rounds, err
		}
		if len(calls) == 0 {
			return conversationID, rounds, nil
		}
		drained := make([]assistant.ClientToolResponse, len(calls))
		for i, call := range calls {
			drained[i] = toolResponse(call, cancelledResult())
		}
		payload = drained
	}
	return conversationID, rounds, ErrMaxTurns
}

// toolRound is one client-tool round trip's wire responses and outcome.
type toolRound struct {
	responses []assistant.ClientToolResponse
	complete  bool
	denied    bool
	stopped   bool
}

func cloneUsage(usage *assistant.Usage) *assistant.Usage {
	if usage == nil {
		return nil
	}
	copied := *usage
	copied.InputTokens = cloneInt(usage.InputTokens)
	copied.OutputTokens = cloneInt(usage.OutputTokens)
	copied.TimeToFirstChunkMs = cloneInt(usage.TimeToFirstChunkMs)
	return &copied
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
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
	onDeny DenyPolicy,
) (toolRound, error) {
	work := make([]pendingTool, len(contents))
	seen := make(map[string]struct{}, len(contents))
	byID := make(map[string]pendingTool, len(contents))
	for i, content := range contents {
		call := toolCallOf(content)
		if call.ID == "" {
			return toolRound{}, errors.New("client tool call has no id")
		}
		if _, exists := seen[call.ID]; exists {
			return toolRound{}, fmt.Errorf("duplicate client tool call id %q", call.ID)
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
	denied := false
	stopRequested := false

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
	// deny answers a gate refusal on the wire.
	deny := func(item pendingTool, result ToolResult) bool {
		denied = true
		return record(item, result)
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
	// stopPendingAfterDenial answers still-pending approvals as cancelled;
	// running siblings finish so their real results reach the wire batch.
	stopPendingAfterDenial := func() bool {
		for id, item := range pending {
			delete(pending, id)
			if !record(item, cancelledResult()) {
				cancelRunning()
				return false
			}
		}
		return true
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
		if item.call.Name == assistant.ApprovalRequestTool {
			// A protocol gate, not a registered tool. Gated mode denies it with
			// no interactive decision; the TUI flow is tracked in BCLI-41.
			if tools.ApprovesServerGate() {
				if !record(item, approvedResult()) {
					cancelRunning()
					return toolRound{denied: denied}, nil
				}
			} else {
				if !deny(item, serverDeniedResult()) {
					cancelRunning()
					return toolRound{denied: denied}, nil
				}
				if onDeny == DenyStop {
					stopRequested = true
					if !stopPendingAfterDenial() {
						return toolRound{denied: denied}, nil
					}
				}
			}
			continue
		}
		requirement, needsApproval := tools.Approval(item.call)
		_, granted := e.sessionGrants[requirement.Key]
		if needsApproval && !granted {
			if stopRequested {
				if !record(item, cancelledResult()) {
					cancelRunning()
					return toolRound{denied: denied}, nil
				}
				continue
			}
			item.key = requirement.Key
			pending[item.call.ID] = item
			block, ok := e.transcript.MarkAwaitingApproval(item.call.ID, requirement.Prompt)
			if !emit(block, ok) {
				cancelRunning()
				return toolRound{denied: denied}, nil
			}
			continue
		}
		if !launch(item, false) {
			cancelRunning()
			return toolRound{denied: denied}, nil
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
						return toolRound{denied: denied}, nil
					}
					continue
				}
				if cancel, ok := running[command.id]; ok {
					cancel()
					delete(running, command.id)
					if !record(byID[command.id], cancelledResult()) {
						cancelRunning()
						return toolRound{denied: denied}, nil
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
				// Answer the denial on the wire; siblings still resolve. Under
				// DenyStop the round then aborts without a follow-up round.
				if !deny(item, deniedResult()) {
					cancelRunning()
					return toolRound{denied: denied}, nil
				}
				if onDeny == DenyStop {
					stopRequested = true
					if !stopPendingAfterDenial() {
						return toolRound{denied: denied}, nil
					}
				}
				continue
			}
			if command.decision == ApprovalAllowSession {
				e.sessionGrants[item.key] = struct{}{}
			}
			if !launch(item, true) {
				cancelRunning()
				return toolRound{denied: denied}, nil
			}
			for id, sibling := range pending {
				if _, granted := e.sessionGrants[sibling.key]; !granted {
					continue
				}
				delete(pending, id)
				if !launch(sibling, true) {
					cancelRunning()
					return toolRound{denied: denied}, nil
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
				if stopRequested {
					// Record the failure so the stopped batch still gets answered.
					if !record(item, ToolResult{Title: "Tool failed", Output: done.err.Error(), IsError: true}) {
						cancelRunning()
						return toolRound{denied: denied}, nil
					}
					continue
				}
				stopRound(&item, done.err)
				return toolRound{denied: denied}, done.err
			}
			if !record(item, done.result) {
				cancelRunning()
				return toolRound{denied: denied}, nil
			}

		case <-ctx.Done():
			cancelRunning()
			return toolRound{denied: denied}, nil
		}
	}
	return toolRound{responses: responses, complete: true, denied: denied, stopped: stopRequested}, nil
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
