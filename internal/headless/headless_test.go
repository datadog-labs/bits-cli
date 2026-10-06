package headless

import (
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
)

func TestClassifyTurn(t *testing.T) {
	cases := []struct {
		result agent.TurnResult
		want   Outcome
	}{
		{result: agent.TurnResult{Outcome: agent.TurnOutcomeCompleted}, want: OutcomeCompleted},
		{result: agent.TurnResult{Outcome: agent.TurnOutcomeCompleted, Denied: true}, want: OutcomeApprovalDenied},
		{result: agent.TurnResult{Outcome: agent.TurnOutcomeFailed, Denied: true}, want: OutcomeFailed},
		{result: agent.TurnResult{Outcome: agent.TurnOutcomeCanceled}, want: OutcomeCanceled},
		{result: agent.TurnResult{Outcome: agent.TurnOutcomeDeadlineExceeded}, want: OutcomeTimedOut},
	}
	for _, test := range cases {
		if got := ClassifyTurn(test.result); got != test.want {
			t.Fatalf("ClassifyTurn(%+v) = %q, want %q", test.result, got, test.want)
		}
	}
}
