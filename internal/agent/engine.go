// Package agent drives the remote assistant turn loop and emits fine-grained
// events on a channel. It imports only the assistant client and has no Bubble
// Tea / UI dependency, so it is reusable by a future headless surface and
// testable without a program.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/DataDog/bits-cli/internal/assistant"
)

var (
	ErrMaxTurns               = errors.New("exceeded max turns")
	ErrHistoryUnsupported     = errors.New("backend does not support loading conversation history")
	ErrCurrentUserUnsupported = errors.New("backend does not support loading the current user")
	ErrOperationActive        = errors.New("another conversation operation is active")
)

// Backend is the minimal transport the engine drives. *assistant.Client
// satisfies it; tests can substitute a fake.
type Backend interface {
	Send(ctx context.Context, message any, opts assistant.SendOptions,
		fn func(assistant.AssistantResponse) error) (string, error)
}

type entitySearchBackend interface {
	SearchEntities(context.Context, assistant.SearchEntitiesInput) (assistant.SearchEntitiesResponse, error)
}

// HistoryBackend adds conversation history loading
type HistoryBackend interface {
	ConversationHistory(ctx context.Context, in assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error)
}

// CurrentUserBackend adds current-user profile loading for local status
// surfaces. Backends without an authenticated Datadog principal may omit it.
type CurrentUserBackend interface {
	CurrentUser(context.Context) (assistant.CurrentUser, error)
}

// EventKind discriminates the events the engine streams for a turn.
type EventKind int

const (
	EventTranscript   EventKind = iota // full transcript snapshot; read Transcript
	EventUsage                         // token usage update; read Usage
	EventConversation                  // server-assigned/confirmed conversation id
	EventTurnDone                      // the turn completed with no pending tool calls
	EventError                         // the turn failed
)

// TranscriptOrigin identifies what caused an EventTranscript snapshot.
type TranscriptOrigin int

const (
	TranscriptOriginLocal   TranscriptOrigin = iota // local user echo, before a backend send
	TranscriptOriginRemote                          // a backend turn or its client-tool work
	TranscriptOriginRestore                         // restored conversation history
)

// TranscriptSnapshot is a read-only view of the complete renderable transcript.
// Its query methods centralize common block traversal and may gain indexes or
// cached values without changing event consumers.
type TranscriptSnapshot struct {
	Blocks []Block
}

// HasStreamingContent reports whether an assistant text or reasoning block is
// still open.
func (s TranscriptSnapshot) HasStreamingContent() bool {
	for _, block := range s.Blocks {
		if !block.Complete && (block.Kind == assistant.KindText || block.Kind == assistant.KindReasoning) {
			return true
		}
	}
	return false
}

// PendingApprovals returns tool blocks awaiting an approval decision, in
// transcript order.
func (s TranscriptSnapshot) PendingApprovals() []Block {
	var pending []Block
	for _, block := range s.Blocks {
		if block.Tool != nil && block.Tool.Status == ToolAwaitingApproval {
			pending = append(pending, block)
		}
	}
	return pending
}

// UserPrompts returns the text of the user messages, in transcript order.
// Blank messages (an attachment-only submit is stored as " ") are skipped;
// repeated messages are kept.
func (s TranscriptSnapshot) UserPrompts() []string {
	var prompts []string
	for _, block := range s.Blocks {
		if block.Role != assistant.RoleUser || block.Kind != assistant.KindText || block.Markdown == nil {
			continue
		}
		if strings.TrimSpace(block.Markdown.Content) != "" {
			prompts = append(prompts, block.Markdown.Content)
		}
	}
	return prompts
}

// Event is one thing that happened during a turn. It is a plain value carried
// on a channel, with only the fields relevant to Kind populated.
type Event struct {
	Kind           EventKind
	Transcript     TranscriptSnapshot // for EventTranscript; read-only full transcript snapshot
	Origin         TranscriptOrigin   // for EventTranscript
	Usage          *assistant.Usage   // for EventUsage
	ConvID         string             // for EventConversation
	Err            error              // for EventError
	BackendFailure bool               // EventError originated at the Assistant backend boundary
	// Round is the 1-based backend send the event belongs to (0 for
	// out-of-turn events). Drained rounds after a denial count but emit
	// no content events.
	Round int
}

// maxTurns caps the client-tool loop so a misbehaving backend can't spin
// forever.
const maxTurns = 150

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
	runtimeStatus          RuntimeStatus
	transcript             *Transcript
	previousConversationID string
	commands               chan toolCommand
	sessionGrants          map[ApprovalKey]struct{}
	active                 atomic.Bool
	operationGeneration    atomic.Uint64
}

// RuntimeStatus is the non-secret request and backend state needed by local
// status surfaces.
type RuntimeStatus struct {
	Profile assistant.Profile
	Model   string
	Backend assistant.BackendStatus
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
	profile := opts.Profile
	if profile == "" {
		profile = assistant.DefaultProfile
	}
	runtimeStatus := RuntimeStatus{Profile: profile, Model: opts.Model}
	if provider, ok := b.(interface {
		BackendStatus() assistant.BackendStatus
	}); ok {
		runtimeStatus.Backend = provider.BackendStatus()
	}
	return &Engine{
		backend:       b,
		opts:          opts,
		runtimeStatus: runtimeStatus,
		transcript:    NewTranscript(),
		commands:      make(chan toolCommand, 64),
		sessionGrants: make(map[ApprovalKey]struct{}),
	}
}

// Site returns the Assistant API site of the active backend. Backends without
// a web counterpart, such as the demo backend, return an empty string.
func (e *Engine) Site() string {
	return e.runtimeStatus.Backend.Site
}

// SearchEntities forwards autocomplete queries when the backend supports the
// Datadog entity suggestions contract. Search does not claim the serialized
// conversation-operation gate.
func (e *Engine) SearchEntities(ctx context.Context, in assistant.SearchEntitiesInput) (assistant.SearchEntitiesResponse, error) {
	backend, ok := e.backend.(entitySearchBackend)
	if !ok {
		return assistant.SearchEntitiesResponse{}, errors.New("entity search is unavailable")
	}
	return backend.SearchEntities(ctx, in)
}

type TurnInput struct {
	Message string
	Tools   *ToolSet
	// Context belongs to this independent user turn. The engine resends it on
	// client-tool continuations, but never stores it in its long-lived options.
	Context *assistant.AssistantContext
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

// StartTurn preserves the event-only API used by interactive surfaces. Callers
// must continue draining the returned channel until it closes after cancelling
// the turn context.
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
	turnStart := len(e.transcript.Blocks())
	defer func() {
		if ctx.Err() != nil && e.transcript.CancelUnfinishedTools(turnStart) {
			// Cancellation makes send's context-aware select unavailable, but this
			// terminal snapshot must not be dropped. It is sent after all queued
			// events and callers drain the channel after cancellation.
			out <- Event{Kind: EventTranscript, Origin: TranscriptOriginRemote, Transcript: e.snapshot()}
		}
		if completion.Err == nil && !completion.Completed && ctx.Err() != nil {
			completion.Err = ctx.Err()
		}
		completion.ConversationID = e.opts.ConversationID
		completion.Blocks = e.snapshot().Blocks
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
	finalizeAndEmit := func(emit func(Event) bool) bool {
		if !e.transcript.FinalizeAll() {
			return true
		}
		return emit(Event{Kind: EventTranscript, Transcript: e.snapshot()})
	}
	emitAtRound := func(round int) func(Event) bool {
		return func(ev Event) bool {
			ev.Round = round
			if ev.Kind == EventTranscript {
				ev.Origin = TranscriptOriginRemote
			}
			return send(ev)
		}
	}

	// The user's turn opens the transcript; the engine owns the user block too.
	e.transcript.AppendUser(in.Message)
	if !send(Event{Kind: EventTranscript, Transcript: e.snapshot(), Origin: TranscriptOriginLocal}) {
		return
	}

	var next any = in.Message
	convID := e.ConversationID()

	for round := 1; round <= maxTurns; round++ {
		var calls []assistant.Content

		emit := emitAtRound(round)
		fold := func(msg assistant.Message) bool {
			if msg.Results != nil && msg.Results.Usage != nil {
				completion.Usage = cloneUsage(msg.Results.Usage)
			}
			b, ok := e.transcript.AppendMessage(msg)
			if ok {
				// Reducers run in the engine goroutine immediately after the wire
				// update is folded. This ordering lets the emitted snapshot carry
				// the reducer's state and keeps filesystem-aware reducers ahead of
				// client-tool execution.
				e.reduceToolInput(ctx, tools, msg, b)
				if !emit(Event{Kind: EventTranscript, Transcript: e.snapshot()}) {
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
		opts.Context = in.Context
		opts.StreamToolCallInput = opts.StreamToolCallInput || tools.NeedsStreamedInput()

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
				if !finalizeAndEmit(emit) {
					return
				}
				if id != "" {
					if !emit(Event{Kind: EventConversation, ConvID: id}) {
						return
					}
				}
				emit(Event{Kind: EventError, Err: err, BackendFailure: true})
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
			if !finalizeAndEmit(emit) {
				return
			}
			completion.Completed = emit(Event{Kind: EventTurnDone})
			return
		}
		toolRound, err := e.runTools(ctx, tools, calls, generation, emit, in.OnDeny)
		if err != nil {
			completion.Err = err
			completion.Denied = completion.Denied || toolRound.denied
			if !finalizeAndEmit(emit) {
				return
			}
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
					emitTerminal := emitAtRound(terminalRound)
					if !finalizeAndEmit(emitTerminal) {
						return
					}
					send(Event{Kind: EventError, Round: terminalRound, Err: err, BackendFailure: true})
					return
				}
				if ctx.Err() != nil {
					return // cancelled: end the turn quietly
				}
				emitTerminal := emitAtRound(terminalRound)
				if !finalizeAndEmit(emitTerminal) {
					return
				}
				completion.Completed = emitTerminal(Event{Kind: EventTurnDone})
			}
			return
		}
		if !toolRound.complete {
			if ctx.Err() == nil {
				if !finalizeAndEmit(emit) {
					return
				}
				completion.Completed = emit(Event{Kind: EventTurnDone})
			}
			return
		}
		next = toolRound.responses
	}
	completion.Err = ErrMaxTurns
	if !finalizeAndEmit(emitAtRound(maxTurns)) {
		return
	}
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
func (e *Engine) snapshot() TranscriptSnapshot {
	blocks := e.transcript.Blocks()
	snapshot := TranscriptSnapshot{Blocks: make([]Block, len(blocks))}
	copy(snapshot.Blocks, blocks)
	return snapshot
}

// ConversationID reports the conversation the engine is bound to. It is set
// from SendOptions and updated as turns run; empty means a new conversation.
func (e *Engine) ConversationID() string { return e.opts.ConversationID }

// Status reports the immutable effective request configuration and non-secret
// backend metadata captured when the engine was constructed. An empty model
// remains empty because only the server knows which effective model it selected.
func (e *Engine) Status() RuntimeStatus {
	return e.runtimeStatus
}

// CurrentUser loads the authenticated Datadog identity when the backend
// exposes that optional capability.
func (e *Engine) CurrentUser(ctx context.Context) (assistant.CurrentUser, error) {
	backend, ok := e.backend.(CurrentUserBackend)
	if !ok {
		return assistant.CurrentUser{}, ErrCurrentUserUnsupported
	}
	return backend.CurrentUser(ctx)
}

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
		send(Event{Kind: EventError, Err: err, BackendFailure: true})
		return
	}
	if resp == nil {
		return
	}
	for _, msg := range resp.Data.Attributes.Messages {
		e.transcript.AppendMessage(msg)
	}
	e.transcript.FinalizeAll()
	if snapshot := e.snapshot(); len(snapshot.Blocks) > 0 {
		send(Event{Kind: EventTranscript, Transcript: snapshot, Origin: TranscriptOriginRestore})
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

	completeTool := func(item pendingTool, result ToolResult) bool {
		if resolved[item.call.ID] {
			return true
		}
		resolved[item.call.ID] = true
		outstanding--
		responses[item.index] = toolResponse(item.call, result)
		result = tools.NormalizeResult(item.call, result)
		_, updated := e.transcript.MarkToolExecuted(item.call.ID, result)
		if !updated {
			return true
		}
		return send(Event{Kind: EventTranscript, Transcript: e.snapshot()})
	}
	launch := func(item pendingTool, approved bool) bool {
		toolCtx, cancel := context.WithCancel(ctx)
		running[item.call.ID] = cancel
		if approved {
			_, updated := e.transcript.MarkToolRunning(item.call.ID)
			if updated {
				if !send(Event{Kind: EventTranscript, Transcript: e.snapshot()}) {
					cancel()
					delete(running, item.call.ID)
					return false
				}
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
		for _, ordered := range work {
			item, ok := pending[ordered.call.ID]
			if !ok {
				continue
			}
			delete(pending, item.call.ID)
			if !completeTool(item, cancelledResult()) {
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
			completeTool(item, result)
		}
	}
	denyServerGate := func(item pendingTool, result ToolResult) bool {
		denied = true
		if !completeTool(item, result) {
			cancelRunning()
			return false
		}
		if onDeny == DenyStop {
			stopRequested = true
			if !stopPendingAfterDenial() {
				return false
			}
		}
		return true
	}

	for _, item := range work {
		permissions := tools.snapshotPermissions()
		if item.call.Name == assistant.ApprovalRequestTool {
			if stopRequested {
				if !completeTool(item, cancelledResult()) {
					cancelRunning()
					return toolRound{denied: denied}, nil
				}
				continue
			}
			if _, err := parseServerGateInput(item.call); err != nil {
				if !denyServerGate(item, invalidServerGateResult()) {
					return toolRound{denied: denied}, nil
				}
				continue
			}
		}
		if item.call.Name == assistant.ApprovalRequestTool && permissions.approvesServerGate() {
			if !completeTool(item, approvedResult()) {
				cancelRunning()
				return toolRound{denied: denied}, nil
			}
			continue
		}
		requirement, needsApproval := permissions.approval(item.call)
		_, granted := e.sessionGrants[requirement.Key]
		if needsApproval && !granted {
			if stopRequested {
				if !completeTool(item, cancelledResult()) {
					cancelRunning()
					return toolRound{denied: denied}, nil
				}
				continue
			}
			item.key = requirement.Key
			pending[item.call.ID] = item
			_, updated := e.transcript.MarkAwaitingApproval(item.call.ID, requirement.Prompt)
			if updated {
				if !send(Event{Kind: EventTranscript, Transcript: e.snapshot()}) {
					cancelRunning()
					return toolRound{denied: denied}, nil
				}
			}
			continue
		}
		if item.call.Name == assistant.ApprovalRequestTool {
			if !needsApproval {
				if !denyServerGate(item, serverDeniedResult()) {
					return toolRound{denied: denied}, nil
				}
				continue
			}
			if !completeTool(item, approvedResult()) {
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
					if !completeTool(item, cancelledResult()) {
						cancelRunning()
						return toolRound{denied: denied}, nil
					}
					continue
				}
				if cancel, ok := running[command.id]; ok {
					cancel()
					delete(running, command.id)
					if !completeTool(byID[command.id], cancelledResult()) {
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
				denied = true
				if !completeTool(item, approvalDeniedResult(item.call)) {
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
			approve := func(item pendingTool) bool {
				if item.call.Name == assistant.ApprovalRequestTool {
					return completeTool(item, approvedResult())
				}
				return launch(item, true)
			}
			if !approve(item) {
				cancelRunning()
				return toolRound{denied: denied}, nil
			}
			for _, ordered := range work {
				sibling, ok := pending[ordered.call.ID]
				if !ok {
					continue
				}
				if _, granted := e.sessionGrants[sibling.key]; !granted {
					continue
				}
				delete(pending, sibling.call.ID)
				if !approve(sibling) {
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
				if stopRequested || denied {
					// Record the failure so the round's batch still gets answered.
					if !completeTool(item, ToolResult{Title: "Tool failed", Output: done.err.Error(), IsError: true}) {
						cancelRunning()
						return toolRound{denied: denied}, nil
					}
					continue
				}
				stopRound(&item, done.err)
				return toolRound{denied: denied}, done.err
			}
			if !completeTool(item, done.result) {
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

// reduceToolInput invokes a registered reducer for the client-side tool
// updates that can carry streamed input. The final client_tool_call is
// explicitly client-side even though its payload does not carry the
// IsClientSide marker used by tool_call_started. Server-side tool_call events
// are intentionally excluded, even when their name matches a local tool.
func (e *Engine) reduceToolInput(
	ctx context.Context,
	tools *ToolSet,
	msg assistant.Message,
	b Block,
) (Block, bool) {
	if tools == nil || b.Tool == nil || b.ToolCallID() == "" || msg.Content.Tool == nil {
		return b, false
	}
	tp := msg.Content.Tool
	if tp.ToolCallID == "" {
		return b, false
	}
	update := ToolInputUpdate{
		ToolCallID:       b.ToolCallID(),
		Name:             b.Tool.Name,
		RawPrefix:        b.Tool.InputPartial,
		PreviewTruncated: b.Tool.InputPreviewTruncated,
	}
	switch msg.Content.Type {
	case assistant.ContentToolCallStarted:
		if !tp.IsClientSide {
			return b, false
		}
	case assistant.ContentToolCallInputDelta:
		// Deltas inherit client-side provenance from their aggregate started
		// block. Some payloads also repeat the marker, so accept either.
		if !b.Tool.IsClientSide && !tp.IsClientSide {
			return b, false
		}
		update.Delta = tp.PartialJSON
	case assistant.ContentClientToolCall:
		update.HasFinalInput = tp.Metadata != nil
		if update.HasFinalInput {
			update.FinalInput = tp.Metadata.Input
		}
	default:
		return b, false
	}
	next, ok := tools.ReduceInput(ctx, update, b.Tool.RenderState)
	if !ok {
		return b, false
	}
	return e.transcript.SetToolRenderState(b.ToolCallID(), next)
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
	if result.Display != "" {
		response.Content = &assistant.MarkdownContent{
			Type:    assistant.ContentMarkdownFragment,
			Content: result.Display,
		}
	}
	if result.IsError {
		response.Status = assistant.ToolStatusError
	}
	return response
}
