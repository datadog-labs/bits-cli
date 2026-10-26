package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

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
	for _, failure := range []error{nil, errors.New("background failed")} {
		name := "success"
		if failure != nil {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				old, current := newBackgroundStream(), newBackgroundStream()
				backend := &backgroundBackend{streams: map[string]*backgroundStream{"old": old, "current": current}}
				engine := New(backend, Config{})
				defer engine.Close()
				events, unsubscribe := engine.Subscribe()
				defer unsubscribe()
				oldRun, err := engine.StartTurn(context.Background(), TurnInput{Message: "old"})
				if err != nil {
					t.Fatal(err)
				}
				old.responses <- backgroundAnswer("old-answer", "first answer", true)
				backgroundEvent(t, events, oldRun, EventBackground)
				if engine.OperationActive() {
					t.Fatal("root completion kept the operation active")
				}
				currentRun, err := engine.StartTurn(context.Background(), TurnInput{Message: "current"})
				if err != nil {
					t.Fatal(err)
				}
				current.responses <- backgroundAnswer("current-answer", "still streaming", false)
				synctest.Wait()
				old.responses <- backgroundAnswer("child-answer", "child finished", false)
				old.finish <- failure
				terminal := EventDone
				if failure != nil {
					terminal = EventError
				}
				got := backgroundEvent(t, events, oldRun, terminal)
				if !errors.Is(got.Err, failure) {
					t.Fatalf("background error = %v, want %v", got.Err, failure)
				}
				if !engine.OperationActive() {
					t.Fatal("background terminal released foreground operation")
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
				old, current := newBackgroundStream(), newBackgroundStream()
				type contextKey struct{}
				type submission struct {
					owner  string
					result assistant.ClientToolResponse
				}
				submitted := make(chan submission, 2)
				backend := &backgroundBackend{streams: map[string]*backgroundStream{"old": old, "current": current}}
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
				engine := New(backend, Config{})
				defer engine.Close()
				events, unsubscribe := engine.Subscribe()
				defer unsubscribe()
				oldRun, err := engine.StartTurn(context.WithValue(context.Background(), contextKey{}, "old"), TurnInput{Message: "old", Tools: tools})
				if err != nil {
					t.Fatal(err)
				}
				old.responses <- backgroundAnswer("root", "children running", true)
				backgroundEvent(t, events, oldRun, EventBackground)
				currentRun, err := engine.StartTurn(context.WithValue(context.Background(), contextKey{}, "current"), TurnInput{Message: "current", Tools: tools})
				if err != nil {
					t.Fatal(err)
				}
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
				old, current := newBackgroundStream(), newBackgroundStream()
				late := backgroundAnswer("late-old-answer", "stale response", false)
				late.Data.Attributes.ConversationID = "old-conversation"
				old.late = &late
				engine := New(&backgroundBackend{streams: map[string]*backgroundStream{"old": old, "current": current}}, Config{})
				defer engine.Close()
				events, unsubscribe := engine.Subscribe()
				defer unsubscribe()
				run, err := engine.StartTurn(context.Background(), TurnInput{Message: "old"})
				if err != nil {
					t.Fatal(err)
				}
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
				if _, err := engine.StartTurn(context.Background(), TurnInput{Message: "current"}); err != nil {
					t.Fatal(err)
				}
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

func TestBackgroundParentCancellationPreservesForeground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old, current := newBackgroundStream(), newBackgroundStream()
		engine := New(&backgroundBackend{streams: map[string]*backgroundStream{"old": old, "current": current}}, Config{})
		defer engine.Close()
		events, unsubscribe := engine.Subscribe()
		defer unsubscribe()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		oldRun, err := engine.StartTurn(ctx, TurnInput{Message: "old"})
		if err != nil {
			t.Fatal(err)
		}
		old.responses <- backgroundAnswer("old-answer", "first answer", true)
		backgroundEvent(t, events, oldRun, EventBackground)
		currentRun, err := engine.StartTurn(context.Background(), TurnInput{Message: "current"})
		if err != nil {
			t.Fatal(err)
		}
		current.responses <- backgroundAnswer("current-answer", "still streaming", false)
		cancel()
		backgroundEvent(t, events, oldRun, EventCanceled)
		<-old.stopped
		if !engine.OperationActive() {
			t.Fatal("canceling background released foreground operation")
		}
		select {
		case <-current.stopped:
			t.Fatal("canceling background stopped foreground stream")
		default:
		}
		current.finish <- nil
		backgroundEvent(t, events, currentRun, EventDone)
	})
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
		old, current := newBackgroundStream(), newBackgroundStream()
		tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "write"}, Approval: func(call ToolCall) (ApprovalRequirement, bool) {
			return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: "same-resource"}}, true
		}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil }})
		if err != nil {
			t.Fatal(err)
		}
		e := New(&backgroundBackend{duplexBackend: duplexBackend{submit: func(context.Context, []assistant.ClientToolResponse) error { return nil }}, streams: map[string]*backgroundStream{"old": old, "current": current}}, Config{})
		defer e.Close()
		events, unsub := e.Subscribe()
		defer unsub()
		r, err := e.StartTurn(context.Background(), TurnInput{Message: "old", Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
		old.responses <- backgroundAnswer("answer", "done", true)
		backgroundEvent(t, events, r, EventBackground)
		_, err = e.StartTurn(context.Background(), TurnInput{Message: "current", Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
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
