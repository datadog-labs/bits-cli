package chat

import (
	"encoding/json"
	"strings"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tools/spec"
	"github.com/DataDog/bits-cli/internal/tui/escape"
)

func renderQuestionsTool(tool *agent.ToolBlock, _ toolPresentation, width int, sty Styles, frame int) string {
	state := lifecycleOf(tool)
	label := "questions"
	if tool.Status == agent.ToolAwaitingInput {
		label = "waiting for your answers"
	}
	if tool.Cancelled {
		label = "questions cancelled"
	}
	header := renderActivityHeader(state, label, "", width, sty, frame)
	input, err := spec.ParseQuestions(tool.Input)
	if err != nil {
		return header + "\n" + sty.ToolError.Render(wrap(escape.Multiline(tool.Output), width))
	}
	var result spec.AskUserQuestionOutput
	if json.Unmarshal([]byte(tool.Output), &result) == nil && result.Success {
		return header + "\n" + sty.ToolDetail.Render(wrap(escape.Multiline(result.Message), width))
	}
	lines := []string{header}
	for _, question := range input.Questions {
		lines = append(lines, sty.ToolDetail.Render(wrap(escape.Multiline("Q: "+question.Question), width)))
	}
	if result.Message != "" {
		lines = append(lines, sty.ToolDetail.Render(wrap(escape.Multiline(result.Message), width)))
	} else if tool.Output != "" {
		lines = append(lines, sty.ToolDetail.Render(wrap(escape.Multiline(tool.Output), width)))
	}
	return strings.Join(lines, "\n")
}
