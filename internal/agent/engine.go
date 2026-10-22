// Package agent drives the remote assistant turn loop and emits fine-grained
// events on a channel. It composes local context with assistant requests without
// a terminal UI dependency, so interactive and headless surfaces share the engine.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent/env"
	"github.com/datadog-labs/bits-cli/internal/assistant"
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

// UserPrompts returns the user messages in transcript order, repeats kept.
// Blank text (an attachment-only submit is stored as " ") is returned empty,
// and a message with neither text nor context is skipped.
func (s TranscriptSnapshot) UserPrompts() []UserPrompt {
	var prompts []UserPrompt
	for _, block := range s.Blocks {
		if block.Role != assistant.RoleUser || block.Kind != assistant.KindText || block.Markdown == nil {
			continue
		}
		text := block.Markdown.Content
		if strings.TrimSpace(text) == "" {
			text = ""
		}
		if text != "" || len(block.Context) > 0 {
			prompts = append(prompts, UserPrompt{Text: text, Context: block.Context})
		}
	}
	return prompts
}

// UserPrompt is a prompt the user sent and the entities it was sent with.
// Context shares immutable transcript data, including nested definition values.
type UserPrompt struct {
	Text    string
	Context []assistant.ContextEntity
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
// and previousConversationID; the permission gate carries its own lock. State crosses goroutine boundaries
// through copied events/results and channel synchronization. Only Decide,
// CancelTool, StopTools, SetPermissionsMode, GrantSessionFor, ClientSkills, and explicitly
// atomic methods may be called concurrently with an operation. Do not add a mutex around Engine state without first changing this
// ownership model and identifying accesses that its existing operation gate and
// channel boundaries do not order.
type Engine struct {
	backend                Backend
	opts                   assistant.SendOptions
	runtimeStatus          RuntimeStatus
	transcript             *Transcript
	continuation           []assistant.Content
	previousConversationID string
	// delivery is the context the current conversation is known to carry.
	delivery contextDelivery
	commands chan toolCommand
	gate     permissionGate
	// permissionsChanged wakes a running round to re-gate its parked calls
	// after a mode switch or a grant. It holds at most one signal: rounds
	// re-read the current permissions anyway.
	permissionsChanged  chan struct{}
	active              atomic.Bool
	operationGeneration atomic.Uint64
	instructions        *env.Instructions
	clientSkills        *env.Skills
	userContext         func(context.Context) string
}

// RuntimeStatus is the non-secret request and backend state needed by local
// status surfaces.
type RuntimeStatus struct {
	MultiAgent bool
	Profile    assistant.Profile
	Model      string
	Backend    assistant.BackendStatus
}

type toolCommand struct {
	generation uint64
	id         string
	decision   ApprovalDecision
	cancel     bool
	stop       bool
}

// Config holds engine-lifetime dependencies and backend request defaults.
// Messages, tools, attachments, and denial policy belong to TurnInput.
type Config struct {
	// ConversationID and MessageHistory initialize the conversation. Starting or
	// installing another conversation clears the initial message history.
	ConversationID string
	MessageHistory []json.RawMessage

	Model         string
	InferenceMode string
	Profile       assistant.Profile
	Referrer      string
	// SkillOverrides takes precedence over built-in defaults by skill name and source.
	SkillOverrides            []assistant.SkillOverride
	ExperimentalToolOverrides map[string]any
	EnableDebugMode           bool
	DebugTag                  string

	ProjectInstructions *env.Instructions
	// ClientSkills is a conversation-scoped registry owned exclusively by this engine.
	ClientSkills *env.Skills
	// UserContext is called on each new user turn to observe current environment
	// state. Unchanged context is suppressed after delivery. The renderer survives
	// conversation changes; delivery tracking does not. Nil disables it.
	UserContext func(context.Context) string
}

// New creates an engine from its backend and configuration. Local context is
// discovered lazily. Callers must not mutate configuration slices or maps after
// construction. Dynamic environment state is observed through UserContext.
func New(b Backend, config Config) *Engine {
	opts := assistant.SendOptions{
		ConversationID:            config.ConversationID,
		MessageHistory:            config.MessageHistory,
		Model:                     config.Model,
		InferenceMode:             config.InferenceMode,
		Profile:                   config.Profile,
		Referrer:                  config.Referrer,
		SkillOverrides:            mergeSkillOverrides(config.SkillOverrides),
		ExperimentalToolOverrides: config.ExperimentalToolOverrides,
		EnableDebugMode:           config.EnableDebugMode,
		DebugTag:                  config.DebugTag,
	}
	profile := opts.Profile
	if profile == "" {
		profile = assistant.DefaultProfile
	}
	_, multiAgent := b.(DuplexBackend)
	runtimeStatus := RuntimeStatus{Profile: profile, Model: opts.Model, MultiAgent: multiAgent}
	if provider, ok := b.(interface {
		BackendStatus() assistant.BackendStatus
	}); ok {
		runtimeStatus.Backend = provider.BackendStatus()
	}
	engine := &Engine{
		backend:            b,
		opts:               opts,
		runtimeStatus:      runtimeStatus,
		transcript:         NewTranscript(),
		commands:           make(chan toolCommand, maxQueuedCommands),
		permissionsChanged: make(chan struct{}, 1),
		instructions:       config.ProjectInstructions,
		clientSkills:       config.ClientSkills,
		userContext:        config.UserContext,
	}
	return engine
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
	// Skill is resolved and read inside this operation before contacting the backend.
	Skill *SkillInvocation
	Tools *ToolSet
	// Context is sent with Message only. The backend persists it on the user
	// message, so tool continuations must not repeat it.
	// Image file URLs are loaded and replaced with data URLs inside the turn.
	// Once submitted, callers must treat it and all nested data as immutable:
	// the engine retains its entities in transcript snapshots without copying.
	Context *assistant.AssistantContext
	// OnDeny is the turn's policy after a denial is answered on the wire.
	OnDeny DenyPolicy
}

// StartTurn runs one user turn and its client-tool round trips, streaming its
// events. Callers must drain the channel until it closes, including after
// cancelling the turn context. The channel closes only after the engine has
// released the operation, so Snapshot and ConversationID then report the
// turn's final state.
func (e *Engine) StartTurn(ctx context.Context, in TurnInput) <-chan Event {
	return e.startTurn(ctx, in, nil)
}

func (e *Engine) startTurn(ctx context.Context, in TurnInput, resumed []ToolCall) <-chan Event {
	if !e.begin() {
		return eventResult(Event{Kind: EventError, Err: ErrOperationActive})
	}
	e.continuation = nil
	generation := e.operationGeneration.Load()
	events := make(chan Event, 64)
	go e.run(ctx, in, resumed, events, generation)
	return events
}

// begin claims the engine for one operation, which must release it on completion.
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

// Decide answers a call parked for approval. It reports whether the answer
// was queued, not applied: the round drops it if the call is no longer parked.
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

// CancelTool cancels one call of the active round. Like Decide, it reports
// whether the command was queued.
func (e *Engine) CancelTool(toolCallID string) bool {
	return e.command(toolCommand{
		generation: e.operationGeneration.Load(),
		id:         toolCallID,
		cancel:     true,
	})
}

// StopTools cancels the active client-tool round and persists its cancellation
// responses before ending the turn. Cancelling the turn context instead leaves
// unanswered calls available for a later process to resume. It reports
// whether the stop was queued.
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
	generation uint64,
) {
	duplexTurn := false
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
		if duplexTurn {
			e.transcript.FinalizeAll()
		}
		if ctx.Err() != nil {
			if duplexTurn {
				e.settleUnfinishedTools(in.Tools, turnStart)
			}
			e.transcript.CancelUnfinishedTools(turnStart)
			// Cancellation makes send's context-aware select unavailable, but this
			// terminal snapshot must not be dropped. It is sent after all queued
			// events and callers drain the channel after cancellation. Always flush,
			// even if a worker completed while cancellation prevented its event send.
			out <- Event{Kind: EventTranscript, Origin: TranscriptOriginRemote, Transcript: e.snapshot()}
		}
		e.active.Store(false)
		close(out)
	}()

	tools := in.Tools

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
	// user block before contacting the backend. The block lists the context
	// exactly as it is sent.
	sentContext, imageNotices, imageErr := prepareImageContext(ctx, in.Context)
	if imageErr != nil {
		// Keep file references in the failed prompt so history can retry them.
		sentContext = in.Context
	}
	if len(resumed) == 0 {
		var sent []assistant.ContextEntity
		if sentContext != nil {
			sent = sentContext.Entities
		}
		e.transcript.AppendUser(in.Message, sent)
		if !send(Event{Kind: EventTranscript, Transcript: e.snapshot(), Origin: TranscriptOriginLocal}) {
			return
		}
	}

	// An invalid image or a skill that cannot be read fails the turn like a
	// backend error: the user block stays visible and nothing is sent.
	if imageErr != nil {
		send(Event{Kind: EventError, Err: imageErr})
		return
	}
	// The first request of a new turn is the prepared user message; later ones
	// answer client tool calls.
	var user *userRequest
	var responses []assistant.ClientToolResponse
	if len(resumed) == 0 {
		prepared, err := e.prepare(ctx, in, sentContext, resizeNotice(imageNotices))
		if err != nil {
			send(Event{Kind: EventError, Err: err})
			return
		}
		e.delivery.pending(prepared)
		user = &prepared
	}

	if backend, ok := e.backend.(DuplexBackend); ok && user != nil {
		capable := true
		if selector, ok := backend.(interface {
			CanStream(string, assistant.SendOptions) bool
		}); ok {
			capable = selector.CanStream(user.message, user.opts)
		}
		if capable {
			duplexTurn = true
			e.runExchange(ctx, in, &streamExchange{backend: backend, message: user.message, opts: user.opts}, user, generation, send)
			return
		}
	}

	// request sends one round and collects the client calls it pauses on. It
	// returns false once the turn has ended.
	request := func(emit func(Event) bool) ([]ToolCall, bool) {
		payload, opts := any(responses), e.sendOptions(tools)
		if user != nil {
			payload, opts = user.message, user.opts
		}
		var calls []ToolCall
		streamed := false

		id, err := e.backend.Send(ctx, payload, opts, func(ar assistant.AssistantResponse) error {
			streamed = true
			msg := ar.Data.Attributes.StructuredMessage
			// A client_tool_call pauses the stream until we answer it; collect it
			// for runTools.
			if msg.Content.Type == assistant.ContentClientToolCall {
				calls = append(calls, toolCallOf(msg.Content))
			}
			if !e.foldMessage(ctx, tools, msg, emit) {
				return ctx.Err()
			}
			return nil
		})
		// Send may discover a server-assigned ID before a later stream failure or
		// cancellation. Preserve it so every surface can report a resumable handle.
		if id != "" {
			e.opts.ConversationID = id
		}
		if user != nil && (streamed || err == nil) {
			e.delivery.delivered(*user)
		}
		if err != nil {
			// Context-derived cancellation remains quiet. Report an independent
			// backend failure when its event can be delivered; cancellation may
			// prevent that event from reaching the caller.
			if ctx.Err() == nil || !errors.Is(err, ctx.Err()) {
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

		e.opts.ConversationID = id
		emit(Event{Kind: EventConversation, ConvID: id})

		if len(calls) == 0 {
			if finalizeAndEmit(emit) {
				emit(Event{Kind: EventTurnDone})
			}
			return nil, false
		}
		return calls, true
	}

	for round := 1; round <= maxTurns; round++ {
		emit := emitAtRound(round)
		calls := resumed
		resumed = nil
		if calls == nil {
			var ok bool
			if calls, ok = request(emit); !ok {
				return
			}
		}
		toolRound, err := e.runTools(ctx, tools, calls, generation, emit, in.OnDeny)
		if err != nil {
			if !finalizeAndEmit(emit) {
				return
			}
			emit(Event{Kind: EventError, Err: err})
			return
		}
		switch toolRound.outcome {
		case roundAborted:
			return // cancelled: end the turn quietly
		case roundStopped:
			// The round was answered on the wire; discard the follow-up and end the turn.
			if ctx.Err() == nil {
				opts := e.sendOptions(tools)
				id, drainRounds, err := e.drainStoppedToolCalls(ctx, toolRound.responses, opts)
				terminalRound := round + drainRounds
				if id != "" {
					e.opts.ConversationID = id
				}
				if err != nil && (ctx.Err() == nil || !errors.Is(err, ctx.Err())) {
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
				emitTerminal(Event{Kind: EventTurnDone})
			}
			return
		case roundAnswered:
			user, responses = nil, toolRound.responses
		}
	}
	if !finalizeAndEmit(emitAtRound(maxTurns)) {
		return
	}
	send(Event{Kind: EventError, Round: maxTurns, Err: ErrMaxTurns})
}

// runExchange drives a turn whose backend traffic is one exchange.
func (e *Engine) runExchange(ctx context.Context, in TurnInput, ex exchange, user *userRequest, generation uint64, send func(Event) bool) {
	turnStart := len(e.transcript.Blocks())
	emit := func(event Event) bool {
		event.Round = 1
		if event.Kind == EventTranscript {
			event.Origin = TranscriptOriginRemote
		}
		return send(event)
	}
	_, err := e.runToolActor(ctx, in.Tools, nil, generation, emit, in.OnDeny, ex, user)
	if ctx.Err() != nil && (err == nil || errors.Is(err, ctx.Err())) {
		return // cancelled: end the turn quietly
	}
	settled := e.settleUnfinishedTools(in.Tools, turnStart)
	if e.transcript.FinalizeAll() || settled {
		if !emit(Event{Kind: EventTranscript, Transcript: e.snapshot()}) {
			return
		}
	}
	if err != nil {
		var backend backendError
		failure := errors.As(err, &backend)
		if failure {
			err = backend.error
		}
		emit(Event{Kind: EventError, Err: err, BackendFailure: failure})
		return
	}
	emit(Event{Kind: EventTurnDone})
}

// settleUnfinishedTools cancels the tool blocks from blocks[from:] still open,
// including provisional calls that never became execution-ready. Normalizing
// their results clears speculative UI state.
func (e *Engine) settleUnfinishedTools(tools *ToolSet, from int) bool {
	changed := false
	for _, block := range e.transcript.Blocks()[from:] {
		if block.Tool == nil || block.Tool.Status.IsTerminal() {
			continue
		}
		call := ToolCall{ID: block.ToolCallID(), Name: block.Tool.Name, Input: block.Tool.Input}
		result := tools.NormalizeResult(call, cancelledResult())
		_, updated := e.transcript.MarkToolExecuted(call.ID, result)
		changed = changed || updated
	}
	return changed
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
	e.resetContext()
	e.transcript = NewTranscript()
	e.continuation = nil
	e.gate.clearGrants()
	return nil
}

// SetPermissionsMode switches the process tool mode at once, even mid-turn.
// The switch governs every gate decision made after it: dispatching a call,
// re-gating parked calls (so a switch away from manual releases them), and
// applying a queued answer: once it returns, no gated call starts under the
// earlier mode. Calls already running are not stopped. Deny mode
// is absolute: no gated call runs, whatever grants or answers exist.
// Returning to manual revokes session grants.
func (e *Engine) SetPermissionsMode(tools *ToolSet, mode PermissionsMode) error {
	if tools == nil {
		return fmt.Errorf("tool set is nil")
	}
	if err := e.gate.setMode(tools, mode); err != nil {
		return err
	}
	e.permissionsUpdated()
	return nil
}

// GrantSessionFor stops asking, for the rest of the conversation, for calls
// sharing the approval key of the call parked as callID. It applies at once:
// a running round releases every parked call the grant covers, callID
// included. It reports false when no call is parked as callID.
func (e *Engine) GrantSessionFor(callID string) bool {
	if !e.gate.grantParked(callID) {
		return false
	}
	e.permissionsUpdated()
	return true
}

func (e *Engine) permissionsUpdated() {
	select {
	case e.permissionsChanged <- struct{}{}:
	default: // a wake-up is already pending
	}
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
	conversation, err := e.LoadConversation(ctx, conversationID)
	if ctx.Err() != nil {
		return // cancelled: end quietly
	}
	if err != nil {
		send(Event{Kind: EventError, Err: err, BackendFailure: !errors.Is(err, ErrHistoryUnsupported) && !errors.Is(err, ErrInvalidConversationID)})
		return
	}
	e.installConversation(conversation)
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
	gate     ApprovalRequirement          // the call's declared gate, set once dispatched
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
	// ex carries the round's calls and answers; nil for a legacy round.
	ex    exchange
	ended bool         // ex has ended; answers go nowhere
	user  *userRequest // acknowledged once the backend responds
	slots chan struct{}
}

// maxConcurrentTools bounds the handlers of an exchange running at once.
const maxConcurrentTools = 16

type exchangeResult struct {
	conversation string
	err          error
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
	return e.runToolActor(ctx, tools, calls, generation, send, onDeny, nil, nil)
}

func (e *Engine) runToolActor(
	ctx context.Context,
	tools *ToolSet,
	calls []ToolCall,
	generation uint64,
	send func(Event) bool,
	onDeny DenyPolicy,
	ex exchange,
	user *userRequest,
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
		calls:       make([]*roundCall, 0, len(calls)),
		byID:        make(map[string]*roundCall, len(calls)),
		results:     make(chan toolDone, len(calls)),
		ex:          ex,
		user:        user,
	}
	var events <-chan exchangeEvent
	var done <-chan exchangeResult
	if ex != nil {
		r.results = make(chan toolDone, maxConcurrentTools)
		r.slots = make(chan struct{}, maxConcurrentTools)
		events, done = r.startExchange()
		defer func() { r.joinExchange(done) }()
	}
	for _, call := range calls {
		if _, err := r.add(call); err != nil {
			return toolRound{}, err
		}
	}
	// However the round ends, none of its calls stays parked.
	defer func() {
		for _, c := range r.calls {
			e.gate.unpark(c.call.ID)
		}
	}()

	// ok turns false once an event can no longer be delivered or the turn is
	// cancelled; the round then ends without responses.
	ok := true
	for _, c := range r.calls {
		if ok = r.dispatch(c); !ok {
			break
		}
	}
	for ok && (ex != nil || slices.ContainsFunc(r.calls, unresolved)) {
		select {
		case event := <-events:
			var err error
			if ok, err = r.apply(event); err != nil {
				r.ended = true
				return toolRound{}, err
			}
		case result := <-done:
			done = nil
			r.ended = true
			r.adopt(result.conversation)
			if result.err == nil {
				r.acknowledge()
			}
			r.stopRound(nil, nil)
			return toolRound{outcome: roundAnswered}, result.err
		case command := <-e.commands:
			if command.generation == generation {
				ok = r.handleCommand(command)
			}

		case <-e.permissionsChanged:
			ok = r.regateParked()

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
				r.ended = true
				r.stopRound(c, done.err)
				return toolRound{}, done.err
			}

		case <-ctx.Done():
			ok = false
		}
	}
	if !ok {
		return toolRound{outcome: roundAborted}, nil
	}
	round := toolRound{
		outcome:   roundAnswered,
		responses: make([]assistant.ClientToolResponse, len(r.calls)),
	}
	if r.stopRequested {
		round.outcome = roundStopped
	}
	for i, c := range r.calls {
		round.responses[i] = c.response
	}
	return round, nil
}

// startExchange runs the exchange until it ends or the round is cancelled.
func (r *toolRoundState) startExchange() (<-chan exchangeEvent, <-chan exchangeResult) {
	events := make(chan exchangeEvent)
	done := make(chan exchangeResult, 1)
	go func() {
		conversation, err := r.ex.run(r.ctx, func(event exchangeEvent) bool {
			select {
			case events <- event:
				return true
			case <-r.ctx.Done():
				return false
			}
		})
		done <- exchangeResult{conversation: conversation, err: err}
	}()
	return events, done
}

// joinExchange cancels an exchange that has not ended, and waits a bounded
// time for its transport cleanup, such as the server cancellation request.
// Tool handlers are never joined.
func (r *toolRoundState) joinExchange(done <-chan exchangeResult) {
	if done == nil {
		return
	}
	r.cancelRound()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case result := <-done:
		r.adopt(result.conversation)
	case <-timer.C:
	}
}

// apply folds one exchange event and dispatches the call it executes. Once a
// stop is requested, content is no longer shown.
func (r *toolRoundState) apply(event exchangeEvent) (bool, error) {
	if event.conversation != "" {
		r.adopt(event.conversation)
		if !r.send(Event{Kind: EventConversation, ConvID: event.conversation}) {
			return false, nil
		}
	}
	if event.message != nil {
		r.acknowledge()
		if !r.stopRequested && !r.e.foldMessage(r.ctx, r.tools, *event.message, r.send) {
			return false, nil
		}
	}
	if event.execute == nil {
		return true, nil
	}
	c, err := r.add(*event.execute)
	if err != nil {
		return false, err
	}
	return r.dispatch(c), nil
}

// add registers an execution-ready call; its ID must be unique in the round.
func (r *toolRoundState) add(call ToolCall) (*roundCall, error) {
	if call.ID == "" {
		return nil, errors.New("client tool call has no id")
	}
	if _, exists := r.byID[call.ID]; exists {
		return nil, fmt.Errorf("duplicate client tool call id %q", call.ID)
	}
	c := &roundCall{call: call}
	r.calls = append(r.calls, c)
	r.byID[call.ID] = c
	return c, nil
}

// adopt records a conversation ID the backend assigned or confirmed.
func (r *toolRoundState) adopt(conversation string) {
	if conversation != "" {
		r.e.opts.ConversationID = conversation
	}
}

// acknowledge records the user request as delivered once the backend responds.
func (r *toolRoundState) acknowledge() {
	if r.user != nil {
		r.e.delivery.delivered(*r.user)
		r.user = nil
	}
}

// dispatch decides how a call starts: answered locally, parked for approval,
// or launched. Once a stop is requested nothing new starts. The approval
// policy runs here, once and outside the gate lock; the mode only decides
// what the declared gate means.
func (r *toolRoundState) dispatch(c *roundCall) bool {
	if r.stopRequested {
		return r.resolve(c, cancelledResult())
	}
	if c.call.Name == assistant.ApprovalRequestTool {
		// The server write gate never runs a handler; it is answered locally.
		if _, err := parseServerGateInput(c.call); err != nil {
			return r.deny(c, invalidServerGateResult())
		}
		if r.tools == nil {
			return r.deny(c, serverDeniedResult()) // no tool set, so no one to approve it
		}
	}
	requirement, gated := r.tools.policy(c.call)
	if !gated {
		r.start(c)
		return true
	}
	c.gate = requirement
	switch r.admit(c, false) {
	case verdictRun:
		return r.publishStarted(c, false)
	case verdictDeny:
		return r.deny(c, modeDeniedResult())
	default:
		return r.awaitApproval(c)
	}
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
	switch r.admit(c, true) {
	case verdictRun:
		return r.publishStarted(c, true)
	case verdictDeny:
		return r.deny(c, modeDeniedResult())
	default:
		return true // an answer never parks
	}
}

// regateParked re-gates parked calls after a mode switch or a grant: a call
// the permissions now allow runs, one the mode denies is denied, and the rest
// stay parked.
func (r *toolRoundState) regateParked() bool {
	for _, c := range r.calls {
		if c.status != callAwaitingApproval {
			continue
		}
		switch r.admit(c, false) {
		case verdictRun:
			if !r.publishStarted(c, true) {
				return false
			}
		case verdictDeny:
			if !r.deny(c, modeDeniedResult()) {
				return false
			}
		case verdictPark:
		}
	}
	return true
}

// admit decides how gated call c proceeds and, when it may run, starts it,
// atomically with respect to permission changes. Callers publish the outcome.
func (r *toolRoundState) admit(c *roundCall, allowOnce bool) verdict {
	return r.e.gate.admit(r.tools, c.call.ID, c.gate.Key, allowOnce, func() { r.start(c) })
}

func (r *toolRoundState) awaitApproval(c *roundCall) bool {
	c.status = callAwaitingApproval
	if _, updated := r.e.transcript.MarkAwaitingApproval(c.call.ID, c.gate.Prompt); !updated {
		return true
	}
	return r.send(Event{Kind: EventTranscript, Transcript: r.e.snapshot()})
}

// start begins c without publishing anything, so it may run under the gate
// lock: a handler is launched, or the server gate is answered as approved.
func (r *toolRoundState) start(c *roundCall) {
	if c.call.Name == assistant.ApprovalRequestTool {
		r.settle(c, approvedResult())
		return
	}
	toolCtx, cancel := context.WithCancel(r.ctx)
	c.status = callRunning
	c.cancel = cancel
	call := c.call
	go func() {
		if r.slots != nil {
			select {
			case r.slots <- struct{}{}:
				defer func() { <-r.slots }()
			case <-toolCtx.Done():
				return
			}
			if r.ctx.Err() != nil || toolCtx.Err() != nil {
				return
			}
		}
		result, err := r.tools.Run(toolCtx, call)
		if r.ex == nil {
			r.results <- toolDone{call: c, result: result, err: err}
			return
		}
		select {
		case r.results <- toolDone{call: c, result: result, err: err}:
		case <-r.ctx.Done():
		}
	}()
}

// publishStarted shows a call start began: the server gate's approval, or a
// handler running once it had waited for approval.
func (r *toolRoundState) publishStarted(c *roundCall, wasParked bool) bool {
	if c.call.Name == assistant.ApprovalRequestTool {
		return r.publishResult(c, approvedResult())
	}
	if !wasParked {
		return true
	}
	if _, updated := r.e.transcript.MarkToolRunning(c.call.ID); !updated {
		return true
	}
	return r.send(Event{Kind: EventTranscript, Transcript: r.e.snapshot()})
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
	r.e.gate.unpark(c.call.ID)
	r.settle(c, result)
	return r.publishResult(c, result)
}

// settle records c's wire response without publishing it.
func (r *toolRoundState) settle(c *roundCall, result ToolResult) {
	c.status = callResolved
	c.response = toolResponse(c.call, result)
}

func (r *toolRoundState) publishResult(c *roundCall, result ToolResult) bool {
	if r.ex != nil && !r.ended && r.ctx.Err() == nil {
		r.ex.answer(c.response)
	}
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

// foldMessage applies remote content and tool-input reducers before publishing
// the transcript and usage through the turn's event emitter.
func (e *Engine) foldMessage(ctx context.Context, tools *ToolSet, msg assistant.Message, emit func(Event) bool) bool {
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
