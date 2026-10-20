package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// scriptBackend is the test backend: Send i streams rounds[i], if any, and
// every request is recorded. It returns the request's conversation ID, or
// convID for a new conversation.
type scriptBackend struct {
	rounds [][]assistant.Message
	convID string
	// After streaming the final round, Send closes started and, when hold is
	// set, waits for hold to close or the request to be cancelled.
	started chan struct{}
	hold    chan struct{}
	// err is returned by Send failOn (default the first), even if cancelled.
	err    error
	failOn int

	startOnce sync.Once
	opts      []assistant.SendOptions
	sent      []any
}

func (b *scriptBackend) Send(ctx context.Context, message any, opts assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.opts = append(b.opts, opts)
	b.sent = append(b.sent, message)
	n := len(b.sent)
	id := opts.ConversationID
	if id == "" {
		id = b.convID
	}
	if n <= len(b.rounds) {
		for _, m := range b.rounds[n-1] {
			var response assistant.AssistantResponse
			response.Data.Attributes.StructuredMessage = m
			if err := emit(response); err != nil {
				return id, err
			}
		}
	}
	held := false
	if n >= len(b.rounds) {
		if b.started != nil {
			b.startOnce.Do(func() { close(b.started) })
		}
		if b.hold != nil {
			held = true
			select {
			case <-b.hold:
			case <-ctx.Done():
			}
		}
	}
	if b.err != nil && n == max(b.failOn, 1) {
		return id, b.err
	}
	if held {
		return id, ctx.Err()
	}
	return id, nil
}

// last returns the options of the latest request.
func (b *scriptBackend) last() assistant.SendOptions {
	if len(b.opts) == 0 {
		return assistant.SendOptions{}
	}
	return b.opts[len(b.opts)-1]
}

// userContexts returns the custom user context of every request.
func (b *scriptBackend) userContexts() []string {
	contexts := make([]string, len(b.opts))
	for i, opts := range b.opts {
		contexts[i] = opts.CustomUserContext
	}
	return contexts
}

// responses returns the client tool response batches sent as follow-ups.
func (b *scriptBackend) responses() [][]assistant.ClientToolResponse {
	var batches [][]assistant.ClientToolResponse
	for _, message := range b.sent {
		if batch, ok := message.([]assistant.ClientToolResponse); ok {
			batches = append(batches, batch)
		}
	}
	return batches
}

// userMessages returns the user messages sent to one conversation.
func (b *scriptBackend) userMessages(conversationID string) []string {
	var messages []string
	for i, message := range b.sent {
		id := b.opts[i].ConversationID
		if id == "" {
			id = b.convID
		}
		if text, ok := message.(string); ok && id == conversationID {
			messages = append(messages, text)
		}
	}
	return messages
}

func textMsg(id, text string) assistant.Message {
	return assistant.AssistantMessage(id, assistant.TextContent(text))
}

func serverCallMsg(callID, name, input string) assistant.Message {
	return assistant.AssistantMessage(callID, assistant.ToolCallContent(callID, name, input))
}

func clientCallMsg(callID, name, input string) assistant.Message {
	content := assistant.ToolCallContent(callID, name, input)
	content.Type = assistant.ContentClientToolCall
	return assistant.AssistantMessage(callID, content)
}

// toolRounds asks for a client call "call-<name>" to each tool in one round,
// then answers.
func toolRounds(names ...string) [][]assistant.Message {
	calls := make([]assistant.Message, len(names))
	for i, name := range names {
		calls[i] = clientCallMsg("call-"+name, name, `{}`)
	}
	return [][]assistant.Message{calls, {textMsg("answer", "done")}}
}

func TestContextIsSentOnlyWithTheUserMessage(t *testing.T) {
	backend := &scriptBackend{rounds: toolRounds("read")}
	tools, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "read"},
		Handler: func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Output: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, Config{})
	turnContext := &assistant.AssistantContext{Entities: []assistant.ContextEntity{{
		Type: "dashboard", ID: "dash-1", Label: "Checkout overview",
	}}}
	drain(t, engine, TurnInput{Message: "inspect", Tools: tools, Context: turnContext})
	drain(t, engine, TurnInput{Message: "next turn", Tools: tools})

	if len(backend.opts) != 3 {
		t.Fatalf("requests = %d, want 3", len(backend.opts))
	}
	if !reflect.DeepEqual(backend.opts[0].Context, turnContext) {
		t.Fatalf("user message context = %#v, want %#v", backend.opts[0].Context, turnContext)
	}
	if backend.opts[1].Context != nil || backend.opts[2].Context != nil {
		t.Fatalf("tool answers or the next turn carried context: %#v, %#v", backend.opts[1].Context, backend.opts[2].Context)
	}
}

func TestUserContextIsSentOnlyWhenTheConversationLacksIt(t *testing.T) {
	backend := &scriptBackend{rounds: toolRounds("read"), convID: "conversation-1"}
	tools, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "read"},
		Handler: func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Output: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := "env-a"
	userContext := func(context.Context) string { return env }
	engine := New(backend, Config{UserContext: userContext})
	turn := func() {
		drain(t, engine, TurnInput{Message: "hi", Tools: tools})
	}

	turn() // tool call, then continuation
	turn()
	env = "env-b"
	turn()
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	turn()

	if err := engine.InstallConversation(context.Background(), &Conversation{id: "restored", transcript: NewTranscript()}); err != nil {
		t.Fatal(err)
	}
	turn()

	want := []string{"env-a", "", "", "env-b", "env-b", "env-b"}
	if got := backend.userContexts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("user contexts = %q, want %q", got, want)
	}
}

// drain runs one turn to its end and returns its events, passing each to
// observe first. The engine is idle once drain returns.
func drain(t *testing.T, e *Engine, in TurnInput, observe ...func(Event)) []Event {
	t.Helper()
	var evs []Event
	for ev := range e.StartTurn(t.Context(), in) {
		for _, fn := range observe {
			fn(ev)
		}
		evs = append(evs, ev)
	}
	return evs
}

// collect gathers the events of an operation already started until its
// channel closes.
func collect(ch <-chan Event) []Event {
	var evs []Event
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

func TestEngineInfersMissingClientIdentityForRegisteredStreamedTool(t *testing.T) {
	for _, test := range []struct {
		name  string
		known bool
		want  bool
	}{
		{name: "omitted marker", want: true},
		{name: "explicit server marker", known: true, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &scriptBackend{rounds: [][]assistant.Message{{assistant.AssistantMessage("started", assistant.Content{
				Type: assistant.ContentToolCallStarted,
				Tool: &assistant.ToolPayload{ToolCallID: "read-1", ToolName: "read_file", HasClientSide: test.known},
			})}}}
			tools, err := NewToolSet(ModeSkipPermissions, Tool{
				Definition: assistant.ClientTool{Name: "read_file"},
				Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			events := drain(t, New(backend, Config{}), TurnInput{Message: "read", Tools: tools})
			var got *ToolBlock
			for _, event := range events {
				if event.Kind != EventTranscript {
					continue
				}
				for _, block := range event.Transcript.Blocks {
					if block.Tool != nil && block.ToolCallID() == "read-1" {
						value := *block.Tool
						got = &value
					}
				}
			}
			if got == nil || got.IsClientSide != test.want {
				t.Fatalf("streamed tool = %+v, want IsClientSide=%v", got, test.want)
			}
		})
	}
}

func stateBlock(event Event, id string) (Block, bool) {
	if event.Kind != EventTranscript {
		return Block{}, false
	}
	for _, block := range event.Transcript.Blocks {
		if block.ToolCallID() == id {
			return block, true
		}
	}
	return Block{}, false
}

func stateHasAssistant(event Event) bool {
	if event.Kind != EventTranscript {
		return false
	}
	for _, block := range event.Transcript.Blocks {
		if block.Role == assistant.RoleAssistant {
			return true
		}
	}
	return false
}

func TestSnapshotDerivesTranscriptViewState(t *testing.T) {
	e := New(&scriptBackend{}, Config{})
	e.transcript.AppendMessage(assistant.AssistantMessage("tool", assistant.ToolCallContent("call-1", "search", "{}")))
	if _, ok := e.transcript.MarkAwaitingApproval("call-1", ApprovalPrompt{}); !ok {
		t.Fatal("MarkAwaitingApproval did not update tool")
	}
	e.transcript.AppendMessage(assistant.AssistantMessage("text", assistant.TextContent("partial")))

	snapshot := e.snapshot()
	if !snapshot.HasStreamingContent() {
		t.Fatal("HasStreamingContent() = false, want true for open assistant text")
	}
	pending := snapshot.PendingApprovals()
	if len(pending) != 1 || pending[0].ToolCallID() != "call-1" {
		t.Fatalf("PendingApprovals() = %+v, want call-1", pending)
	}
}

func TestSnapshotUserPrompts(t *testing.T) {
	user := func(text string) Block {
		return Block{Role: assistant.RoleUser, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: text}}
	}
	answer := Block{Role: assistant.RoleAssistant, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: "answer"}}
	withContext := user(" ")
	withContext.Context = []assistant.ContextEntity{{Type: assistant.EntityImageURL, ID: "data:image/png;base64,AAAA"}}
	for _, test := range []struct {
		name   string
		blocks []Block
		want   []UserPrompt
	}{
		{name: "empty transcript"},
		{
			name:   "user text in order, repeats kept",
			blocks: []Block{user("a"), answer, user("b\nc"), user("b\nc")},
			want:   []UserPrompt{{Text: "a"}, {Text: "b\nc"}, {Text: "b\nc"}},
		},
		{
			name:   "blank and body-less user blocks skipped",
			blocks: []Block{user(" "), {Role: assistant.RoleUser, Kind: assistant.KindText}, user("kept")},
			want:   []UserPrompt{{Text: "kept"}},
		},
		{
			name:   "an attachment-only prompt is kept with empty text",
			blocks: []Block{withContext},
			want:   []UserPrompt{{Context: withContext.Context}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := (TranscriptSnapshot{Blocks: test.blocks}).UserPrompts(); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("UserPrompts() = %q, want %q", got, test.want)
			}
		})
	}
}

func kinds(evs []Event) []EventKind {
	ks := make([]EventKind, len(evs))
	for i, e := range evs {
		ks[i] = e.Kind
	}
	return ks
}

type streamedInputState struct {
	phase string
	delta string
	final string
}

func TestEngineReducesClientToolInputBeforeEventAndHandler(t *testing.T) {
	backend := &scriptBackend{rounds: [][]assistant.Message{{
		assistant.AssistantMessage("tool-call", assistant.Content{Type: assistant.ContentToolCallStarted, Tool: &assistant.ToolPayload{
			ToolCallID: "call-1", ToolName: "preview", IsClientSide: true,
		}}),
		assistant.AssistantMessage("tool-call", assistant.Content{Type: assistant.ContentToolCallInputDelta, Tool: &assistant.ToolPayload{ToolCallID: "call-1", PartialJSON: `{"value":`}}),
		assistant.AssistantMessage("tool-call", assistant.Content{Type: assistant.ContentToolCallInputDelta, Tool: &assistant.ToolPayload{ToolCallID: "call-1", PartialJSON: `"delta"}`}}),
		clientCallMsg("call-1", "preview", `{"value":"final"}`),
	}, {textMsg("answer", "done")}}}
	reducerCalls := 0
	handlerSawReducer := false
	tools, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "preview"},
		InputReducer: func(_ context.Context, update ToolInputUpdate, prior any) any {
			reducerCalls++
			state := &streamedInputState{phase: update.Name, delta: update.Delta, final: update.FinalInput}
			if previous, ok := prior.(*streamedInputState); ok {
				state.phase = previous.phase + "+" + state.phase
			}
			return state
		},
		Handler: func(_ context.Context, _ ToolCall) (ToolResult, error) {
			handlerSawReducer = reducerCalls == 4
			return ToolResult{Output: "ok"}, nil
		},
	}, Tool{
		Definition: assistant.ClientTool{Name: "ordinary"},
		Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, New(backend, Config{}), TurnInput{Message: "go", Tools: tools})
	if len(backend.opts) != 2 {
		t.Fatalf("backend requests = %d, want initial and continuation", len(backend.opts))
	}
	for _, opts := range backend.opts {
		if len(opts.ClientTools) != 2 || !opts.ClientTools[0].StreamInput || opts.ClientTools[1].StreamInput {
			t.Fatalf("per-tool streaming flags = %+v", opts.ClientTools)
		}
	}
	if reducerCalls != 4 {
		t.Fatalf("reducer calls = %d, want 4", reducerCalls)
	}
	if !handlerSawReducer {
		t.Fatal("handler ran before all streamed input reductions")
	}
	var final Block
	for _, event := range events {
		block, ok := stateBlock(event, "call-1")
		if !ok {
			continue
		}
		if block.Tool != nil && block.Tool.RenderState != nil {
			final = block
		}
	}
	state, ok := final.Tool.RenderState.(*streamedInputState)
	if !ok || state.final != `{"value":"final"}` {
		t.Fatalf("final reducer state = %#v, want final input", final.Tool.RenderState)
	}
}

func TestEngineDoesNotReduceServerToolCall(t *testing.T) {
	backend := &scriptBackend{rounds: [][]assistant.Message{{serverCallMsg("call-1", "preview", `{"value":"server"}`)}}}
	reducerCalls := 0
	tools, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "preview"},
		InputReducer: func(context.Context, ToolInputUpdate, any) any {
			reducerCalls++
			return struct{}{}
		},
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, New(backend, Config{}), TurnInput{Message: "go", Tools: tools})
	if reducerCalls != 0 {
		t.Fatalf("server-side tool triggered %d reductions", reducerCalls)
	}
	if len(backend.opts) == 0 || !backend.opts[0].ClientTools[0].StreamInput {
		t.Fatalf("per-tool streaming not requested for reducer-backed tool: %+v", backend.opts)
	}
}

func TestEnginePreservesPerToolInputStreaming(t *testing.T) {
	backend := &scriptBackend{}
	tools, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "ordinary", StreamInput: true},
		Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	e := New(backend, Config{})
	_ = drain(t, e, TurnInput{Message: "go", Tools: tools})
	if len(backend.opts) == 0 || !backend.opts[0].ClientTools[0].StreamInput {
		t.Fatalf("caller-provided per-tool streaming was disabled: %+v", backend.opts)
	}
}

func TestTurnCompletionSynchronizesOperationRelease(t *testing.T) {
	e := New(&scriptBackend{convID: "conversation-1"}, Config{})
	operation := e.beginTurn(context.Background(), TurnInput{Message: "question"})
	completion := <-operation.completion
	if !completion.Completed {
		t.Fatalf("completion = %+v", completion)
	}
	if e.OperationActive() {
		t.Fatal("completion was published before operation release")
	}
	if err := e.NewConversation(); err != nil {
		t.Fatalf("operation after completion: %v", err)
	}
	_ = collect(operation.events)
}

func TestConcurrentTurnReturnsOperationError(t *testing.T) {
	gate := make(chan struct{})
	e := New(&scriptBackend{hold: gate}, Config{})
	ch := e.StartTurn(context.Background(), TurnInput{Message: "one"})
	events := drain(t, e, TurnInput{Message: "two"})
	if len(events) != 1 || events[0].Kind != EventError || !errors.Is(events[0].Err, ErrOperationActive) {
		t.Fatalf("overlap events = %+v, want ErrOperationActive", events)
	}
	close(gate)
	_ = collect(ch)
}

func TestTurnEmitsEventSequence(t *testing.T) {
	usage := assistant.AssistantMessage("m1", assistant.TextContent(""))
	usage.Results = &assistant.Results{Usage: &assistant.Usage{TokensUsed: 5}}
	b := &scriptBackend{
		convID: "conv-1",
		rounds: [][]assistant.Message{{textMsg("m1", "Hello"), textMsg("m1", " world"), usage}},
	}
	e := New(b, Config{})

	evs := drain(t, e, TurnInput{Message: "hi"})
	want := []EventKind{EventTranscript, EventTranscript, EventTranscript, EventUsage, EventConversation, EventTranscript, EventTurnDone}
	if got := kinds(evs); !reflect.DeepEqual(got, want) {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
	if evs[0].Origin != TranscriptOriginLocal || evs[1].Origin != TranscriptOriginRemote {
		t.Fatalf("transcript origins = %v, %v; want local, remote", evs[0].Origin, evs[1].Origin)
	}
	blocks := evs[2].Transcript.Blocks
	if len(blocks) == 0 || blocks[len(blocks)-1].Markdown == nil || blocks[len(blocks)-1].Markdown.Content != "Hello world" {
		t.Fatalf("final text blocks = %+v", blocks)
	}
	if blocks[len(blocks)-1].Complete {
		t.Fatal("streaming transcript snapshot unexpectedly marked the answer complete")
	}
	if evs[4].ConvID != "conv-1" {
		t.Fatalf("conv id = %q, want conv-1", evs[4].ConvID)
	}
	final := evs[5].Transcript.Blocks
	if len(final) == 0 || !final[len(final)-1].Complete {
		t.Fatalf("final transcript snapshot = %+v, want completed answer", final)
	}
}

func TestTurnRetainsConversationIDDiscoveredBeforeBackendError(t *testing.T) {
	backendErr := errors.New("stream failed")
	e := New(&scriptBackend{
		convID: "created-before-error",
		err:    backendErr,
		rounds: [][]assistant.Message{{textMsg("m1", "partial")}},
	}, Config{})

	events := drain(t, e, TurnInput{Message: "question"})
	wantKinds := []EventKind{EventTranscript, EventTranscript, EventTranscript, EventConversation, EventError}
	if got := kinds(events); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("event kinds = %v, want %v", got, wantKinds)
	}
	if events[3].ConvID != "created-before-error" || !errors.Is(events[4].Err, backendErr) || !events[4].BackendFailure {
		t.Fatalf("terminal events = %+v", events[2:])
	}
	final := events[2].Transcript.Blocks
	if len(final) == 0 || !final[len(final)-1].Complete {
		t.Fatalf("final transcript snapshot = %+v, want completed answer", final)
	}
	if got := e.ConversationID(); got != "created-before-error" {
		t.Fatalf("conversation ID = %q", got)
	}
	blocks := e.Snapshot()
	if len(blocks) != 2 || !blocks[1].Complete {
		t.Fatalf("final blocks = %+v", blocks)
	}
}

func TestNewConversationIsLazyAndRetainsClientConfiguration(t *testing.T) {
	backend := &scriptBackend{convID: "new-conversation-1"}
	opts := Config{
		ConversationID: "old-conversation",
		Model:          "retained-model",
		DebugTag:       "retained-debug-tag",
		MessageHistory: []json.RawMessage{json.RawMessage(`{"role":"user","content":"old history"}`)},
	}
	e := New(backend, opts)

	_ = drain(t, e, TurnInput{Message: "old prompt"})
	callsBeforeReset := len(backend.opts)
	if err := e.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if len(backend.opts) != callsBeforeReset {
		t.Fatal("NewConversation made a backend request")
	}
	if got := e.ConversationID(); got != "" {
		t.Fatalf("conversation id after reset = %q, want empty", got)
	}
	if got := e.PreviousConversationID(); got != "old-conversation" {
		t.Fatalf("previous conversation id = %q, want old-conversation", got)
	}
	if len(e.transcript.Blocks()) != 0 {
		t.Fatalf("transcript after reset has %d blocks", len(e.transcript.Blocks()))
	}

	_ = drain(t, e, TurnInput{Message: "new prompt"})
	if got := e.ConversationID(); got != "new-conversation-1" {
		t.Fatalf("new conversation id = %q", got)
	}
	if got := backend.opts[len(backend.opts)-1]; got.ConversationID != "" ||
		len(got.MessageHistory) != 0 || got.Model != opts.Model ||
		got.DebugTag != opts.DebugTag {
		t.Fatalf("first new request options = %+v; process-wide configuration was not retained", got)
	}
	if got := backend.userMessages("old-conversation"); !reflect.DeepEqual(got, []string{"old prompt"}) {
		t.Fatalf("prior conversation was mutated: %v", got)
	}
	if got := backend.userMessages("new-conversation-1"); !reflect.DeepEqual(got, []string{"new prompt"}) {
		t.Fatalf("new conversation messages = %v", got)
	}
}

func TestNewConversationRetainsIDDiscoveredDuringCancellation(t *testing.T) {
	backend := &scriptBackend{convID: "created-before-cancel", started: make(chan struct{}), hold: make(chan struct{})}
	e := New(backend, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	turn := e.StartTurn(ctx, TurnInput{Message: "cancel me"})
	<-backend.started
	cancel()
	_ = collect(turn)

	if got := e.ConversationID(); got != "created-before-cancel" {
		t.Fatalf("conversation id after cancellation = %q", got)
	}
	if err := e.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if got := e.PreviousConversationID(); got != "created-before-cancel" {
		t.Fatalf("previous conversation id after reset = %q", got)
	}
	if e.ConversationID() != "" {
		t.Fatalf("current conversation id after reset = %q", e.ConversationID())
	}
}

// Cancellation settles the turn's unfinished tools, leaves earlier turns'
// tools untouched, and delivers that settled snapshot even when the consumer
// has stopped reading and the event buffer is full.
func TestTurnCancellationSettlesUnfinishedTools(t *testing.T) {
	second := []assistant.Message{
		assistant.AssistantMessage("preview", assistant.Content{
			Type: assistant.ContentToolCallStarted,
			Tool: &assistant.ToolPayload{ToolCallID: "preview", ToolName: "write_file", IsClientSide: true},
		}),
		assistant.AssistantMessage("preview", assistant.Content{
			Type: assistant.ContentToolCallInputDelta,
			Tool: &assistant.ToolPayload{ToolCallID: "preview", PartialJSON: `{"path":"half.txt"}`},
		}),
		serverCallMsg("server", "search_logs", `{}`),
	}
	for i := range 128 { // more than the engine buffers
		second = append(second, textMsg(fmt.Sprint("filler-", i), "filler"))
	}
	backend := &scriptBackend{rounds: [][]assistant.Message{{serverCallMsg("prior", "old_tool", `{}`)}, second}, hold: make(chan struct{})}
	engine := New(backend, Config{})
	drain(t, engine, TurnInput{Message: "first"})

	ctx, cancel := context.WithCancel(context.Background())
	events := engine.StartTurn(ctx, TurnInput{Message: "second"})
	for len(events) < cap(events) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	evs := collect(events)

	status := map[string]ToolStatus{}
	for _, block := range evs[len(evs)-1].Transcript.Blocks {
		if block.Tool != nil {
			status[block.ToolCallID()] = block.Tool.Status
		}
	}
	if status["prior"] == ToolCancelled || status["preview"] != ToolCancelled || status["server"] != ToolCancelled {
		t.Fatalf("tool statuses = %v, want only this turn's tools cancelled", status)
	}
}

func TestNewConversationRejectsActiveTurnWithoutMutation(t *testing.T) {
	gate := make(chan struct{})
	e := New(&scriptBackend{hold: gate}, Config{ConversationID: "old"})
	turn := e.StartTurn(context.Background(), TurnInput{Message: "busy"})
	if err := e.NewConversation(); !errors.Is(err, ErrOperationActive) {
		t.Fatalf("NewConversation error = %v, want ErrOperationActive", err)
	}
	if got := e.ConversationID(); got != "old" {
		t.Fatalf("active reset changed conversation id to %q", got)
	}
	close(gate)
	_ = collect(turn)
}

func TestRestoreEmitsSingleSnapshot(t *testing.T) {
	resp := &assistant.ConversationHistoryResponse{}
	resp.Data.Type = "conversation-history-response"
	resp.Data.Attributes.Messages = []assistant.Message{
		assistant.AssistantMessage("m1", assistant.TextContent("Hello")),
		assistant.AssistantMessage("turn", assistant.Content{Type: assistant.ContentTurnStatus, TurnStatus: &assistant.TurnStatusPayload{Status: "ended"}}),
		assistant.AssistantMessage("stop", assistant.Content{Type: assistant.ContentUserStop, Stop: &assistant.StopPayload{Content: "stopped"}}),
		assistant.AssistantMessage("internal", assistant.Content{Type: assistant.ContentProviderCompaction, Compaction: &assistant.CompactionPayload{Summary: "private"}}),
		assistant.AssistantMessage("m2", assistant.TextContent("World")),
		assistant.AssistantMessage("future", assistant.Content{Type: "future_content"}),
	}
	e := New(&historyBackend{resp: resp}, Config{ConversationID: testConversationID})

	evs := collect(e.Restore(context.Background()))
	if len(evs) != 1 || evs[0].Kind != EventTranscript {
		t.Fatalf("events = %v, want one EventTranscript", kinds(evs))
	}
	if evs[0].Origin != TranscriptOriginRestore {
		t.Fatalf("restore origin = %v, want restore", evs[0].Origin)
	}
	blocks := evs[0].Transcript.Blocks
	if len(blocks) != 3 || blocks[0].Kind != assistant.KindText || blocks[1].Kind != assistant.KindText || blocks[2].Kind != assistant.KindUnknown {
		t.Fatalf("restored blocks = %+v, want text/text/unknown without technical markers", blocks)
	}
}

func TestRestoreUnsupportedBackendErrors(t *testing.T) {
	e := New(&scriptBackend{}, Config{ConversationID: testConversationID})

	evs := collect(e.Restore(context.Background()))
	if len(evs) != 1 || evs[0].Kind != EventError || !errors.Is(evs[0].Err, ErrHistoryUnsupported) {
		t.Fatalf("events = %+v, want ErrHistoryUnsupported", evs)
	}
}

// An empty history folds to no blocks, so the len>0 guard emits nothing.
func TestRestoreEmptyEmitsNothing(t *testing.T) {
	resp := &assistant.ConversationHistoryResponse{}
	resp.Data.Type = "conversation-history-response"
	e := New(&historyBackend{resp: resp}, Config{ConversationID: testConversationID})

	if evs := collect(e.Restore(context.Background())); len(evs) != 0 {
		t.Fatalf("events = %v, want none", kinds(evs))
	}
}

type statusBackend struct {
	scriptBackend
	calls int
}

func (b *statusBackend) BackendStatus() assistant.BackendStatus {
	b.calls++
	return assistant.BackendStatus{
		Site:                "https://api.us5.datadoghq.com",
		AuthenticationMode:  "oauth",
		AuthenticationState: "authenticated",
		HasCredentials:      true,
	}
}

func TestEngineStatusReportsEffectiveNonSecretRuntimeConfiguration(t *testing.T) {
	backend := &statusBackend{}
	engine := New(backend, Config{
		ConversationID: "conversation-1",
		Model:          "model-x",
	})
	got := engine.Status()
	if got.Profile != assistant.DefaultProfile || got.Model != "model-x" {
		t.Fatalf("engine status = %+v", got)
	}
	if got.Backend != (assistant.BackendStatus{
		Site:                "https://api.us5.datadoghq.com",
		AuthenticationMode:  "oauth",
		AuthenticationState: "authenticated",
		HasCredentials:      true,
	}) {
		t.Fatalf("backend status = %+v", got.Backend)
	}
	_ = engine.Status()
	if backend.calls != 1 {
		t.Fatalf("backend status collections = %d, want one immutable snapshot", backend.calls)
	}
}

type currentUserBackend struct {
	scriptBackend
	user  assistant.CurrentUser
	err   error
	calls int
}

func (b *currentUserBackend) CurrentUser(context.Context) (assistant.CurrentUser, error) {
	b.calls++
	return b.user, b.err
}

func TestEngineCurrentUserUsesOptionalBackendCapability(t *testing.T) {
	want := assistant.CurrentUser{Name: "Bits User", Organization: "Bits Staging"}
	backend := &currentUserBackend{user: want}
	engine := New(backend, Config{})

	got, err := engine.CurrentUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want || backend.calls != 1 {
		t.Fatalf("current user = %+v calls=%d, want %+v calls=1", got, backend.calls, want)
	}
}

func TestEngineCurrentUserRejectsUnsupportedBackend(t *testing.T) {
	engine := New(&scriptBackend{}, Config{})

	_, err := engine.CurrentUser(context.Background())
	if !errors.Is(err, ErrCurrentUserUnsupported) {
		t.Fatalf("current user error = %v, want ErrCurrentUserUnsupported", err)
	}
}
