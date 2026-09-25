package spec

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestParseQuestions(t *testing.T) {
	for _, raw := range []string{
		``, `null`, `[]`, `{}`, `{"questions":null}`, `{"questions":[]}`,
		`{"questions":[null]}`, `{"questions":[{"question":"?","options":[]}]}`,
		`{"questions":[{"question":"?","options":[{"label":"A"}]}]}`,
		`{"questions":[{"question":"?","options":[{"description":"A"}]}]}`,
		`{"questions":[{"question":"?","options":[{"label":"A","description":null}]}]}`,
		`{"questions":[{"question":"?","options":[{"label":"A","description":5}]}]}`,
		`{"questions":[{"question":" ","options":[{"label":"A","description":""}]}]}`,
		`{"questions":[{"question":"?","options":[{"label":" ","description":""}]}]}`,
		`{"questions":[{"question":"?","options":[null]}]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseQuestions(raw); err == nil {
				t.Fatalf("accepted %q", raw)
			}
		})
	}
	for _, questions := range []int{1, 5, 6} {
		for _, options := range []int{1, 5, 6} {
			t.Run(fmt.Sprintf("%d_questions_%d_options", questions, options), func(t *testing.T) {
				in := AskUserQuestionInput{Questions: make([]Question, questions)}
				for i := range in.Questions {
					in.Questions[i] = Question{Question: "Which?", Options: make([]QuestionOption, options)}
					for j := range in.Questions[i].Options {
						in.Questions[i].Options[j].Label = "A"
					}
				}
				raw, err := json.Marshal(in)
				if err != nil {
					t.Fatal(err)
				}
				_, err = ParseQuestions(string(raw))
				if (err == nil) != (questions <= 5 && options <= 5) {
					t.Fatalf("bounds validation: %v", err)
				}
			})
		}
	}
}

func TestFormatQuestionAnswers(t *testing.T) {
	in := AskUserQuestionInput{Questions: []Question{{Question: "One?"}, {Question: "Two?"}}}
	for _, answers := range [][]string{nil, {"A"}, {"A", ""}, {"A", " "}, {"A", "B", "C"}} {
		if _, err := in.FormatAnswers(QuestionAnswers{Answers: answers}); err == nil {
			t.Fatalf("accepted incomplete answers %q", answers)
		}
	}
	out, err := in.FormatAnswers(QuestionAnswers{Answers: []string{"A", "custom answer"}})
	if err != nil || !out.Success || out.Message != "Q: One?\nA: A\n\nQ: Two?\nA: custom answer" {
		t.Fatalf("result=%+v err=%v", out, err)
	}
	out, err = in.FormatAnswers(QuestionAnswers{Dismissed: true, Answers: []string{"partial"}})
	if err != nil || out.Success || !strings.Contains(out.Message, "without answering") || strings.Contains(out.Message, "partial") {
		t.Fatalf("dismissal=%+v err=%v", out, err)
	}
}
