package fake

import (
	"encoding/json"
	"fmt"

	"github.com/DataDog/bits-cli/internal/assistant"
	questionspec "github.com/DataDog/bits-cli/internal/tools/spec"
)

// questionDemoSource keeps the short interactive demo prompt while running
// through the scripted backend's normal client-tool and continuation path.
func questionDemoSource(opts assistant.SendOptions) string {
	available := false
	for _, tool := range opts.ClientTools {
		available = available || tool.Name == questionspec.AskUserQuestion
	}
	if !available {
		return fmt.Sprintf("say(%q)", "The question demo requires an interactive session. Run bits without the run subcommand.")
	}
	input := questionspec.AskUserQuestionInput{Questions: []questionspec.Question{
		{Question: "Which region should we investigate?", Options: []questionspec.QuestionOption{
			{Label: "US (Recommended)", Description: "Start with the US production environment."},
			{Label: "EU", Description: "Investigate the EU production environment."},
		}},
		{Question: "Which service should we focus on?", Options: []questionspec.QuestionOption{
			{Label: "API", Description: "Inspect API latency and error rates."},
			{Label: "Worker", Description: "Inspect background jobs and queue delays."},
		}},
	}}
	arguments, _ := json.Marshal(input)
	return fmt.Sprintf("r = call(%q, %s)\nif r.ok:\n    say(%q + r.output)\nelse:\n    say(%q + r.output)",
		questionspec.AskUserQuestion, arguments,
		"Demo continued after receiving your answers.\n\n",
		"Demo continued without answers.\n\n")
}
