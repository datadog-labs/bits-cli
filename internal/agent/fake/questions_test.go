package fake_test

import (
	"context"
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
				Message: "load(\"testdata/questions.star\", \"demo\")\ndemo()", Tools: set, Interactive: true,
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

func TestQuestionScriptResumesAfterClientExit(t *testing.T) {
	backend := &fake.Fake{}
	set, err := agent.NewToolSet(agent.ModeManual, tools.NewAskUserQuestionTool())
	if err != nil {
		t.Fatal(err)
	}
	original := agent.New(backend, assistant.SendOptions{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	prompted := false
	for event := range original.StartTurn(ctx, agent.TurnInput{Message: "load(\"testdata/questions.star\", \"demo\")\ndemo()", Tools: set, Interactive: true}) {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		for _, block := range event.Transcript.Blocks {
			if block.Tool != nil && block.Tool.InputRequest != nil && block.Tool.InputRequest.Pending() {
				prompted = true
				cancel()
			}
		}
	}
	if !prompted {
		t.Fatal("script never requested input")
	}
	resumed := agent.New(backend, assistant.SendOptions{ConversationID: original.ConversationID()})
	for event := range resumed.Restore(t.Context()) {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
	if !resumed.CanResumeTools(set) {
		t.Fatal("saved script call was not resumable")
	}
	completed, answers := false, 0
	for event := range resumed.ResumePendingTools(t.Context(), agent.TurnInput{Tools: set, Interactive: true}) {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		completed = completed || event.Kind == agent.EventTurnDone
		for _, block := range event.Transcript.Blocks {
			if block.Tool != nil && block.Tool.InputRequest != nil && block.Tool.InputRequest.Pending() {
				if block.Tool.InputRequest.Respond(spec.QuestionAnswers{Answers: []string{"EU", "Worker"}}) {
					answers++
				}
			}
		}
	}
	if !completed || answers != 1 {
		t.Fatalf("completed=%v answers=%d", completed, answers)
	}
	blocks := resumed.Snapshot()
	last := blocks[len(blocks)-1]
	if last.Markdown == nil || !strings.Contains(last.Markdown.Content, "Worker") {
		t.Fatalf("script failed to continue: %+v", last)
	}
}
