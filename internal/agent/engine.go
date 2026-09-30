// Package agent drives the remote assistant turn loop and emits fine-grained
// events on a channel. It imports only the assistant client and has no Bubble
// Tea / UI dependency, so it is reusable by a future headless surface and
// testable without a program.
package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/DataDog/bits-cli/internal/assistant"
)

var (
	ErrMaxTurns               = errors.New("exceeded max turns")
	ErrHistoryUnsupported     = errors.New("backend does not support loading conversation history")
	ErrCurrentUserUnsupported = errors.New("backend does not support loading the current user")
	ErrOperationActive        = errors.New("another conversation operation is active")
	ErrNoPendingTools         = errors.New("no resumable client tools")
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

const maxQueuedCommands = 64

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
	continuation           *toolContinuation
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
	stop       bool
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
		commands:      make(chan toolCommand, maxQueuedCommands),
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
	return e.beginTurnWithCalls(ctx, in, nil)
}

func (e *Engine) beginTurnWithCalls(ctx context.Context, in TurnInput, resumed []ToolCall) turnOperation {
	if !e.begin() {
		return completedTurnOperation(ErrOperationActive)
	}
	e.continuation = nil
	generation := e.operationGeneration.Load()
	events := make(chan Event, 64)
	completion := make(chan turnCompletion, 1)
	go e.run(ctx, in, resumed, events, completion, generation)
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

// StopTools cancels the active client-tool round and persists its cancellation
// responses before ending the turn. Cancelling the turn context instead leaves
// unanswered calls available for a later process to resume.
func (e *Engine) StopTools() bool {
	return e.command(toolCommand{generation: e.operationGeneration.Load(), stop: true})
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
	resumed []ToolCall,
	out chan<- Event,
	completionOut chan<- turnCompletion,
	generation uint64,
) {
	completion := turnCompletion{}
	turnStart := len(e.transcript.Blocks())
	if len(resumed) > 0 {
		for i, block := range e.transcript.Blocks() {
			for _, call := range resumed {
				if block.ToolCallID() == call.ID {
					turnStart = min(turnStart, i)
				}
			}
		}
	}
	defer func() {
		if ctx.Err() != nil {
			e.transcript.CancelUnfinishedTools(turnStart)
			// Cancellation makes send's context-aware select unavailable, but this
			// terminal snapshot must not be dropped. It is sent after all queued
			// events and callers drain the channel after cancellation. Always flush,
			// even if a worker completed while cancellation prevented its event send.
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

	// A restored tool call is already in the transcript; only a new turn adds a
	// user block before contacting the backend.
	if len(resumed) == 0 {
		e.transcript.AppendUser(in.Message)
		if !send(Event{Kind: EventTranscript, Transcript: e.snapshot(), Origin: TranscriptOriginLocal}) {
			return
		}
	}

	var next any = in.Message
	convID := e.ConversationID()

	// request sends one round and collects the client calls it pauses on. It
	// returns false once the turn has ended.
	request := func(emit func(Event) bool, opts assistant.SendOptions) ([]ToolCall, bool) {
		var calls []ToolCall
		fold := func(msg assistant.Message) bool {
			if msg.Results != nil && msg.Results.Usage != nil {
				completion.Usage = cloneUsage(msg.Results.Usage)
			}
			b, ok := e.transcript.AppendMessage(msg)
			if ok {
				// Some providers omit is_client_side on a streamed start, then
				// supply it only implicitly with the final client_tool_call. The
				// registered tool set is authoritative for this turn, so recover
				// that missing identity without overriding an explicit false server
				// marker.
				if msg.Content.Type == assistant.ContentToolCallStarted && msg.Content.Tool != nil &&
					!msg.Content.Tool.HasClientSide && tools.Has(msg.Content.Tool.ToolName) {
					e.transcript.MarkToolClientSide(msg.Content.Tool.ToolCallID)
				}
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

		id, err := e.backend.Send(ctx, next, opts, func(ar assistant.AssistantResponse) error {
			msg := ar.Data.Attributes.StructuredMessage
			// A client_tool_call pauses the stream until we answer it; collect it
			// for runTools.
			if msg.Content.Type == assistant.ContentClientToolCall {
				calls = append(calls, toolCallOf(msg.Content))
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
					return nil, false
				}
				if id != "" {
					if !emit(Event{Kind: EventConversation, ConvID: id}) {
						return nil, false
					}
				}
				emit(Event{Kind: EventError, Err: err, BackendFailure: true})
				return nil, false
			}
		}
		if ctx.Err() != nil {
			return nil, false // cancelled: end the turn quietly
		}

		convID = id
		e.opts.ConversationID = convID
		emit(Event{Kind: EventConversation, ConvID: convID})

		if len(calls) == 0 {
			if finalizeAndEmit(emit) {
				completion.Completed = emit(Event{Kind: EventTurnDone})
			}
			return nil, false
		}
		return calls, true
	}

	for round := 1; round <= maxTurns; round++ {
		emit := emitAtRound(round)
		opts := e.opts
		opts.ConversationID = convID
		opts.ClientTools = defs
		opts.Context = in.Context
		opts.StreamToolCallInput = opts.StreamToolCallInput || tools.NeedsStreamedInput()

		calls := resumed
		resumed = nil
		if calls == nil {
			var ok bool
			if calls, ok = request(emit, opts); !ok {
				return
			}
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
		switch toolRound.outcome {
		case roundAborted:
			return // cancelled: end the turn quietly
		case roundStopped:
			// The round was answered on the wire; discard the follow-up and end the turn.
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
		case roundAnswered:
			next = toolRound.responses
		}
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

// roundOutcome is how a client-tool round ended.
type roundOutcome uint8

const (
	roundAborted  roundOutcome = iota // the turn was cancelled; nothing is answered
	roundAnswered                     // send the responses as the next round
	roundStopped                      // answer on the wire, drain follow-ups, end the turn
)

// toolRound is one client-tool round trip's outcome and wire responses.
// Responses are set unless the round was aborted.
type toolRound struct {
	outcome   roundOutcome
	responses []assistant.ClientToolResponse
	denied    bool
}

func cloneUsage(usage *assistant.Usage) *assistant.Usage {
	if usage == nil {
		return nil
	}
	copied := *usage
	if usage.InputTokens != nil {
		copied.InputTokens = new(*usage.InputTokens)
	}
	if usage.OutputTokens != nil {
		copied.OutputTokens = new(*usage.OutputTokens)
	}
	if usage.TimeToFirstChunkMs != nil {
		copied.TimeToFirstChunkMs = new(*usage.TimeToFirstChunkMs)
	}
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
	e.continuation = nil
	clear(e.sessionGrants)
	return nil
}

// SetPermissionsMode changes the process tool mode while the engine is idle.
// The engine owns session grants, so returning to manual clears them under the
// same operation gate used by turns and conversation restores.
func (e *Engine) SetPermissionsMode(tools *ToolSet, mode PermissionsMode) error {
	if !e.active.CompareAndSwap(false, true) {
		return ErrOperationActive
	}
	defer e.active.Store(false)
	if tools == nil {
		return fmt.Errorf("tool set is nil")
	}
	if err := tools.SetPermissionsMode(mode); err != nil {
		return err
	}
	if mode == ModeManual {
		clear(e.sessionGrants)
	}
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
	e.continuation = continuationFromHistory(resp.Data.Attributes.Messages)
	e.transcript.FinalizeAll()
	if snapshot := e.snapshot(); len(snapshot.Blocks) > 0 {
		send(Event{Kind: EventTranscript, Transcript: snapshot, Origin: TranscriptOriginRestore})
	}
}

// callStatus is where one client tool call is within its round.
type callStatus int

const (
	callQueued callStatus = iota // not yet dispatched
	callAwaitingApproval
	callRunning
	callResolved // its wire response is recorded
)

// roundCall is the engine goroutine's state for one client tool call.
type roundCall struct {
	call     ToolCall
	status   callStatus
	key      ApprovalKey                  // set while awaiting approval
	cancel   context.CancelFunc           // set once launched
	response assistant.ClientToolResponse // set once resolved
}

func unresolved(c *roundCall) bool { return c.status != callResolved }

type toolDone struct {
	call   *roundCall
	result ToolResult
	err    error
}

// toolRoundState is one runTools invocation. Only the engine goroutine touches
// it; handler goroutines report back through results.
type toolRoundState struct {
	e             *Engine
	ctx           context.Context // parent of every handler context
	cancelRound   context.CancelFunc
	tools         *ToolSet
	send          func(Event) bool
	onDeny        DenyPolicy
	calls         []*roundCall // in wire order
	byID          map[string]*roundCall
	results       chan toolDone
	denied        bool
	stopRequested bool
}

// Workers only execute handlers. The engine goroutine owns approvals,
// transcript changes, and response ordering.
func (e *Engine) runTools(
	ctx context.Context,
	tools *ToolSet,
	calls []ToolCall,
	generation uint64,
	send func(Event) bool,
	onDeny DenyPolicy,
) (toolRound, error) {
	roundCtx, cancelRound := context.WithCancel(ctx)
	defer cancelRound()
	r := &toolRoundState{
		e:           e,
		ctx:         roundCtx,
		cancelRound: cancelRound,
		tools:       tools,
		send:        send,
		onDeny:      onDeny,
		calls:       make([]*roundCall, len(calls)),
		byID:        make(map[string]*roundCall, len(calls)),
		results:     make(chan toolDone, len(calls)),
	}
	for i, call := range calls {
		if call.ID == "" {
			return toolRound{}, errors.New("client tool call has no id")
		}
		if _, exists := r.byID[call.ID]; exists {
			return toolRound{}, fmt.Errorf("duplicate client tool call id %q", call.ID)
		}
		r.calls[i] = &roundCall{call: call}
		r.byID[call.ID] = r.calls[i]
	}

	// ok turns false once an event can no longer be delivered or the turn is
	// cancelled; the round then ends without responses.
	ok := true
	for _, c := range r.calls {
		if ok = r.dispatch(c); !ok {
			break
		}
	}
	for ok && slices.ContainsFunc(r.calls, unresolved) {
		select {
		case command := <-e.commands:
			if command.generation == generation {
				ok = r.handleCommand(command)
			}

		case done := <-r.results:
			c := done.call
			if c.status == callResolved {
				continue // cancelled or stopped before its handler returned
			}
			c.cancel()
			switch {
			case done.err == nil:
				ok = r.resolve(c, done.result)
			case r.stopRequested || r.denied:
				// Record the failure so the round's batch still gets answered.
				ok = r.resolve(c, toolFailedResult(done.err))
			default:
				r.stopRound(c, done.err)
				return toolRound{denied: r.denied}, done.err
			}

		case <-ctx.Done():
			ok = false
		}
	}
	if !ok {
		return toolRound{outcome: roundAborted, denied: r.denied}, nil
	}
	round := toolRound{
		outcome:   roundAnswered,
		responses: make([]assistant.ClientToolResponse, len(r.calls)),
		denied:    r.denied,
	}
	if r.stopRequested {
		round.outcome = roundStopped
	}
	for i, c := range r.calls {
		round.responses[i] = c.response
	}
	return round, nil
}

// dispatch decides how a call starts: answered locally, parked for approval,
// or launched.
func (r *toolRoundState) dispatch(c *roundCall) bool {
	permissions := r.tools.snapshotPermissions()
	if c.call.Name != assistant.ApprovalRequestTool {
		requirement, needsApproval := permissions.approval(c.call)
		_, granted := r.e.sessionGrants[requirement.Key]
		switch {
		case !needsApproval || granted:
			return r.launch(c, false)
		case r.stopRequested:
			return r.resolve(c, cancelledResult())
		case permissions.deniesGates():
			return r.deny(c, modeDeniedResult())
		default:
			return r.awaitApproval(c, requirement)
		}
	}

	// The server write gate never runs a handler; it is answered locally.
	if r.stopRequested {
		return r.resolve(c, cancelledResult())
	}
	if _, err := parseServerGateInput(c.call); err != nil {
		return r.deny(c, invalidServerGateResult())
	}
	if permissions.approvesServerGate() {
		return r.resolve(c, approvedResult())
	}
	requirement, needsApproval := permissions.approval(c.call)
	if !needsApproval {
		return r.deny(c, serverDeniedResult())
	}
	if _, granted := r.e.sessionGrants[requirement.Key]; granted {
		return r.resolve(c, approvedResult())
	}
	if permissions.deniesGates() {
		return r.deny(c, modeDeniedResult())
	}
	return r.awaitApproval(c, requirement)
}

func (r *toolRoundState) handleCommand(command toolCommand) bool {
	if command.stop {
		r.stopRequested = true
		r.stopRound(nil, nil)
		return true
	}
	c, ok := r.byID[command.id]
	if !ok {
		return true
	}
	if command.cancel {
		switch c.status {
		case callAwaitingApproval:
			return r.resolve(c, cancelledResult())
		case callRunning:
			c.cancel()
			return r.resolve(c, cancelledResult())
		default:
			return true
		}
	}

	if c.status != callAwaitingApproval {
		return true
	}
	if command.decision == ApprovalDeny {
		return r.deny(c, approvalDeniedResult(c.call))
	}
	if command.decision == ApprovalAllowSession {
		r.e.sessionGrants[c.key] = struct{}{}
	}
	if !r.approve(c) {
		return false
	}
	for _, sibling := range r.calls {
		if sibling.status != callAwaitingApproval {
			continue
		}
		if _, granted := r.e.sessionGrants[sibling.key]; granted && !r.approve(sibling) {
			return false
		}
	}
	return true
}

func (r *toolRoundState) awaitApproval(c *roundCall, requirement ApprovalRequirement) bool {
	c.status = callAwaitingApproval
	c.key = requirement.Key
	if _, updated := r.e.transcript.MarkAwaitingApproval(c.call.ID, requirement.Prompt); !updated {
		return true
	}
	return r.send(Event{Kind: EventTranscript, Transcript: r.e.snapshot()})
}

func (r *toolRoundState) approve(c *roundCall) bool {
	if c.call.Name == assistant.ApprovalRequestTool {
		return r.resolve(c, approvedResult())
	}
	return r.launch(c, true)
}

// deny answers the denial on the wire; siblings still resolve. Under DenyStop
// still-pending approvals are answered as cancelled and the round then ends
// without a follow-up round, while running siblings finish so their real
// results reach the wire batch.
func (r *toolRoundState) deny(c *roundCall, result ToolResult) bool {
	r.denied = true
	if !r.resolve(c, result) {
		return false
	}
	if r.onDeny != DenyStop {
		return true
	}
	r.stopRequested = true
	for _, sibling := range r.calls {
		if sibling.status == callAwaitingApproval && !r.resolve(sibling, cancelledResult()) {
			return false
		}
	}
	return true
}

func (r *toolRoundState) launch(c *roundCall, approved bool) bool {
	toolCtx, cancel := context.WithCancel(r.ctx)
	c.status = callRunning
	c.cancel = cancel
	if approved {
		if _, updated := r.e.transcript.MarkToolRunning(c.call.ID); updated {
			if !r.send(Event{Kind: EventTranscript, Transcript: r.e.snapshot()}) {
				return false
			}
		}
	}
	call := c.call
	go func() {
		result, err := r.tools.Run(toolCtx, call)
		r.results <- toolDone{call: c, result: result, err: err}
	}()
	return true
}

// stopRound cancels every handler and answers each unresolved call as
// cancelled, or as failed for the call whose handler failed.
func (r *toolRoundState) stopRound(failed *roundCall, err error) {
	r.cancelRound()
	for _, c := range r.calls {
		if c.status == callResolved {
			continue
		}
		result := cancelledResult()
		if c == failed {
			result = toolFailedResult(err)
		}
		r.resolve(c, result)
	}
}

// resolve records the call's wire response once and publishes its result.
func (r *toolRoundState) resolve(c *roundCall, result ToolResult) bool {
	if c.status == callResolved {
		return true
	}
	c.status = callResolved
	c.response = toolResponse(c.call, result)
	result = r.tools.NormalizeResult(c.call, result)
	if _, updated := r.e.transcript.MarkToolExecuted(c.call.ID, result); !updated {
		return true
	}
	return r.send(Event{Kind: EventTranscript, Transcript: r.e.snapshot()})
}

func toolFailedResult(err error) ToolResult {
	return ToolResult{Title: "Tool failed", Output: err.Error(), IsError: true}
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
