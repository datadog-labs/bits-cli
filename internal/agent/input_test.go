package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/assistant"
)

func TestRequestInputUnsupported(t *testing.T) {
	if _, err := RequestInput(t.Context(), "question"); !errors.Is(err, ErrInputUnsupported) {
		t.Fatalf("error=%v", err)
	}
}

func TestRequestInputRespondsOnce(t *testing.T) {
	requests := make(chan *InputRequest, 1)
	ctx := context.WithValue(t.Context(), inputContextKey{}, inputSender(func(r *InputRequest) error { requests <- r; return nil }))
	done := make(chan any, 1)
	go func() {
		answer, err := RequestInput(ctx, "question")
		if err != nil {
			t.Error(err)
		}
		done <- answer
	}()
	r := <-requests
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if r.Respond("answer") {
				accepted.Add(1)
			}
		})
	}
	wg.Wait()
	if answer := <-done; answer != "answer" || accepted.Load() != 1 || r.Pending() {
		t.Fatalf("answer=%v accepted=%d pending=%t", answer, accepted.Load(), r.Pending())
	}
}

func TestRequestInputCancellation(t *testing.T) {
	requests := make(chan *InputRequest, 1)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), inputContextKey{}, inputSender(func(r *InputRequest) error { requests <- r; return nil })))
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := RequestInput(ctx, "question"); done <- err }()
	r := <-requests
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if r.Pending() || r.Respond("late answer") {
		t.Fatal("cancelled request accepted an answer")
	}
}

func TestDenialCancelsPendingInput(t *testing.T) {
	for _, waitForInput := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_input", true: "after_input"}[waitForInput], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			b := &batchToolBackend{t: t, toolNames: []string{"question", "write"}}
			e := New(b, assistant.SendOptions{})
			startInput := make(chan struct{})
			set, err := NewToolSet(ModeManual,
				Tool{Definition: assistant.ClientTool{Name: "question"}, Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
					select {
					case <-startInput:
					case <-ctx.Done():
						return cancelledResult(), nil
					}
					_, err := RequestInput(ctx, "question")
					if !errors.Is(err, context.Canceled) {
						t.Errorf("input error=%v", err)
					}
					return cancelledResult(), nil
				}},
				Tool{
					Definition: assistant.ClientTool{Name: "write"}, Handler: func(context.Context, ToolCall) (ToolResult, error) {
						t.Error("denied write ran")
						return ToolResult{}, nil
					},
					Approval: func(ToolCall) (ApprovalRequirement, bool) {
						return ApprovalRequirement{Key: ApprovalKey{Tool: "write"}}, true
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if waitForInput {
				close(startInput)
			}
			events := e.StartTurn(ctx, TurnInput{Message: "ask and write", Tools: set, Interactive: true, OnDeny: DenyStop})
			denied := false
			for ev := range events {
				if denied || ev.Kind != EventTranscript {
					continue
				}
				for _, block := range ev.Transcript.Blocks {
					if block.Tool == nil {
						continue
					}
					ready := block.Tool.Status == ToolAwaitingApproval
					if waitForInput {
						ready = block.Tool.Status == ToolAwaitingInput
					}
					if ready {
						e.Decide("call-write", ApprovalDeny)
						denied = true
						if !waitForInput {
							close(startInput)
						}
						break
					}
				}
			}
			if ctx.Err() != nil || b.calls != 2 || len(b.responses) != 2 {
				t.Fatalf("denial blocked: err=%v calls=%d responses=%+v", ctx.Err(), b.calls, b.responses)
			}
		})
	}
}
