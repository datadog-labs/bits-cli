package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools/spec"
)

func TestQuestionTranscriptRestoresAnswersWithoutLocalState(t *testing.T) {
	output, err := json.Marshal(spec.AskUserQuestionOutput{Success: true, Message: "Q: One?\nA: A\n\nQ: Two?\nA: Custom\n\nQ: Three?\nA: Last answer"})
	if err != nil {
		t.Fatal(err)
	}
	block := agent.Block{Kind: assistant.KindToolResult, Complete: true, Tool: &agent.ToolBlock{
		Name: spec.AskUserQuestion, IsClientSide: true, Status: agent.ToolSuccess,
		Input:  `{"questions":[{"question":"One?","options":[{"label":"A","description":""}]}]}`,
		Output: string(output),
	}}
	view := ansi.Strip(RenderBlock(block, 80, DefaultStyles(true), 0))
	for _, want := range []string{"One?", "Two?", "Three?", "Custom", "Last answer"} {
		if !strings.Contains(view, want) {
			t.Fatalf("restored transcript missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "awaiting approval") {
		t.Fatal("question displayed as permission approval")
	}
}
