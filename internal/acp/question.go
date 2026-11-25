package acp

import (
	"cmp"
	"fmt"
	"strconv"

	"github.com/coder/acp-go-sdk"

	"github.com/datadog-labs/bits-cli/internal/tools"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
)

// question answers an ask_user_question call with a form: one single-select
// field per question, plus an optional free-text field standing for the
// TUI's "Other" choice.
func (s *session) question(request *tools.Request) {
	if request.Call.Name != spec.AskUserQuestion {
		request.Respond(nil, fmt.Errorf("no form for tool %q", request.Call.Name))
		return
	}
	input, err := spec.ParseQuestions(request.Call.Input)
	if err != nil {
		request.Respond(nil, err)
		return
	}
	call := acp.ToolCallId(request.Call.ID)
	response, err := s.client.CreateElicitation(request.Context(), acp.CreateElicitationRequest{Form: &acp.CreateElicitationForm{
		Mode:            "form",
		Message:         "The assistant has questions for you.",
		RequestedSchema: questionSchema(input),
		Session:         &acp.ElicitationSessionScope{SessionId: s.id, ToolCallId: &call},
	}})
	if err != nil {
		request.Respond(nil, err)
		return
	}
	request.Respond(questionAnswers(input, response), nil)
}

func choiceField(i int) string { return "q" + strconv.Itoa(i) }
func otherField(i int) string  { return "q" + strconv.Itoa(i) + "_other" }

func questionSchema(input spec.AskUserQuestionInput) acp.ElicitationSchema {
	schema := acp.ElicitationSchema{Type: "object", Properties: map[string]acp.ElicitationPropertySchema{}}
	for i, q := range input.Questions {
		options := make([]acp.EnumOption, 0, len(q.Options))
		for _, o := range q.Options {
			options = append(options, acp.EnumOption{Const: o.Label, Title: o.Label, Description: acp.Ptr(o.Description)})
		}
		schema.Properties[choiceField(i)] = acp.ElicitationPropertySchema{String: &acp.ElicitationPropertySchemaString{Type: "string", Title: acp.Ptr(q.Question), OneOf: options}}
		schema.Properties[otherField(i)] = acp.ElicitationPropertySchema{String: &acp.ElicitationPropertySchemaString{Type: "string", Title: acp.Ptr("Other answer (optional)")}}
	}
	return schema
}

// questionAnswers reads an accepted form back. A declined or cancelled form,
// or one missing an answer, dismisses the questions, as the TUI's Esc does.
func questionAnswers(input spec.AskUserQuestionInput, response acp.CreateElicitationResponse) spec.QuestionAnswers {
	if response.Accept == nil {
		return spec.QuestionAnswers{Dismissed: true}
	}
	value := func(field string) string {
		if v, ok := response.Accept.Content[field]; ok && v.String != nil {
			return string(*v.String)
		}
		return ""
	}
	answers := make([]string, len(input.Questions))
	for i := range input.Questions {
		// A filled "Other" wins over the choice, as in the TUI.
		answers[i] = cmp.Or(value(otherField(i)), value(choiceField(i)))
		if answers[i] == "" {
			return spec.QuestionAnswers{Dismissed: true}
		}
	}
	return spec.QuestionAnswers{Answers: answers}
}
