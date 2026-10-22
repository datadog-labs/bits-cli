package headless

import (
	"context"
	"errors"
	"testing"
)

func TestClassify(t *testing.T) {
	turnErr, consumeErr := errors.New("backend failed"), errors.New("writer failed")
	for _, tt := range []struct {
		name                string
		completed, denied   bool
		turnErr, consumeErr error
		want                Outcome
		wantErr             error
	}{
		{name: "completed", completed: true, want: OutcomeCompleted},
		{name: "a denial refines a completion", completed: true, denied: true, want: OutcomeApprovalDenied},
		{name: "a failure wins over a denial", denied: true, turnErr: turnErr, want: OutcomeFailed, wantErr: turnErr},
		{name: "a consumer failure wins", completed: true, consumeErr: consumeErr, want: OutcomeFailed, wantErr: consumeErr},
		{name: "canceled", turnErr: context.Canceled, want: OutcomeCanceled, wantErr: context.Canceled},
		{name: "timed out", turnErr: context.DeadlineExceeded, want: OutcomeTimedOut, wantErr: context.DeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := classify(tt.completed, tt.denied, tt.turnErr, tt.consumeErr)
			if got != tt.want || !errors.Is(err, tt.wantErr) || (tt.wantErr == nil) != (err == nil) {
				t.Fatalf("classify = %q, %v; want %q, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
