package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// scriptBackend replays a fixed set of messages, then returns convID/err.
type scriptBackend struct {
	msgs   []assistant.Message
	convID string
	err    error
}

func (b *scriptBackend) Send(_ context.Context, _ any, _ assistant.SendOptions, fn func(assistant.AssistantResponse) error) (string, error) {
	for _, m := range b.msgs {
		var ar assistant.AssistantResponse
		ar.Data.Attributes.StructuredMessage = m
		if err := fn(ar); err != nil {
			return "", err
		}
	}
	return b.convID, b.err
}

// blockingBackend holds a turn open until gate is closed.
type blockingBackend struct{ gate chan struct{} }

func (b *blockingBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	select {
	case <-b.gate:
	case <-ctx.Done():
	}
	return "conv", nil
}

type conversationRecordingBackend struct {
	opts     []assistant.SendOptions
	messages map[string][]string
}

type cancellationIDBackend struct{ started chan struct{} }

type contextRecordingBackend struct {
	calls        int
	contexts     []*assistant.AssistantContext
	userContexts []string
}

func (b *contextRecordingBackend) Send(_ context.Context, _ any, opts assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.calls++
	b.contexts = append(b.contexts, opts.Context)
	b.userContexts = append(b.userContexts, opts.CustomUserContext)
	var response assistant.AssistantResponse
	if b.calls == 1 {
		content := assistant.ToolCallContent("tool-call-1", "read", `{}`)
		content.Type = assistant.ContentClientToolCall
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("tool-message", content)
		return "conversation-1", emit(response)
	}
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer-message", assistant.TextContent("done"))
	return "conversation-1", emit(response)
}

func (b *cancellationIDBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	close(b.started)
	<-ctx.Done()
	return "created-before-cancel", ctx.Err()
}

func (b *conversationRecordingBackend) Send(_ context.Context, message any, opts assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	b.opts = append(b.opts, opts)
	id := opts.ConversationID
	if id == "" {
		id = "new-conversation-1"
	}
	b.messages[id] = append(b.messages[id], message.(string))
	return id, nil
}

func TestTurnContextSurvivesToolContinuationsAndDoesNotLeak(t *testing.T) {
	backend := &contextRecordingBackend{}
	tools, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "read"},
		Handler: func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Output: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, assistant.SendOptions{})
	turnContext := &assistant.AssistantContext{Entities: []assistant.ContextEntity{{
		Type: "dashboard", ID: "dash-1", Label: "Checkout overview",
	}}}
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "inspect", Tools: tools, Context: turnContext}))
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "next turn", Tools: tools}))

	if len(backend.contexts) != 3 {
		t.Fatalf("request contexts = %#v", backend.contexts)
	}
	if !reflect.DeepEqual(backend.contexts[0], turnContext) || !reflect.DeepEqual(backend.contexts[1], turnContext) {
		t.Fatalf("initial/continuation contexts = %#v, want %#v", backend.contexts[:2], turnContext)
	}
	if backend.contexts[2] != nil {
		t.Fatalf("next independent turn reused context: %#v", backend.contexts[2])
	}
}

func TestUserContextIsSentOnlyWhenTheConversationLacksIt(t *testing.T) {
	backend := &contextRecordingBackend{}
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
	engine := New(backend, assistant.SendOptions{})
	userContext := func(context.Context) string { return env }
	turn := func() {
		drain(engine.StartTurn(context.Background(), TurnInput{Message: "hi", Tools: tools, UserContext: userContext}))
	}

	turn() // tool call, then continuation
	turn()
	env = "env-b"
	turn()
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	turn()

	want := []string{"env-a", "", "", "env-b", "env-b"}
	if !reflect.DeepEqual(backend.userContexts, want) {
		t.Fatalf("user contexts = %q, want %q", backend.userContexts, want)
	}
}

// historyBackend also serves conversation history.
type historyBackend struct {
	scriptBackend
	resp *assistant.ConversationHistoryResponse
	err  error
}

func (b *historyBackend) ConversationHistory(_ context.Context, _ assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
	return b.resp, b.err
}

func drain(ch <-chan Event) []Event {
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
			backend := &scriptBackend{msgs: []assistant.Message{assistant.AssistantMessage("started", assistant.Content{
				Type: assistant.ContentToolCallStarted,
				Tool: &assistant.ToolPayload{ToolCallID: "read-1", ToolName: "read_file", HasClientSide: test.known},
			})}}
			tools, err := NewToolSet(ModeSkipPermissions, Tool{
				Definition: assistant.ClientTool{Name: "read_file"},
				Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			events := drain(New(backend, assistant.SendOptions{}).StartTurn(context.Background(), TurnInput{Message: "read", Tools: tools}))
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
	e := New(&scriptBackend{}, assistant.SendOptions{})
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
	for _, test := range []struct {
		name   string
		blocks []Block
		want   []string
	}{
		{name: "empty transcript"},
		{
			name:   "user text in order, repeats kept",
			blocks: []Block{user("a"), answer, user("b\nc"), user("b\nc")},
			want:   []string{"a", "b\nc", "b\nc"},
		},
		{
			name:   "blank and body-less user blocks skipped",
			blocks: []Block{user(" "), {Role: assistant.RoleUser, Kind: assistant.KindText}, user("kept")},
			want:   []string{"kept"},
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

type streamedInputBackend struct {
	opts   []assistant.SendOptions
	calls  int
	server bool
}

func (b *streamedInputBackend) Send(_ context.Context, _ any, opts assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.opts = append(b.opts, opts)
	b.calls++
	if b.calls > 1 {
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("done"))
		return "conversation-1", emit(response)
	}
	content := assistant.Content{Type: assistant.ContentToolCallStarted, Tool: &assistant.ToolPayload{
		ToolCallID: "call-1", ToolName: "preview", IsClientSide: true,
	}}
	if b.server {
		content.Type = assistant.ContentToolCall
		content.Tool.IsClientSide = false
		content.Tool.Metadata = &assistant.ToolMetadata{Name: "preview", Input: `{"value":"server"}`}
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("server-call", content)
		return "conversation-1", emit(response)
	}
	for _, payload := range []assistant.Content{
		content,
		{Type: assistant.ContentToolCallInputDelta, Tool: &assistant.ToolPayload{ToolCallID: "call-1", PartialJSON: `{"value":`}},
		{Type: assistant.ContentToolCallInputDelta, Tool: &assistant.ToolPayload{ToolCallID: "call-1", PartialJSON: `"delta"}`}},
	} {
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("tool-call", payload)
		if err := emit(response); err != nil {
			return "conversation-1", err
		}
	}
	final := assistant.ToolCallContent("call-1", "preview", `{"value":"final"}`)
	final.Type = assistant.ContentClientToolCall
	var response assistant.AssistantResponse
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("client-call", final)
	return "conversation-1", emit(response)
}

func TestEngineReducesClientToolInputBeforeEventAndHandler(t *testing.T) {
	backend := &streamedInputBackend{}
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
	events := drain(New(backend, assistant.SendOptions{}).StartTurn(context.Background(), TurnInput{Message: "go", Tools: tools}))
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
	backend := &streamedInputBackend{server: true}
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
	drain(New(backend, assistant.SendOptions{}).StartTurn(context.Background(), TurnInput{Message: "go", Tools: tools}))
	if reducerCalls != 0 {
		t.Fatalf("server-side tool triggered %d reductions", reducerCalls)
	}
	if len(backend.opts) == 0 || !backend.opts[0].ClientTools[0].StreamInput {
		t.Fatalf("per-tool streaming not requested for reducer-backed tool: %+v", backend.opts)
	}
}

func TestEnginePreservesPerToolInputStreaming(t *testing.T) {
	backend := &conversationRecordingBackend{messages: make(map[string][]string)}
	tools, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "ordinary", StreamInput: true},
		Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	e := New(backend, assistant.SendOptions{})
	_ = drain(e.StartTurn(context.Background(), TurnInput{Message: "go", Tools: tools}))
	if len(backend.opts) == 0 || !backend.opts[0].ClientTools[0].StreamInput {
		t.Fatalf("caller-provided per-tool streaming was disabled: %+v", backend.opts)
	}
}

func TestTurnCompletionSynchronizesOperationRelease(t *testing.T) {
	e := New(&scriptBackend{convID: "conversation-1"}, assistant.SendOptions{})
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
	_ = drain(operation.events)
}

func TestConcurrentTurnReturnsOperationError(t *testing.T) {
	gate := make(chan struct{})
	e := New(&blockingBackend{gate: gate}, assistant.SendOptions{})
	ch := e.StartTurn(context.Background(), TurnInput{Message: "one"})
	events := drain(e.StartTurn(context.Background(), TurnInput{Message: "two"}))
	if len(events) != 1 || events[0].Kind != EventError || !errors.Is(events[0].Err, ErrOperationActive) {
		t.Fatalf("overlap events = %+v, want ErrOperationActive", events)
	}
	close(gate)
	_ = drain(ch)
}

func TestTurnEmitsEventSequence(t *testing.T) {
	usage := assistant.AssistantMessage("m1", assistant.TextContent(""))
	usage.Results = &assistant.Results{Usage: &assistant.Usage{TokensUsed: 5}}
	b := &scriptBackend{
		convID: "conv-1",
		msgs: []assistant.Message{
			assistant.AssistantMessage("m1", assistant.TextContent("Hello")),
			assistant.AssistantMessage("m1", assistant.TextContent(" world")),
			usage,
		},
	}
	e := New(b, assistant.SendOptions{})

	evs := drain(e.StartTurn(context.Background(), TurnInput{Message: "hi"}))
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
		msgs:   []assistant.Message{assistant.AssistantMessage("m1", assistant.TextContent("partial"))},
	}, assistant.SendOptions{})

	events := drain(e.StartTurn(context.Background(), TurnInput{Message: "question"}))
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
	backend := &conversationRecordingBackend{messages: map[string][]string{}}
	opts := assistant.SendOptions{
		ConversationID: "old-conversation",
		Model:          "retained-model",
		DebugTag:       "retained-debug-tag",
		MessageHistory: []json.RawMessage{json.RawMessage(`{"role":"user","content":"old history"}`)},
	}
	e := New(backend, opts)

	_ = drain(e.StartTurn(context.Background(), TurnInput{Message: "old prompt"}))
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

	_ = drain(e.StartTurn(context.Background(), TurnInput{Message: "new prompt"}))
	if got := e.ConversationID(); got != "new-conversation-1" {
		t.Fatalf("new conversation id = %q", got)
	}
	if got := backend.opts[len(backend.opts)-1]; got.ConversationID != "" ||
		len(got.MessageHistory) != 0 || got.Model != opts.Model ||
		got.DebugTag != opts.DebugTag {
		t.Fatalf("first new request options = %+v; process-wide configuration was not retained", got)
	}
	if got := backend.messages["old-conversation"]; !reflect.DeepEqual(got, []string{"old prompt"}) {
		t.Fatalf("prior conversation was mutated: %v", got)
	}
	if got := backend.messages["new-conversation-1"]; !reflect.DeepEqual(got, []string{"new prompt"}) {
		t.Fatalf("new conversation messages = %v", got)
	}
}

func TestNewConversationRetainsIDDiscoveredDuringCancellation(t *testing.T) {
	backend := &cancellationIDBackend{started: make(chan struct{})}
	e := New(backend, assistant.SendOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	turn := e.StartTurn(ctx, TurnInput{Message: "cancel me"})
	<-backend.started
	cancel()
	_ = drain(turn)

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

func TestNewConversationRejectsActiveTurnWithoutMutation(t *testing.T) {
	gate := make(chan struct{})
	e := New(&blockingBackend{gate: gate}, assistant.SendOptions{ConversationID: "old"})
	turn := e.StartTurn(context.Background(), TurnInput{Message: "busy"})
	if err := e.NewConversation(); !errors.Is(err, ErrOperationActive) {
		t.Fatalf("NewConversation error = %v, want ErrOperationActive", err)
	}
	if got := e.ConversationID(); got != "old" {
		t.Fatalf("active reset changed conversation id to %q", got)
	}
	close(gate)
	_ = drain(turn)
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
	e := New(&historyBackend{resp: resp}, assistant.SendOptions{ConversationID: testConversationID})

	evs := drain(e.Restore(context.Background()))
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
	e := New(&scriptBackend{}, assistant.SendOptions{ConversationID: testConversationID})

	evs := drain(e.Restore(context.Background()))
	if len(evs) != 1 || evs[0].Kind != EventError || !errors.Is(evs[0].Err, ErrHistoryUnsupported) {
		t.Fatalf("events = %+v, want ErrHistoryUnsupported", evs)
	}
}

// An empty history folds to no blocks, so the len>0 guard emits nothing.
func TestRestoreEmptyEmitsNothing(t *testing.T) {
	resp := &assistant.ConversationHistoryResponse{}
	resp.Data.Type = "conversation-history-response"
	e := New(&historyBackend{resp: resp}, assistant.SendOptions{ConversationID: testConversationID})

	if evs := drain(e.Restore(context.Background())); len(evs) != 0 {
		t.Fatalf("events = %v, want none", kinds(evs))
	}
}

type statusBackend struct{ calls int }

func (*statusBackend) Send(context.Context, any, assistant.SendOptions, func(assistant.AssistantResponse) error) (string, error) {
	return "", nil
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
	engine := New(backend, assistant.SendOptions{
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
	user  assistant.CurrentUser
	err   error
	calls int
}

func (*currentUserBackend) Send(context.Context, any, assistant.SendOptions, func(assistant.AssistantResponse) error) (string, error) {
	return "", nil
}

func (b *currentUserBackend) CurrentUser(context.Context) (assistant.CurrentUser, error) {
	b.calls++
	return b.user, b.err
}

func TestEngineCurrentUserUsesOptionalBackendCapability(t *testing.T) {
	want := assistant.CurrentUser{Name: "Bits User", Organization: "Bits Staging"}
	backend := &currentUserBackend{user: want}
	engine := New(backend, assistant.SendOptions{})

	got, err := engine.CurrentUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want || backend.calls != 1 {
		t.Fatalf("current user = %+v calls=%d, want %+v calls=1", got, backend.calls, want)
	}
}

func TestEngineCurrentUserRejectsUnsupportedBackend(t *testing.T) {
	engine := New(&scriptBackend{}, assistant.SendOptions{})

	_, err := engine.CurrentUser(context.Background())
	if !errors.Is(err, ErrCurrentUserUnsupported) {
		t.Fatalf("current user error = %v, want ErrCurrentUserUnsupported", err)
	}
}
