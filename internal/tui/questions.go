package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tools/spec"
)

type questionForm struct {
	request *agent.InputRequest
	input   spec.AskUserQuestionInput
	answers []string
	choices []int
	custom  []string
	page    int // len(questions) is the review page
	editing bool
	text    textinput.Model
	scroll  int
	follow  bool
}

func newQuestionForm(request *agent.InputRequest, input spec.AskUserQuestionInput) *questionForm {
	text := textinput.New()
	text.Prompt = ""
	text.SetVirtualCursor(true)
	text.Placeholder = "Type your answer"
	text.CharLimit = 0
	return &questionForm{
		request: request, input: input, text: text,
		answers: make([]string, len(input.Questions)),
		choices: make([]int, len(input.Questions)),
		custom:  make([]string, len(input.Questions)), follow: true,
	}
}

func (m *Model) syncQuestions() {
	if m.cancelRequested {
		m.questions = nil
		return
	}
	// Keep the current form when concurrent tools publish another request.
	if m.questions != nil && m.questions.request.Pending() {
		return
	}
	for _, block := range m.transcript.Blocks {
		if block.Tool == nil || block.Tool.InputRequest == nil || !block.Tool.InputRequest.Pending() {
			continue
		}
		request := block.Tool.InputRequest
		input, ok := request.Value.(spec.AskUserQuestionInput)
		if !ok {
			continue
		}
		if m.questions == nil || m.questions.request != request {
			m.questions = newQuestionForm(request, input)
			m.clearSelection()
			m.editor.CloseMenu()
		}
		return
	}
	m.questions = nil
}

func (m *Model) answerQuestions(dismiss bool) {
	q := m.questions
	if q == nil {
		return
	}
	answers := spec.QuestionAnswers{Dismissed: dismiss}
	if !dismiss {
		answers.Answers = append([]string(nil), q.answers...)
		if _, err := q.input.FormatAnswers(answers); err != nil {
			for i, answer := range q.answers {
				if strings.TrimSpace(answer) == "" {
					_ = q.showPage(i)
					return
				}
			}
		}
	}
	q.request.Respond(answers)
	m.syncQuestions()
	m.layoutTranscript()
}

// saveDraft keeps custom text separate from explicitly confirmed answers.
func (q *questionForm) saveDraft() {
	if q.editing {
		q.custom[q.page] = q.text.Value()
	}
}

func (q *questionForm) selectChoice(choice int) tea.Cmd {
	q.saveDraft()
	q.choices[q.page] = choice
	q.follow = true
	q.editing = choice == len(q.input.Questions[q.page].Options)
	if q.editing {
		q.text.SetValue(q.custom[q.page])
		return q.text.Focus()
	}
	q.text.Blur()
	return nil
}

func (q *questionForm) showPage(page int) tea.Cmd {
	q.saveDraft()
	q.editing = false
	q.text.Blur()
	q.page = (page + len(q.answers) + 1) % (len(q.answers) + 1)
	q.scroll, q.follow = 0, true
	if q.page < len(q.answers) {
		return q.selectChoice(q.choices[q.page])
	}
	return nil
}

func (m *Model) updateQuestions(msg tea.Msg) tea.Cmd {
	q := m.questions
	if q == nil || !q.request.Pending() {
		return nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			m.answerQuestions(true)
			return nil
		case "ctrl+x":
			m.cancelRemote()
			return nil
		case "tab", "ctrl+right":
			return q.showPage(q.page + 1)
		case "shift+tab", "ctrl+left":
			return q.showPage(q.page - 1)
		case "pgup", "pgdown":
			delta := m.questionBodyHeight()
			if key.String() == "pgup" {
				delta = -delta
			}
			q.scroll = max(0, q.scroll+delta)
			q.follow = false
			return nil
		case "enter":
			if q.page == len(q.answers) {
				m.answerQuestions(false)
				return nil
			}
			question := q.input.Questions[q.page]
			if q.editing {
				if strings.TrimSpace(q.text.Value()) == "" {
					return nil
				}
				q.answers[q.page] = q.text.Value()
			} else {
				q.answers[q.page] = question.Options[q.choices[q.page]].Label
			}
			return q.showPage(q.page + 1)
		case "up", "down":
			if q.page < len(q.answers) {
				delta := 1
				if key.String() == "up" {
					delta = -1
				}
				count := len(q.input.Questions[q.page].Options) + 1
				return q.selectChoice((q.choices[q.page] + delta + count) % count)
			}
		}
		// Text entry owns printable keys, including digits and brackets.
		if !q.editing {
			switch key.String() {
			case "[":
				return q.showPage(q.page - 1)
			case "]":
				return q.showPage(q.page + 1)
			}
			if q.page < len(q.answers) {
				count := len(q.input.Questions[q.page].Options) + 1
				switch key.String() {
				case "j":
					return q.selectChoice((q.choices[q.page] + 1) % count)
				case "k":
					return q.selectChoice((q.choices[q.page] + count - 1) % count)
				}
				if len(key.String()) == 1 && key.Code >= '1' && key.Code < '1'+rune(count) {
					return q.selectChoice(int(key.Code - '1'))
				}
			}
		}
	}
	if q.editing {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.PasteMsg:
			q.follow = true
		}
		var cmd tea.Cmd
		q.text, cmd = q.text.Update(msg)
		return cmd
	}
	return nil
}
