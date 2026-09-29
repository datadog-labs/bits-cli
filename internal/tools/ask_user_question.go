package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools/spec"
)

// NewAskUserQuestionTool is registered only by the interactive CLI. It has no
// approval policy: permission grants cannot answer a question for the user.
func NewAskUserQuestionTool(ui *UI) agent.Tool {
	return agent.Tool{
		Resumable: true,
		Definition: assistant.ClientTool{
			Name:        spec.AskUserQuestion,
			Description: `Ask the user one or more questions with distinct, concrete choices when clarification would improve your answer or the user should choose an approach. Use this tool instead of listing choices in prose. Do not use it for trivial questions, yes/no permission requests, or open-ended questions without meaningful options. Group all questions into one call. The UI adds a free-text Other option; do not include your own. Put a recommended option first and append "(Recommended)" to its label. Keep questions concise and options distinct.`,
			InputSchema: map[string]any{
				"type": "object", "required": []string{"questions"},
				"properties": map[string]any{
					"questions": map[string]any{
						"type": "array", "minItems": 1, "maxItems": 5,
						"items": map[string]any{
							"type": "object", "required": []string{"question", "options"},
							"properties": map[string]any{
								"question": map[string]any{"type": "string"},
								"options": map[string]any{
									"type": "array", "minItems": 1, "maxItems": 5,
									"items": map[string]any{
										"type": "object", "required": []string{"label", "description"},
										"properties": map[string]any{
											"label":       map[string]any{"type": "string"},
											"description": map[string]any{"type": "string"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		Handler: func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
			return askUserQuestion(ctx, call, ui)
		},
	}
}

func askUserQuestion(ctx context.Context, call agent.ToolCall, ui *UI) (agent.ToolResult, error) {
	input, err := spec.ParseQuestions(call.Input)
	if err != nil {
		return errorResult("invalid ask_user_question arguments: %v", err), nil
	}
	answers, err := Interact[spec.QuestionAnswers](ctx, ui, call)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return agent.ToolResult{Title: "Cancelled", Output: "User input was cancelled without answering.", IsError: true, Cancelled: true}, nil
		}
		return errorResult("ask_user_question: %v", err), nil
	}
	result, err := input.FormatAnswers(answers)
	if err != nil {
		return agent.ToolResult{Title: "Questions", Output: "invalid question answers: " + err.Error(), IsError: true}, nil
	}
	output, err := json.Marshal(result)
	if err != nil {
		return agent.ToolResult{Title: "Questions", Output: err.Error(), IsError: true}, nil
	}
	return agent.ToolResult{Title: "Questions", Output: string(output), Display: result.Message, IsError: !result.Success}, nil
}
