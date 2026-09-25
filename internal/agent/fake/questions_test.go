package fake_test

import (
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/agent/fake"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tools/spec"
)

func TestQuestionDemo(t *testing.T) {
	for _, dismiss := range []bool{false, true} {
		t.Run(map[bool]string{false: "answer", true: "dismiss"}[dismiss], func(t *testing.T) {
			set, err := agent.NewToolSet(agent.ModeSkipPermissions, tools.NewAskUserQuestionTool())
			if err != nil {
				t.Fatal(err)
			}
			engine := agent.New(&fake.Fake{}, assistant.SendOptions{})
			requests := 0
			result, err := engine.RunTurn(t.Context(), agent.TurnInput{
				Message: "test ask_user_question", Tools: set, Interactive: true,
			}, func(event agent.Event) error {
				for _, block := range event.Transcript.Blocks {
					if block.Tool == nil || block.Tool.InputRequest == nil || !block.Tool.InputRequest.Pending() {
						continue
					}
					requests++
					block.Tool.InputRequest.Respond(spec.QuestionAnswers{Answers: []string{"EU", "custom service"}, Dismissed: dismiss})
				}
				return nil
			})
			if err != nil || result.Outcome != agent.TurnOutcomeCompleted || requests != 1 {
				t.Fatalf("outcome=%s requests=%d err=%v", result.Outcome, requests, err)
			}
			last := result.Blocks[len(result.Blocks)-1]
			want := "custom service"
			if dismiss {
				want = "without answers"
			}
			if last.Markdown == nil || !strings.Contains(last.Markdown.Content, want) {
				t.Fatalf("missing demo continuation %q: %+v", want, last)
			}
		})
	}
}

func TestQuestionDemoWithoutInteractiveTool(t *testing.T) {
	engine := agent.New(&fake.Fake{}, assistant.SendOptions{})
	result, err := engine.RunTurn(t.Context(), agent.TurnInput{Message: "test ask_user_question"}, nil)
	if err != nil || result.Outcome != agent.TurnOutcomeCompleted {
		t.Fatalf("outcome=%s err=%v", result.Outcome, err)
	}
	for _, block := range result.Blocks {
		if block.Tool != nil {
			t.Fatal("demo emitted an unavailable interactive tool")
		}
	}
}
