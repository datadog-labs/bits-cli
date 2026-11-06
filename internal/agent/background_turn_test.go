package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

type backgroundStream struct {
	responses chan assistant.AssistantResponse
	finish    chan error
	stopped   chan struct{}
	late      *assistant.AssistantResponse
}

func newBackgroundStream() *backgroundStream {
	return &backgroundStream{responses: make(chan assistant.AssistantResponse), finish: make(chan error), stopped: make(chan struct{})}
}

type backgroundBackend struct {
	duplexBackend
	streams map[string]*backgroundStream
}

func (b *backgroundBackend) Stream(ctx context.Context, message string, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) error {
	s := b.streams[message]
	defer close(s.stopped)
	for {
		select {
		case response := <-s.responses:
			if err := emit(response); err != nil {
				return err
			}
		case err := <-s.finish:
			return err
		case <-ctx.Done():
			if s.late != nil {
				_ = emit(*s.late)
			}
			return ctx.Err()
		}
	}
}

// backgroundFixture owns only setup; tests control stream ordering explicitly.
type backgroundFixture struct {
	*Engine
	backend *backgroundBackend
	events  <-chan Event
}

func newBackgroundFixture(t *testing.T, names ...string) *backgroundFixture {
	t.Helper()
	backend := &backgroundBackend{streams: make(map[string]*backgroundStream)}
	for _, name := range names {
		backend.streams[name] = newBackgroundStream()
	}
	engine := New(backend, Config{})
	events, unsubscribe := engine.Subscribe()
	t.Cleanup(engine.Close)
	t.Cleanup(unsubscribe)
	return &backgroundFixture{engine, backend, events}
}

func (f *backgroundFixture) start(t *testing.T, ctx context.Context, name string, tools *ToolSet) RunID {
	t.Helper()
	run, err := f.StartTurn(ctx, TurnInput{Message: name, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func backgroundAnswer(id, text string, completed bool) assistant.AssistantResponse {
	var response assistant.AssistantResponse
	response.Data.Attributes.StructuredMessage = textMsg(id, text)
	response.RootTurnCompleted = completed
	return response
}

func backgroundEvent(t *testing.T, events <-chan Event, run RunID, kind EventKind) Event {
	t.Helper()
	for event := range events {
		if event.Run == run && event.Kind == kind {
			return event
		}
		if event.Run == run && (event.Kind == EventDone || event.Kind == EventError || event.Kind == EventCanceled) {
			t.Fatalf("run %v ended with %+v before event %v", run, event, kind)
		}
	}
	t.Fatalf("subscription closed before run %v event %v", run, kind)
	return Event{}
}

func TestBackgroundCompletionPreservesForegroundOperation(t *testing.T) {
	for _, failure := range []error{nil, errors.New("background failed"), context.Canceled} {
		name := "success"
		if failure != nil {
			name = failure.Error()
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newBackgroundFixture(t, "old", "current")
				engine, events := f.Engine, f.events
				old, current := f.backend.streams["old"], f.backend.streams["current"]
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				oldRun := f.start(t, ctx, "old", nil)
				old.responses <- backgroundAnswer("old-answer", "first answer", true)
				backgroundEvent(t, events, oldRun, EventBackground)
				if engine.OperationActive() {
					t.Fatal("root completion kept the operation active")
				}
				currentRun := f.start(t, context.Background(), "current", nil)
				current.responses <- backgroundAnswer("current-answer", "still streaming", false)
				synctest.Wait()
				old.responses <- backgroundAnswer("child-answer", "child finished", false)
				synctest.Wait()
				terminal := EventDone
				if errors.Is(failure, context.Canceled) {
					cancel()
					terminal = EventCanceled
				} else {
					old.finish <- failure
					if failure != nil {
						terminal = EventError
					}
				}
				got := backgroundEvent(t, events, oldRun, terminal)
				if !errors.Is(got.Err, failure) {
					t.Fatalf("background error = %v, want %v", got.Err, failure)
				}
				if !engine.OperationActive() {
					t.Fatal("background terminal released foreground operation")
				}
				<-old.stopped
				select {
				case <-current.stopped:
					t.Fatal("background terminal stopped foreground stream")
				default:
				}
				foundCurrent, foundChild := false, false
				for _, block := range engine.State().Blocks {
					if block.MessageID == "current-answer" {
						foundCurrent = true
						if block.Complete {
							t.Fatal("background terminal finalized foreground text")
						}
					}
					if block.MessageID == "child-answer" {
						foundChild = true
					}
				}
				if !foundCurrent || !foundChild {
					t.Fatalf("missing foreground or child answer: %+v", engine.State().Blocks)
				}
				current.finish <- nil
				backgroundEvent(t, events, currentRun, EventDone)
				if engine.OperationActive() {
					t.Fatal("foreground completion kept operation active")
				}
			})
		})
	}
}

func TestBackgroundApprovalsAndToolResultsStayWithTheirRun(t *testing.T) {
	for _, stopOld := range []bool{false, true} {
		name := "approve background"
		if stopOld {
			name = "stop background tools"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newBackgroundFixture(t, "old", "current")
				old, current := f.backend.streams["old"], f.backend.streams["current"]
				type contextKey struct{}
				type submission struct {
					owner  string
					result assistant.ClientToolResponse
				}
				submitted := make(chan submission, 2)
				backend := f.backend
				backend.submit = func(ctx context.Context, results []assistant.ClientToolResponse) error {
					for _, result := range results {
						submitted <- submission{ctx.Value(contextKey{}).(string), result}
					}
					return nil
				}
				tools, err := NewToolSet(Tool{
					Definition: assistant.ClientTool{Name: "write"},
					Approval: func(call ToolCall) (ApprovalRequirement, bool) {
						return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: call.ID}}, true
					},
					Handler: func(_ context.Context, call ToolCall) (ToolResult, error) { return ToolResult{Output: call.ID}, nil },
				})
				if err != nil {
					t.Fatal(err)
				}
				engine, events := f.Engine, f.events
				oldRun := f.start(t, context.WithValue(context.Background(), contextKey{}, "old"), "old", tools)
				old.responses <- backgroundAnswer("root", "children running", true)
				backgroundEvent(t, events, oldRun, EventBackground)
				currentRun := f.start(t, context.WithValue(context.Background(), contextKey{}, "current"), "current", tools)
				old.responses <- duplexCall("old-tool", "write")
				current.responses <- duplexCall("current-tool", "write")
				synctest.Wait()
				for _, owner := range []string{"old", "current"} {
					status, output := assistant.ToolStatusSuccess, owner+"-tool"
					if stopOld && owner == "old" {
						if err := engine.StopToolsFor(owner + "-tool"); err != nil {
							t.Fatal(err)
						}
						status, output = assistant.ToolStatusError, "tool execution was cancelled by the user"
						if !engine.OperationActive() {
							t.Fatal("stopping background tools released foreground")
						}
					} else if err := engine.Decide(owner+"-tool", ApprovalAllowOnce); err != nil {
						t.Fatalf("approve %s: %v", owner, err)
					}
					got := <-submitted
					if got.owner != owner || got.result.ToolCallID != owner+"-tool" || got.result.Metadata.Output != output || got.result.Status != status {
						t.Fatalf("submission for %s = %+v", owner, got)
					}
				}
				old.finish <- nil
				backgroundEvent(t, events, oldRun, EventDone)
				current.finish <- nil
				backgroundEvent(t, events, currentRun, EventDone)
			})
		})
	}
}

func TestBackgroundConversationResetAndCloseCancelRuns(t *testing.T) {
	for _, reset := range []bool{false, true} {
		name := "close"
		if reset {
			name = "new conversation"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newBackgroundFixture(t, "old", "current")
				engine, events := f.Engine, f.events
				old, current := f.backend.streams["old"], f.backend.streams["current"]
				late := backgroundAnswer("late-old-answer", "stale response", false)
				late.Data.Attributes.ConversationID = "old-conversation"
				old.late = &late
				run := f.start(t, context.Background(), "old", nil)
				old.responses <- backgroundAnswer("old-answer", "first answer", true)
				backgroundEvent(t, events, run, EventBackground)
				if reset {
					if err := engine.NewConversation(); err != nil {
						t.Fatal(err)
					}
					<-old.stopped
					synctest.Wait()
					if state := engine.State(); len(state.Blocks) != 0 || state.ConversationID != "" {
						t.Fatalf("old run repopulated reset conversation: %+v", state)
					}
				}
				f.start(t, context.Background(), "current", nil)
				synctest.Wait()
				engine.Close()
				<-old.stopped
				<-current.stopped
				if _, err := engine.StartTurn(context.Background(), TurnInput{Message: "closed"}); !errors.Is(err, ErrClosed) {
					t.Fatalf("start after close = %v", err)
				}
			})
		})
	}
}

func TestBackgroundStopWaitsForCleanupAfterConversationReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		canceled, release := make(chan struct{}), make(chan struct{})
		backend := &duplexBackend{stream: func(ctx context.Context, emit func(assistant.AssistantResponse) error) error {
			if err := emit(backgroundAnswer("root", "children running", true)); err != nil {
				return err
			}
			<-ctx.Done()
			close(canceled)
			<-release
			return ctx.Err()
		}}
		engine := New(backend, Config{})
		defer engine.Close()
		events, unsubscribe := engine.Subscribe()
		defer unsubscribe()
		run, err := engine.StartTurn(context.Background(), TurnInput{Message: "start"})
		if err != nil {
			t.Fatal(err)
		}
		backgroundEvent(t, events, run, EventBackground)
		if err := engine.NewConversation(); err != nil {
			t.Fatal(err)
		}
		<-canceled
		done := make(chan struct{})
		var stopErr error
		go func() { stopErr = engine.StopBackground(context.Background()); close(done) }()
		synctest.Wait()
		early := false
		select {
		case <-done:
			early = true
		default:
		}
		close(release)
		<-done
		if early {
			t.Error("StopBackground returned while previous conversation's transport was still cleaning up")
		}
		if stopErr != nil {
			t.Fatal(stopErr)
		}
	})
}

func TestBackgroundTurnSessionGrantCoversAllRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newBackgroundFixture(t, "old", "current")
		old, current := f.backend.streams["old"], f.backend.streams["current"]
		tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "write"}, Approval: func(call ToolCall) (ApprovalRequirement, bool) {
			return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: "same-resource"}}, true
		}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil }})
		if err != nil {
			t.Fatal(err)
		}
		f.backend.submit = func(context.Context, []assistant.ClientToolResponse) error { return nil }
		e, events := f.Engine, f.events
		r := f.start(t, context.Background(), "old", tools)
		old.responses <- backgroundAnswer("answer", "done", true)
		backgroundEvent(t, events, r, EventBackground)
		f.start(t, context.Background(), "current", tools)
		old.responses <- duplexCall("old-tool", "write")
		current.responses <- duplexCall("current-tool", "write")
		synctest.Wait()
		if err = e.GrantSessionFor("old-tool"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		for _, b := range e.State().Blocks {
			if b.Tool != nil && b.Tool.Status == ToolAwaitingApproval {
				t.Errorf("same-key tool %s still awaiting approval after session grant", b.ToolCallID())
			}
		}
	})
}

// mixedBackend selects the regular request-round transport for the newer turn.
type mixedBackend struct {
	*backgroundBackend
	send func(context.Context, any, assistant.SendOptions, func(assistant.AssistantResponse) error) (string, error)
}

func (b *mixedBackend) CanStream(message string, _ assistant.SendOptions) bool {
	return message == "old"
}

func (b *mixedBackend) Send(ctx context.Context, message any, opts assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	return b.send(ctx, message, opts, emit)
}

func TestBackgroundStreamOverlapsRegularToolRound(t *testing.T) {
	for _, backgroundFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("background_first_%t", backgroundFirst), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				old := newBackgroundStream()
				regularResults := make(chan []assistant.ClientToolResponse, 1)
				childResults := make(chan []assistant.ClientToolResponse, 1)
				finishRegular := make(chan struct{})
				backend := &mixedBackend{backgroundBackend: &backgroundBackend{streams: map[string]*backgroundStream{"old": old}}}
				backend.submit = func(_ context.Context, results []assistant.ClientToolResponse) error {
					childResults <- results
					return nil
				}
				backend.send = func(_ context.Context, message any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
					if results, ok := message.([]assistant.ClientToolResponse); ok {
						regularResults <- results
						<-finishRegular
						return "conversation", emit(backgroundAnswer("regular-answer", "regular final", false))
					}
					for _, id := range []string{"first", "second"} {
						if err := emit(duplexCall(id, "write")); err != nil {
							return "conversation", err
						}
					}
					return "conversation", nil
				}
				tools, err := NewToolSet(Tool{
					Definition: assistant.ClientTool{Name: "write"},
					Approval: func(call ToolCall) (ApprovalRequirement, bool) {
						return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: call.ID}}, true
					},
					Handler: func(_ context.Context, call ToolCall) (ToolResult, error) { return ToolResult{Output: call.ID}, nil },
				})
				if err != nil {
					t.Fatal(err)
				}
				engine := New(backend, Config{ConversationID: "conversation"})
				defer engine.Close()
				events, unsubscribe := engine.Subscribe()
				defer unsubscribe()
				first, err := engine.StartTurn(context.Background(), TurnInput{Message: "old", Tools: tools})
				if err != nil {
					t.Fatal(err)
				}
				old.responses <- backgroundAnswer("initial", "initial answer", true)
				backgroundEvent(t, events, first, EventBackground)
				second, err := engine.StartTurn(context.Background(), TurnInput{Message: "regular", Tools: tools})
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				old.responses <- duplexCall("child", "write")
				old.responses <- backgroundAnswer("followup", "child followup", false)
				synctest.Wait()
				if len(engine.State().PendingApprovals()) != 3 {
					t.Fatal("both transports must expose their pending approvals")
				}
				for _, id := range []string{"child", "second", "first"} {
					if err := engine.Decide(id, ApprovalAllowOnce); err != nil {
						t.Fatal(err)
					}
				}
				if got := <-childResults; len(got) != 1 || got[0].ToolCallID != "child" {
					t.Fatalf("child results: %+v", got)
				}
				if got := <-regularResults; len(got) != 2 || got[0].ToolCallID != "first" || got[1].ToolCallID != "second" {
					t.Fatalf("regular results lost wire order: %+v", got)
				}
				finishOld := func() { old.finish <- nil; backgroundEvent(t, events, first, EventDone) }
				finishNew := func() { close(finishRegular); backgroundEvent(t, events, second, EventDone) }
				if backgroundFirst {
					finishOld()
					if !engine.OperationActive() {
						t.Fatal("background completion released regular foreground")
					}
					finishNew()
				} else {
					finishNew()
					finishOld()
				}
				if engine.OperationActive() {
					t.Fatal("completed foreground still holds admission")
				}
				found := make(map[string]bool)
				for _, block := range engine.State().Blocks {
					owner := map[string]RunID{"followup": first, "regular-answer": second}[block.MessageID]
					if owner != 0 {
						found[block.MessageID] = true
						if block.Run != owner || !block.Complete {
							t.Fatalf("wrong block ownership or completion: %+v", block)
						}
					}
				}
				if len(found) != 2 {
					t.Fatalf("missing response blocks: %v", found)
				}
			})
		})
	}
}

func TestForegroundCleanupKeepsAdmissionButRejectsCommands(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := newBackgroundStream()
		started, joining, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		submitted := make(chan []assistant.ClientToolResponse, 1)
		backend := &mixedBackend{backgroundBackend: &backgroundBackend{streams: map[string]*backgroundStream{"old": old}}}
		backend.submit = func(_ context.Context, results []assistant.ClientToolResponse) error {
			submitted <- results
			return nil
		}
		backend.send = func(ctx context.Context, _ any, _ assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
			close(started)
			<-ctx.Done()
			close(joining)
			<-release
			return "late-conversation", ctx.Err()
		}
		tools, err := NewToolSet(Tool{
			Definition: assistant.ClientTool{Name: "write"},
			Approval: func(call ToolCall) (ApprovalRequirement, bool) {
				return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: call.ID}}, true
			},
			Handler: func(_ context.Context, call ToolCall) (ToolResult, error) { return ToolResult{Output: call.ID}, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		engine := New(backend, Config{})
		defer engine.Close()
		events, unsubscribe := engine.Subscribe()
		defer unsubscribe()
		first, err := engine.StartTurn(context.Background(), TurnInput{Message: "old", Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
		old.responses <- backgroundAnswer("initial", "initial", true)
		backgroundEvent(t, events, first, EventBackground)
		old.responses <- duplexCall("child", "write")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		second, err := engine.StartTurn(ctx, TurnInput{Message: "regular"})
		if err != nil {
			t.Fatal(err)
		}
		<-started
		cancel()
		<-joining
		synctest.Wait()
		_ = engine.State()
		if err := engine.StopTools(); !errors.Is(err, ErrStale) {
			t.Fatalf("joining foreground accepted stop: %v", err)
		}
		if err := engine.Decide("child", ApprovalAllowOnce); err != nil {
			t.Fatal(err)
		}
		if got := <-submitted; got[0].ToolCallID != "child" {
			t.Fatalf("background decision misrouted: %+v", got)
		}
		if _, err := engine.StartTurn(context.Background(), TurnInput{Message: "blocked"}); !errors.Is(err, ErrOperationActive) {
			t.Fatalf("new turn during cleanup: %v", err)
		}
		if err := engine.NewConversation(); !errors.Is(err, ErrOperationActive) {
			t.Fatalf("new conversation during cleanup: %v", err)
		}
		close(release)
		var final State
		for event := range Follow(events, second) {
			if event.Kind == EventState {
				final = event.State
			}
			if event.Terminal() && event.Kind != EventCanceled {
				t.Fatalf("terminal: %+v", event)
			}
		}
		if final.ConversationID != "late-conversation" {
			t.Fatalf("late conversation absent from terminal state: %+v", final)
		}
		old.finish <- nil
		backgroundEvent(t, events, first, EventDone)
		if err := engine.NewConversation(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCancellationDuringPreparationNeverRegistersTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		preparing, release := make(chan struct{}), make(chan struct{})
		backend := &scriptBackend{}
		engine := New(backend, Config{UserContext: func(context.Context) string { close(preparing); <-release; return "context" }})
		defer engine.Close()
		events, unsubscribe := engine.Subscribe()
		defer unsubscribe()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		run, err := engine.StartTurn(ctx, TurnInput{Message: "cancel during preparation"})
		if err != nil {
			t.Fatal(err)
		}
		<-preparing
		cancel()
		close(release)
		backgroundEvent(t, events, run, EventCanceled)
		if len(backend.sent) != 0 || engine.OperationActive() {
			t.Fatal("cancelled preparation sent a request or retained admission")
		}
		if err := engine.StopTools(); !errors.Is(err, ErrStale) {
			t.Fatalf("cancelled preparation registered a turn: %v", err)
		}
		if err := engine.NewConversation(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestForegroundTransportCleanupIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		backend := &mixedBackend{backgroundBackend: &backgroundBackend{}}
		backend.send = func(ctx context.Context, _ any, _ assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
			close(started)
			<-ctx.Done()
			<-release
			return "", ctx.Err()
		}
		engine := New(backend, Config{})
		defer engine.Close()
		events, unsubscribe := engine.Subscribe()
		defer unsubscribe()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		run, err := engine.StartTurn(ctx, TurnInput{Message: "regular"})
		if err != nil {
			t.Fatal(err)
		}
		<-started
		start := time.Now()
		cancel()
		backgroundEvent(t, events, run, EventCanceled)
		if elapsed := time.Since(start); elapsed != 30*time.Second {
			t.Fatalf("transport cleanup took %s, want 30s", elapsed)
		}
		if engine.OperationActive() {
			t.Fatal("timed out cleanup retained admission")
		}
		engine.Close()
		close(release)
	})
}
