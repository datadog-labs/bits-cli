package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

type duplexBackend struct {
	stream func(context.Context, func(assistant.AssistantResponse) error) error
	submit func(context.Context, []assistant.ClientToolResponse) error
}

func (b *duplexBackend) Send(context.Context, any, assistant.SendOptions, func(assistant.AssistantResponse) error) (string, error) {
	return "", errors.New("duplex backend must not use sequential Send")
}

func (b *duplexBackend) Stream(ctx context.Context, _ string, _ assistant.SendOptions, fn func(assistant.AssistantResponse) error) error {
	return b.stream(ctx, fn)
}

func (b *duplexBackend) SubmitToolResults(ctx context.Context, results []assistant.ClientToolResponse) error {
	return b.submit(ctx, results)
}

func duplexCall(id, name string) assistant.AssistantResponse {
	var response assistant.AssistantResponse
	content := assistant.ToolCallContent(id, name, `{}`)
	content.Type = assistant.ContentClientToolCall
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("message-"+id, content)
	return response
}

func TestDuplexPublishesFinalizedTranscriptBeforeTerminalEvent(t *testing.T) {
	for _, failure := range []error{nil, errors.New("stream failed")} {
		name := "success"
		terminal := EventDone
		if failure != nil {
			name, terminal = "failure", EventError
		}
		t.Run(name, func(t *testing.T) {
			backend := &duplexBackend{stream: func(_ context.Context, emit func(assistant.AssistantResponse) error) error {
				var response assistant.AssistantResponse
				response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("answer text"))
				if err := emit(response); err != nil {
					return err
				}
				return failure
			}}
			engine := New(backend, Config{})
			events := coalesced(collect(turnEvents(engine, context.Background(), TurnInput{Message: "question"})))
			want := []EventKind{EventTranscript, EventTranscript, terminal}
			if got := kinds(events); !reflect.DeepEqual(got, want) {
				t.Fatalf("event kinds = %v, want %v", got, want)
			}
			final := events[1].Transcript.Blocks
			if len(final) != 2 || !final[1].Complete || events[1].Round == 0 {
				t.Fatalf("final transcript = %+v", events[1])
			}
			if !errors.Is(events[2].Err, failure) {
				t.Fatalf("terminal error = %v, want %v", events[2].Err, failure)
			}
		})
	}
}

func TestDuplexOverlapsSeparatelyArrivingToolsAndSubmitsDuringStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	firstStarted := make(chan struct{})
	secondSubmitted := make(chan struct{})
	submitted := make(chan string, 2)
	backend := &duplexBackend{
		stream: func(ctx context.Context, emit func(assistant.AssistantResponse) error) error {
			if err := emit(duplexCall("first", "read")); err != nil {
				return err
			}
			select {
			case <-firstStarted:
			case <-ctx.Done():
				return ctx.Err()
			}
			if err := emit(duplexCall("second", "read")); err != nil {
				return err
			}
			for _, want := range []string{"second", "first"} {
				select {
				case got := <-submitted:
					if got != want {
						return errors.New("tools did not submit independently")
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		},
		submit: func(_ context.Context, results []assistant.ClientToolResponse) error {
			for _, result := range results {
				submitted <- result.ToolCallID
				if result.ToolCallID == "second" {
					close(secondSubmitted)
				}
			}
			return nil
		},
	}
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(ctx context.Context, call ToolCall) (ToolResult, error) {
		if call.ID == "first" {
			close(firstStarted)
			select {
			case <-secondSubmitted:
			case <-ctx.Done():
				return ToolResult{}, ctx.Err()
			}
		}
		return ToolResult{Output: call.ID}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, Config{PermissionsMode: ModeSkipPermissions})
	eventsCh := turnEvents(engine, ctx, TurnInput{Message: "read both", Tools: tools})
	events := collect(eventsCh)
	terminal := events[len(events)-1]
	if terminal.Kind != EventDone || terminal.Err != nil {
		t.Fatalf("terminal = %+v", terminal)
	}
}

func TestDuplexTerminalFailureCancelsUncooperativeHandler(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	failure := errors.New("server agent failed")
	backend := &duplexBackend{stream: func(ctx context.Context, emit func(assistant.AssistantResponse) error) error {
		if err := emit(duplexCall("slow", "read")); err != nil {
			return err
		}
		select {
		case <-started:
			return failure
		case <-ctx.Done():
			return ctx.Err()
		}
	}, submit: func(context.Context, []assistant.ClientToolResponse) error { return nil }}
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return ToolResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, Config{PermissionsMode: ModeSkipPermissions})
	eventsCh := turnEvents(engine, ctx, TurnInput{Message: "read", Tools: tools})
	events := collect(eventsCh)
	terminal := events[len(events)-1]
	if !errors.Is(terminal.Err, failure) {
		t.Fatalf("terminal = %+v", terminal)
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("handler was not cancelled")
	}
	if len(events) == 0 || !events[len(events)-1].BackendFailure {
		t.Fatalf("events = %+v", events)
	}
}

func TestDuplexApprovalDenialIsSubmittedAndControlsFutureCalls(t *testing.T) {
	for _, policy := range []DenyPolicy{DenyStop, DenyContinue} {
		t.Run(map[DenyPolicy]string{DenyStop: "stop", DenyContinue: "continue"}[policy], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var executions atomic.Int32
			results := make(chan assistant.ClientToolResponse, 2)
			backend := &duplexBackend{
				stream: func(ctx context.Context, emit func(assistant.AssistantResponse) error) error {
					if err := emit(duplexCall("gated", "write")); err != nil {
						return err
					}
					select {
					case result := <-results:
						if result.ToolCallID != "gated" || result.Status != assistant.ToolStatusError || result.Metadata.Output != "local execution was denied by the user" {
							return errors.New("missing wire denial")
						}
					case <-ctx.Done():
						return ctx.Err()
					}
					if err := emit(duplexCall("later", "read")); err != nil {
						return err
					}
					select {
					case result := <-results:
						if result.ToolCallID != "later" {
							return errors.New("wrong follow-up tool")
						}
						if policy == DenyStop && result.Metadata.Output != "tool execution was cancelled by the user" {
							return errors.New("follow-up not cancelled")
						}
						if policy == DenyContinue && result.Status != assistant.ToolStatusSuccess {
							return errors.New("follow-up not executed")
						}
					case <-ctx.Done():
						return ctx.Err()
					}
					var answer assistant.AssistantResponse
					answer.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("after denial"))
					return emit(answer)
				},
				submit: func(ctx context.Context, responses []assistant.ClientToolResponse) error {
					for _, response := range responses {
						select {
						case results <- response:
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					return nil
				},
			}
			handler := func(context.Context, ToolCall) (ToolResult, error) {
				executions.Add(1)
				return ToolResult{Output: "ok"}, nil
			}
			tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "write"}, Handler: handler, Approval: func(ToolCall) (ApprovalRequirement, bool) {
				return ApprovalRequirement{Key: ApprovalKey{Tool: "write"}}, true
			}},
				Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: handler},
			)
			if err != nil {
				t.Fatal(err)
			}
			engine := New(backend, Config{})
			eventsCh := turnEvents(engine, ctx, TurnInput{Message: "write", Tools: tools, OnDeny: policy})
			decided := false
			var events []Event
			for event := range eventsCh {
				events = append(events, event)
				if !decided && len(event.Transcript.PendingApprovals()) > 0 {
					decided = engine.Decide("gated", ApprovalDeny) == nil
				}
			}
			terminal := events[len(events)-1]
			if !decided || terminal.Kind != EventDone || terminal.Err != nil {
				t.Fatalf("terminal = %+v; decided = %v", terminal, decided)
			}
			wantRuns := int32(0)
			if policy == DenyContinue {
				wantRuns = 1
			}
			if got := executions.Load(); got != wantRuns {
				t.Fatalf("executions = %d, want %d", got, wantRuns)
			}
			for _, block := range engine.Snapshot() {
				if policy == DenyStop && block.MessageID == "answer" {
					t.Fatal("stopped run exposed follow-up answer")
				}
			}
		})
	}
}

// A failed submission stops sibling calls at once: stream cancellation may
// wait on the server, as the multi-agent client's cancel request does.
func TestDuplexSubmissionFailureStopsSiblingsBeforeStreamCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	siblingStarted, siblingStopped := make(chan struct{}), make(chan struct{})
	failure := errors.New("result POST failed")
	backend := &duplexBackend{stream: func(streamCtx context.Context, emit func(assistant.AssistantResponse) error) error {
		for _, id := range []string{"slow", "read"} {
			if err := emit(duplexCall(id, id)); err != nil {
				return err
			}
		}
		<-streamCtx.Done()
		select {
		case <-siblingStopped:
		case <-ctx.Done():
			t.Error("sibling handler kept running during stream cleanup")
		}
		return streamCtx.Err()
	}, submit: func(context.Context, []assistant.ClientToolResponse) error { return failure }}
	tools, err := NewToolSet(
		// The failing answer waits for the sibling to run: a sibling cancelled
		// before it starts never runs at all.
		Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
			select {
			case <-siblingStarted:
			case <-ctx.Done():
			}
			return ToolResult{Output: "ok"}, nil
		}},
		Tool{Definition: assistant.ClientTool{Name: "slow"}, Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
			close(siblingStarted)
			<-ctx.Done()
			close(siblingStopped)
			return ToolResult{}, ctx.Err()
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, Config{PermissionsMode: ModeSkipPermissions})
	events := collect(turnEvents(engine, ctx, TurnInput{Message: "read", Tools: tools}))
	terminal := events[len(events)-1]
	if terminal.Kind != EventError || !errors.Is(terminal.Err, failure) || !terminal.BackendFailure {
		t.Fatalf("terminal = %+v", terminal)
	}
	if !hasToolStatus(events, "slow", ToolCancelled) {
		t.Fatal("stopped sibling was not settled as cancelled")
	}
}

func TestDuplexBoundsExecutionAcrossArrivingBatches(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{}, 64)
	var count atomic.Int32
	backend := &duplexBackend{stream: func(ctx context.Context, emit func(assistant.AssistantResponse) error) error {
		for i := range 64 {
			if err := emit(duplexCall(fmt.Sprintf("read-%d", i), "read")); err != nil {
				return err
			}
		}
		for range 16 {
			select {
			case <-started:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}, submit: func(context.Context, []assistant.ClientToolResponse) error { return nil }}
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
		count.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		return ToolResult{}, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, Config{PermissionsMode: ModeSkipPermissions})
	eventsCh := turnEvents(engine, ctx, TurnInput{Message: "read many", Tools: tools})
	events := collect(eventsCh)
	terminal := events[len(events)-1]
	if terminal.Kind != EventDone || terminal.Err != nil {
		t.Fatalf("terminal = %+v", terminal)
	}
	if got := count.Load(); got > 16 {
		t.Fatalf("started %d blocking handlers; expected at most 16 across all batches", got)
	}
	for _, block := range engine.Snapshot() {
		if block.Tool != nil && block.Tool.Status != ToolCancelled {
			t.Fatalf("tool %s was not cancelled at terminal: %+v", block.ToolCallID(), block.Tool)
		}
	}
}

func TestDuplexWaitsForTransportCleanupAfterLocalFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	defer close(releaseCleanup)
	failure := errors.New("local handler failed")
	backend := &duplexBackend{stream: func(ctx context.Context, emit func(assistant.AssistantResponse) error) error {
		if err := emit(duplexCall("read", "read")); err != nil {
			return err
		}
		<-ctx.Done()
		close(cleanupStarted)
		select {
		case <-releaseCleanup:
		case <-time.After(time.Second):
		}
		return ctx.Err()
	}, submit: func(context.Context, []assistant.ClientToolResponse) error { return nil }}
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, failure }})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, Config{PermissionsMode: ModeSkipPermissions})
	eventsCh := turnEvents(engine, ctx, TurnInput{Message: "read", Tools: tools})
	finished := make(chan struct{})
	var events []Event
	go func() { events = collect(eventsCh); close(finished) }()
	select {
	case <-cleanupStarted:
	case <-ctx.Done():
		t.Fatal("cleanup did not start")
	}
	select {
	case <-finished:
		t.Fatal("engine returned before transport cancellation cleanup")
	case <-time.After(25 * time.Millisecond):
	}
	releaseCleanup <- struct{}{}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("engine did not return after transport cleanup")
	}
	terminal := events[len(events)-1]
	if !errors.Is(terminal.Err, failure) {
		t.Fatalf("terminal = %+v", terminal)
	}
}

func TestDuplexPermissionChangeReleasesParkedTools(t *testing.T) {
	for _, serverGate := range []bool{false, true} {
		t.Run(fmt.Sprintf("server_gate_%t", serverGate), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			results := make(chan assistant.ClientToolResponse, 2)
			backend := &duplexBackend{
				stream: func(ctx context.Context, emit func(assistant.AssistantResponse) error) error {
					for _, id := range []string{"first", "second"} {
						response := duplexCall(id, "write")
						if serverGate {
							tool := response.Data.Attributes.StructuredMessage.Content.Tool
							tool.Metadata.Name = assistant.ApprovalRequestTool
							tool.Metadata.Input = fmt.Sprintf(`{"tool_name":"write_dashboard","tool_args":{},"tool_call_id":%q}`, id)
						}
						if err := emit(response); err != nil {
							return err
						}
					}
					seen := map[string]bool{}
					for range 2 {
						select {
						case result := <-results:
							if result.Status != assistant.ToolStatusSuccess || seen[result.ToolCallID] {
								return fmt.Errorf("unexpected result: %+v", result)
							}
							seen[result.ToolCallID] = true
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					return nil
				},
				submit: func(ctx context.Context, responses []assistant.ClientToolResponse) error {
					select {
					case results <- responses[0]:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				},
			}
			var runs atomic.Int32
			tools, err := NewToolSet(Tool{
				Definition: assistant.ClientTool{Name: "write"},
				Approval: func(call ToolCall) (ApprovalRequirement, bool) {
					return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: call.ID}}, true
				},
				Handler: func(context.Context, ToolCall) (ToolResult, error) {
					runs.Add(1)
					return ToolResult{Output: "written"}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			engine := New(backend, Config{})
			eventsCh := turnEvents(engine, ctx, TurnInput{Message: "write both", Tools: tools})
			waitForToolStatusEvents(t, eventsCh, "second", ToolAwaitingApproval)
			if err := engine.SetPermissionsMode(ModeSkipPermissions); err != nil {
				t.Fatal(err)
			}
			events := collect(eventsCh)
			terminal := events[len(events)-1]
			if terminal.Kind != EventDone || terminal.Err != nil {
				t.Fatalf("terminal = %+v", terminal)
			}
			wantRuns := int32(2)
			if serverGate {
				wantRuns = 0
			}
			if runs.Load() != wantRuns {
				t.Fatalf("local executions = %d, want %d", runs.Load(), wantRuns)
			}
		})
	}
}

func TestDuplexLateChildResultPreservesTerminalOutcome(t *testing.T) {
	conflict := &assistant.APIError{StatusCode: 409}
	failure := errors.New("run failed")
	unauthorized := &assistant.APIError{StatusCode: 401}
	for _, tc := range []struct {
		name          string
		submissionErr error
		terminalErr   error
		noTerminal    bool
		wantErr       error
		wantDelay     time.Duration
	}{
		{name: "retired batch", submissionErr: conflict, wantDelay: time.Second},
		{name: "retired run", submissionErr: &assistant.APIError{StatusCode: 404}, wantDelay: time.Second},
		{name: "closed stream", submissionErr: assistant.ErrNoActiveMultiAgentRun, wantDelay: time.Second},
		{name: "failed terminal", submissionErr: conflict, terminalErr: failure, wantErr: failure, wantDelay: time.Second},
		{name: "unresolved conflict", submissionErr: conflict, noTerminal: true, wantErr: conflict, wantDelay: 30 * time.Second},
		{name: "unauthorized", submissionErr: unauthorized, wantErr: unauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				submitted := make(chan struct{})
				backend := &duplexBackend{
					stream: func(ctx context.Context, emit func(assistant.AssistantResponse) error) error {
						if err := emit(duplexCall("child-read", "read")); err != nil {
							return err
						}
						<-submitted
						if tc.noTerminal {
							<-ctx.Done()
							return ctx.Err()
						}
						// The root has finished, but the server still has child resources to close.
						select {
						case <-time.After(time.Second):
							return tc.terminalErr
						case <-ctx.Done():
							return ctx.Err()
						}
					},
					submit: func(context.Context, []assistant.ClientToolResponse) error {
						close(submitted)
						return tc.submissionErr
					},
				}
				tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil }})
				if err != nil {
					t.Fatal(err)
				}
				engine := New(backend, Config{PermissionsMode: ModeSkipPermissions})
				defer engine.Close() // the bubble requires its goroutines to exit
				start := time.Now()
				events := collect(turnEvents(engine, context.Background(), TurnInput{Message: "question", Tools: tools}))
				terminal := events[len(events)-1]
				wantKind := EventDone
				if tc.wantErr != nil {
					wantKind = EventError
				}
				if terminal.Kind != wantKind || !errors.Is(terminal.Err, tc.wantErr) {
					t.Fatalf("terminal = %+v, want kind %v error %v", terminal, wantKind, tc.wantErr)
				}
				if elapsed := time.Since(start); elapsed != tc.wantDelay {
					t.Fatalf("completion took %s, want %s", elapsed, tc.wantDelay)
				}
			})
		})
	}
}
