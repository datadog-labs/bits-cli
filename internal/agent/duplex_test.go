package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
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
		terminal := EventTurnDone
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
			events := drain(engine.StartTurn(context.Background(), TurnInput{Message: "question"}))
			want := []EventKind{EventTranscript, EventTranscript, EventTranscript, terminal}
			if got := kinds(events); !reflect.DeepEqual(got, want) {
				t.Fatalf("event kinds = %v, want %v", got, want)
			}
			streaming := events[1].Transcript.Blocks
			if len(streaming) != 2 || streaming[1].Complete {
				t.Fatalf("streaming blocks = %+v", streaming)
			}
			final := events[2].Transcript.Blocks
			if len(final) != 2 || !final[1].Complete || events[2].Origin != TranscriptOriginRemote {
				t.Fatalf("final transcript = %+v", events[2])
			}
			if !errors.Is(events[3].Err, failure) {
				t.Fatalf("terminal error = %v, want %v", events[3].Err, failure)
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
	tools, err := NewToolSet(ModeSkipPermissions, Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(ctx context.Context, call ToolCall) (ToolResult, error) {
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
	op := New(backend, Config{}).beginTurn(ctx, TurnInput{Message: "read both", Tools: tools})
	drain(op.events)
	completion := <-op.completion
	if !completion.Completed || completion.Err != nil {
		t.Fatalf("completion = %+v", completion)
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
	tools, err := NewToolSet(ModeSkipPermissions, Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		return ToolResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	op := New(backend, Config{}).beginTurn(ctx, TurnInput{Message: "read", Tools: tools})
	events := drain(op.events)
	completion := <-op.completion
	if !errors.Is(completion.Err, failure) {
		t.Fatalf("completion = %+v", completion)
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
			tools, err := NewToolSet(ModeManual,
				Tool{Definition: assistant.ClientTool{Name: "write"}, Handler: handler, Approval: func(ToolCall) (ApprovalRequirement, bool) {
					return ApprovalRequirement{Key: ApprovalKey{Tool: "write"}}, true
				}},
				Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: handler},
			)
			if err != nil {
				t.Fatal(err)
			}
			engine := New(backend, Config{})
			op := engine.beginTurn(ctx, TurnInput{Message: "write", Tools: tools, OnDeny: policy})
			decided := false
			for event := range op.events {
				if !decided && len(event.Transcript.PendingApprovals()) > 0 {
					decided = engine.Decide("gated", ApprovalDeny)
				}
			}
			completion := <-op.completion
			if !decided || !completion.Completed || !completion.Denied || completion.Err != nil {
				t.Fatalf("completion = %+v; decided = %v", completion, decided)
			}
			wantRuns := int32(0)
			if policy == DenyContinue {
				wantRuns = 1
			}
			if got := executions.Load(); got != wantRuns {
				t.Fatalf("executions = %d, want %d", got, wantRuns)
			}
			for _, block := range completion.Blocks {
				if policy == DenyStop && block.MessageID == "answer" {
					t.Fatal("stopped run exposed follow-up answer")
				}
			}
		})
	}
}

func TestDuplexSubmissionFailureCancelsStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cancelled := make(chan struct{})
	failure := errors.New("result POST failed")
	backend := &duplexBackend{stream: func(ctx context.Context, emit func(assistant.AssistantResponse) error) error {
		if err := emit(duplexCall("read", "read")); err != nil {
			return err
		}
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}, submit: func(context.Context, []assistant.ClientToolResponse) error { return failure }}
	tools, err := NewToolSet(ModeSkipPermissions, Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	op := New(backend, Config{}).beginTurn(ctx, TurnInput{Message: "read", Tools: tools})
	events := drain(op.events)
	completion := <-op.completion
	if !errors.Is(completion.Err, failure) || completion.Completed {
		t.Fatalf("completion = %+v", completion)
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("stream was not cancelled")
	}
	if len(events) == 0 || !events[len(events)-1].BackendFailure {
		t.Fatalf("events = %+v", events)
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
	tools, err := NewToolSet(ModeSkipPermissions, Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
		count.Add(1)
		started <- struct{}{}
		<-ctx.Done()
		return ToolResult{}, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	op := New(backend, Config{}).beginTurn(ctx, TurnInput{Message: "read many", Tools: tools})
	drain(op.events)
	completion := <-op.completion
	if !completion.Completed || completion.Err != nil {
		t.Fatalf("completion = %+v", completion)
	}
	if got := count.Load(); got > 16 {
		t.Fatalf("started %d blocking handlers; expected at most 16 across all batches", got)
	}
	for _, block := range completion.Blocks {
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
	tools, err := NewToolSet(ModeSkipPermissions, Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, failure }})
	if err != nil {
		t.Fatal(err)
	}
	op := New(backend, Config{}).beginTurn(ctx, TurnInput{Message: "read", Tools: tools})
	finished := make(chan struct{})
	go func() { drain(op.events); close(finished) }()
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
	completion := <-op.completion
	if !errors.Is(completion.Err, failure) {
		t.Fatalf("completion = %+v", completion)
	}
}
