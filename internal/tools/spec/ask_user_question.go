package spec

import (
	"encoding/json"
	"fmt"
	"strings"
)

const AskUserQuestion = "ask_user_question"

var ClientAskUserQuestion = Identity{ClientSide: true, Name: AskUserQuestion}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type Question struct {
	Question string           `json:"question"`
	Options  []QuestionOption `json:"options"`
}

type AskUserQuestionInput struct {
	Questions []Question `json:"questions"`
}

// QuestionAnswers uses nil answers only for dismissal. A submitted form must
// contain a nonempty answer for every question, in request order.
type QuestionAnswers struct {
	Answers   []string
	Dismissed bool
}

type AskUserQuestionOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// ParseQuestions validates required fields as well as the Web's array bounds.
func ParseQuestions(raw string) (AskUserQuestionInput, error) {
	var input AskUserQuestionInput
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return input, fmt.Errorf("decode questions: %w", err)
	}
	if len(input.Questions) < 1 || len(input.Questions) > 5 {
		return input, fmt.Errorf("questions must contain 1–5 entries")
	}
	// Pointers distinguish missing/null strings from valid empty descriptions.
	var required struct {
		Questions []struct {
			Options []struct {
				Description *string
			}
		}
	}
	if err := json.Unmarshal([]byte(raw), &required); err != nil {
		return input, err
	}
	for i, question := range input.Questions {
		if strings.TrimSpace(question.Question) == "" {
			return input, fmt.Errorf("question %d must have nonempty question text", i+1)
		}
		if len(question.Options) < 1 || len(question.Options) > 5 {
			return input, fmt.Errorf("question %d must contain 1–5 options", i+1)
		}
		for j, option := range question.Options {
			if strings.TrimSpace(option.Label) == "" || required.Questions[i].Options[j].Description == nil {
				return input, fmt.Errorf("question %d option %d requires a nonempty label and a description", i+1, j+1)
			}
		}
	}
	return input, nil
}

func (in AskUserQuestionInput) FormatAnswers(answers QuestionAnswers) (AskUserQuestionOutput, error) {
	if answers.Dismissed {
		return AskUserQuestionOutput{Message: "User dismissed the question without answering. Proceed with your best guess or ask a follow-up."}, nil
	}
	if len(answers.Answers) != len(in.Questions) {
		return AskUserQuestionOutput{}, fmt.Errorf("every question requires an answer")
	}
	pairs := make([]string, len(in.Questions))
	for i, q := range in.Questions {
		if strings.TrimSpace(answers.Answers[i]) == "" {
			return AskUserQuestionOutput{}, fmt.Errorf("question %d is unanswered", i+1)
		}
		pairs[i] = "Q: " + q.Question + "\nA: " + answers.Answers[i]
	}
	return AskUserQuestionOutput{Success: true, Message: strings.Join(pairs, "\n\n")}, nil
}
