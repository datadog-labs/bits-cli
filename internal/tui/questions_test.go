package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tools/spec"
)

const questionInput = `{"questions":[{"question":"Which region?","options":[{"label":"US","description":"US region"},{"label":"EU","description":"EU region"}]},{"question":"Which service?","options":[{"label":"API","description":"Public API"}]}]}`

type questionBackend struct {
	t           *testing.T
	input       string
	count       int
	calls       int
	responses   []assistant.ClientToolResponse
	definitions []assistant.ClientTool
}

func (b *questionBackend) Send(_ context.Context, message any, opts assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.calls++
	if b.calls == 1 {
		b.definitions = opts.ClientTools
		for i := range max(1, b.count) {
			content := assistant.ToolCallContent(fmt.Sprintf("question-%d", i), spec.AskUserQuestion, b.input)
			content.Type = assistant.ContentClientToolCall
			var response assistant.AssistantResponse
			response.Data.Attributes.StructuredMessage = assistant.AssistantMessage(fmt.Sprintf("message-%d", i), content)
			if err := emit(response); err != nil {
				return "conversation-1", err
			}
		}
		return "conversation-1", nil
	}
	var ok bool
	b.responses, ok = message.([]assistant.ClientToolResponse)
	if !ok {
		b.t.Errorf("follow-up has type %T", message)
	}
	var response assistant.AssistantResponse
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("Continuing with your answers."))
	return "conversation-1", emit(response)
}

func startQuestions(t *testing.T, mode agent.PermissionsMode, input string, count int) (*Model, *questionBackend) {
	t.Helper()
	backend := &questionBackend{t: t, input: input, count: count}
	set, err := agent.NewToolSet(mode, tools.NewAskUserQuestionTool())
	if err != nil {
		t.Fatal(err)
	}
	m := New(agent.New(backend, assistant.SendOptions{}), Config{Tools: set})
	m.resize(80, 24)
	setConversationInput(m, "Help me choose")
	_, _ = m.submit()
	t.Cleanup(func() {
		if m.turnEvents != nil {
			m.cancelRemote()
			drainConversationRemote(t, m)
		}
	})
	return m, backend
}

func waitQuestions(t *testing.T, m *Model) {
	t.Helper()
	for m.questions == nil && m.turnEvents != nil {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.turnGen, m.turnEvents)))
	}
	if m.questions == nil {
		t.Fatal("turn ended without a question form")
	}
}

func questionKey(m *Model, code rune, mod tea.KeyMod) {
	_, _ = m.Update(tea.KeyPressMsg{Code: code, Mod: mod})
}

func TestQuestionsAnswerReviewAndContinue(t *testing.T) {
	for _, mode := range []agent.PermissionsMode{agent.ModeManual, agent.ModeSkipPermissions} {
		t.Run(string(mode), func(t *testing.T) {
			m, backend := startQuestions(t, mode, questionInput, 1)
			waitQuestions(t, m)
			request := m.questions.request
			if m.editor.Focused() || len(m.pendingApprovals) != 0 {
				t.Fatal("question incorrectly routed through editor or permissions")
			}
			if backend.calls != 1 {
				t.Fatal("continued before an answer")
			}
			if len(backend.definitions) != 1 || backend.definitions[0].Name != spec.AskUserQuestion {
				t.Fatal("question tool not advertised")
			}
			questionKey(m, tea.KeyDown, 0)
			questionKey(m, tea.KeyEnter, 0) // EU
			questionKey(m, tea.KeyDown, 0)
			// Other focuses immediately, with no Enter needed before typing.
			_, _ = m.Update(tea.PasteMsg{Content: "billing-東京"})
			questionKey(m, tea.KeyEnter, 0)
			view := ansi.Strip(m.View().Content)
			for _, want := range []string{"Review answers", "Which region?", "EU", "Which service?", "billing-東京"} {
				if !strings.Contains(view, want) {
					t.Fatalf("review missing %q:\n%s", want, view)
				}
			}
			if backend.calls != 1 {
				t.Fatal("continued before review submission")
			}
			// Revisit and change the first answer before submitting.
			questionKey(m, tea.KeyTab, 0)
			questionKey(m, tea.KeyUp, 0)
			questionKey(m, tea.KeyEnter, 0) // US
			questionKey(m, tea.KeyTab, 0)   // review
			questionKey(m, tea.KeyEnter, 0)
			if request.Respond(spec.QuestionAnswers{Dismissed: true}) {
				t.Fatal("accepted a duplicate answer")
			}
			questionKey(m, tea.KeyEnter, 0) // repeated submit cannot resume twice
			drainConversationRemote(t, m)
			if backend.calls != 2 || len(backend.responses) != 1 {
				t.Fatalf("calls=%d responses=%v", backend.calls, backend.responses)
			}
			response := backend.responses[0]
			if response.ToolCallID != "question-0" || response.Status != assistant.ToolStatusSuccess {
				t.Fatalf("response=%+v", response)
			}
			var result spec.AskUserQuestionOutput
			if err := json.Unmarshal([]byte(response.Metadata.Output), &result); err != nil {
				t.Fatal(err)
			}
			want := "Q: Which region?\nA: US\n\nQ: Which service?\nA: billing-東京"
			if !result.Success || result.Message != want {
				t.Fatalf("result=%+v", result)
			}
			if m.questions != nil || !m.editor.Focused() {
				t.Fatal("form did not release input")
			}
			transcript := ansi.Strip(m.list.Document())
			for _, want := range []string{"Which region?", "A: US", "Which service?", "billing-東京", "Continuing with your answers."} {
				if !strings.Contains(transcript, want) {
					t.Fatalf("transcript missing %q:\n%s", want, transcript)
				}
			}
		})
	}
}

func TestQuestionsUnansweredDismissal(t *testing.T) {
	m, backend := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	questionKey(m, tea.KeyTab, tea.ModShift) // review with no answers
	if !strings.Contains(ansi.Strip(m.View().Content), "[Unanswered]") {
		t.Fatal("unanswered questions not identified")
	}
	questionKey(m, tea.KeyEnter, 0)
	if m.questions == nil || m.questions.page != 0 || !m.questions.request.Pending() {
		t.Fatal("review synthesized an answer")
	}
	questionKey(m, tea.KeyEnter, 0) // answer only the first question
	questionKey(m, tea.KeyEscape, 0)
	drainConversationRemote(t, m)
	if backend.calls != 2 || len(backend.responses) != 1 {
		t.Fatalf("dismissal did not continue exactly once: %+v", backend)
	}
	response := backend.responses[0]
	var result spec.AskUserQuestionOutput
	if err := json.Unmarshal([]byte(response.Metadata.Output), &result); err != nil {
		t.Fatal(err)
	}
	if response.Status != assistant.ToolStatusError || result.Success || !strings.Contains(result.Message, "without answering") || strings.Contains(result.Message, "A: US") {
		t.Fatalf("dismissal submitted partial answers: %+v", response)
	}
	for _, block := range m.transcript.Blocks {
		if block.Tool != nil && block.Tool.Denied {
			t.Fatal("dismissal is not a permission denial")
		}
	}
}

func TestQuestionsStopCancelsRequest(t *testing.T) {
	m, backend := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	request := m.questions.request
	questionKey(m, 'x', tea.ModCtrl)
	drainConversationRemote(t, m)
	if request.Pending() || request.Respond(spec.QuestionAnswers{Dismissed: true}) {
		t.Fatal("cancelled request still accepts input")
	}
	if backend.calls != 1 || len(backend.responses) != 0 || m.questions != nil {
		t.Fatal("stop continued the turn or left a form")
	}
	for _, block := range m.engine.Snapshot() {
		if block.Tool != nil && (!block.Tool.Cancelled || block.Tool.InputRequest != nil) {
			t.Fatalf("tool not cancelled: %+v", block.Tool)
		}
	}
	if err := m.engine.NewConversation(); err != nil {
		t.Fatalf("engine still blocked: %v", err)
	}
}

func TestQuestionsMultipleCallsAndToolCancellation(t *testing.T) {
	m, backend := startQuestions(t, agent.ModeSkipPermissions, questionInput, 2)
	waitQuestions(t, m)
	// Cancelling one call must not strand the other, regardless of worker order.
	m.engine.CancelTool("question-0")
	cancelled := false
	for !cancelled && m.turnEvents != nil {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.turnGen, m.turnEvents)))
		for _, block := range m.transcript.Blocks {
			if block.ToolCallID() == "question-0" && block.Tool.Cancelled {
				cancelled = true
			}
		}
	}
	if !cancelled {
		t.Fatal("per-tool cancellation was not applied")
	}
	for m.turnEvents != nil {
		if m.questions != nil {
			m.answerQuestions(true)
		}
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.turnGen, m.turnEvents)))
	}
	if backend.calls != 2 || len(backend.responses) != 2 {
		t.Fatalf("calls=%d responses=%+v", backend.calls, backend.responses)
	}
	for i, response := range backend.responses {
		if response.ToolCallID != fmt.Sprintf("question-%d", i) {
			t.Fatal("response order changed")
		}
	}
}

func TestQuestionsConcealedInputAndLayout(t *testing.T) {
	m, backend := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	m.resize(30, 10)
	questionKey(m, tea.KeyEnter, 0)
	questionKey(m, tea.KeyEscape, 0)
	if m.questions == nil || m.questions.page != 0 {
		t.Fatal("hidden form consumed a key")
	}
	for _, size := range [][2]int{{36, 14}, {80, 24}, {120, 40}} {
		m.resize(size[0], size[1])
		view := m.View().Content
		visible := strings.Split(ansi.Strip(view), "\n")
		for i, row := range visible {
			if strings.Contains(row, "esc dismiss") && (i == 0 || strings.TrimSpace(visible[i-1]) != "") {
				t.Fatalf("no gap above the hints at %v:\n%s", size, ansi.Strip(view))
			}
		}
		if strings.Count(view, "\n")+1 > size[1] {
			t.Fatalf("form too tall at %v", size)
		}
		for _, row := range strings.Split(view, "\n") {
			if ansi.StringWidth(row) > size[0] {
				t.Fatalf("row too wide at %v: %q", size, row)
			}
		}
	}
	if backend.calls != 1 {
		t.Fatal("resize submitted answers")
	}
}

func TestQuestionsMalformedArgumentsContinueWithoutForm(t *testing.T) {
	m, backend := startQuestions(t, agent.ModeSkipPermissions, `{"questions":[]}`, 1)
	drainConversationRemote(t, m)
	if m.questions != nil || backend.calls != 2 || len(backend.responses) != 1 || backend.responses[0].Status != assistant.ToolStatusError {
		t.Fatalf("malformed call did not return an error: %+v", backend)
	}
}

func TestQuestionsFiveQuestionsAndLongOptions(t *testing.T) {
	input := spec.AskUserQuestionInput{Questions: make([]spec.Question, 5)}
	for i := range input.Questions {
		input.Questions[i] = spec.Question{Question: fmt.Sprintf("Question %d?", i+1), Options: make([]spec.QuestionOption, 5)}
		for j := range input.Questions[i].Options {
			input.Questions[i].Options[j] = spec.QuestionOption{Label: fmt.Sprintf("Option %d", j+1), Description: strings.Repeat("A long explanation. ", 10)}
		}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	m, backend := startQuestions(t, agent.ModeSkipPermissions, string(raw), 1)
	waitQuestions(t, m)
	m.resize(36, 14)
	for i := range 5 {
		questionKey(m, tea.KeyUp, 0)    // Other, after all five options
		questionKey(m, tea.KeyEnter, 0) // blank Other must stay unanswered
		if !m.questions.editing || m.questions.page != i {
			t.Fatal("blank custom answer was submitted")
		}
		_, _ = m.Update(tea.PasteMsg{Content: fmt.Sprintf("Custom %d", i+1)})
		view := ansi.Strip(m.View().Content)
		if !strings.Contains(view, fmt.Sprintf("Custom %d", i+1)) || !strings.Contains(view, "esc dismiss") {
			t.Fatalf("custom input or controls hidden:\n%s", view)
		}
		questionKey(m, tea.KeyTab, 0) // save draft, not answer
		questionKey(m, tea.KeyTab, tea.ModShift)
		if !m.questions.editing || m.questions.text.Value() != fmt.Sprintf("Custom %d", i+1) {
			t.Fatal("custom draft lost on navigation")
		}
		questionKey(m, tea.KeyEnter, 0)
	}
	for range 5 {
		questionKey(m, tea.KeyPgDown, 0)
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Custom 5") {
		t.Fatalf("review cannot scroll to the last answer:\n%s", view)
	}
	questionKey(m, tea.KeyEnter, 0)
	drainConversationRemote(t, m)
	if backend.calls != 2 || len(backend.responses) != 1 {
		t.Fatalf("calls=%d responses=%+v", backend.calls, backend.responses)
	}
	if !strings.Contains(backend.responses[0].Metadata.Output, "Custom 5") {
		t.Fatal("last answer omitted from response")
	}
}

func TestQuestionsStayInlineAndRestoreComposer(t *testing.T) {
	m, _ := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"Help me choose", "waiting for your answers", "□ Which region?", "✓ Review", "› 1. US", "enter select · tab/arrow keys to navigate · esc dismiss"} {
		if !strings.Contains(view, want) {
			t.Fatalf("inline view missing %q:\n%s", want, view)
		}
	}
	if m.list.Height() < 3 || m.list.Height()+m.questionHeight()+chatNoticeHeight+chatFooterHeight != m.height {
		t.Fatal("picker did not reserve space for the conversation")
	}
	if strings.Contains(view, "Working on it…") {
		t.Fatal("composer was rendered behind the picker")
	}
	questionKey(m, tea.KeyEscape, 0)
	drainConversationRemote(t, m)
	if m.questions != nil || m.list.Height() != m.height-chatNoticeHeight-chatFooterHeight-m.editor.Height() {
		t.Fatal("dismissing the picker did not restore the transcript height")
	}
}

func TestQuestionsShortcutsKeepDraftsAndRequireConfirmation(t *testing.T) {
	m, backend := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	questionKey(m, '2', 0)
	if m.questions.choices[0] != 1 || m.questions.answers[0] != "" {
		t.Fatal("number shortcut must highlight without confirming an answer")
	}
	questionKey(m, '3', 0)
	if !m.questions.editing || !m.questions.text.Focused() {
		t.Fatal("Other did not focus immediately")
	}
	for _, key := range "12[]jk" {
		_, _ = m.Update(tea.KeyPressMsg{Code: key, Text: string(key)})
	}
	if m.questions.text.Value() != "12[]jk" || m.questions.page != 0 {
		t.Fatalf("shortcuts stole custom text: %q", m.questions.text.Value())
	}
	questionKey(m, tea.KeyTab, 0)
	questionKey(m, tea.KeyTab, tea.ModShift)
	if !m.questions.editing || m.questions.text.Value() != "12[]jk" || m.questions.answers[0] != "" {
		t.Fatal("page navigation lost or confirmed a draft")
	}
	questionKey(m, tea.KeyUp, 0) // leave Other
	questionKey(m, 'k', 0)       // US
	questionKey(m, 'j', 0)       // EU
	if m.questions.choices[0] != 1 || m.questions.editing {
		t.Fatal("choice navigation failed after leaving Other")
	}
	questionKey(m, tea.KeyDown, 0)
	if m.questions.text.Value() != "12[]jk" {
		t.Fatal("custom draft was lost when choosing another option")
	}
	questionKey(m, tea.KeyTab, 0)
	questionKey(m, ']', 0) // review, without confirming either answer
	questionKey(m, tea.KeyEnter, 0)
	if m.questions.page != 0 || backend.calls != 1 || m.questions.answers[0] != "" || m.questions.answers[1] != "" {
		t.Fatal("navigation implicitly submitted a choice or draft")
	}
}

func TestQuestionsMouseTabsChoicesAndReview(t *testing.T) {
	m, backend := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	clickText := func(text string) {
		t.Helper()
		for y, row := range strings.Split(ansi.Strip(m.View().Content), "\n") {
			if offset := strings.Index(row, text); offset >= 0 {
				x := ansi.StringWidth(row[:offset])
				_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
				return
			}
		}
		t.Fatalf("click target %q not visible", text)
	}
	clickText("2. EU")
	if m.questions.choices[0] != 1 || m.questions.answers[0] != "" {
		t.Fatal("click should highlight a choice without submitting")
	}
	questionKey(m, tea.KeyEnter, 0)
	clickText("2. Other")
	if !m.questions.editing {
		t.Fatal("clicking Other did not focus text input")
	}
	_, _ = m.Update(tea.PasteMsg{Content: "billing-worker"})
	questionKey(m, tea.KeyEnter, 0)
	clickText("billing-worker") // review row
	if m.questions.page != 1 || !m.questions.editing || m.questions.text.Value() != "billing-worker" {
		t.Fatal("review click did not reopen the saved custom answer")
	}
	clickText("✓ Review")
	if m.questions.page != 2 || backend.calls != 1 {
		t.Fatal("tab click failed or submitted the form")
	}
	questionKey(m, tea.KeyEnter, 0)
	drainConversationRemote(t, m)
	if backend.calls != 2 || len(backend.responses) != 1 {
		t.Fatal("mouse flow did not resume exactly once")
	}
}

func TestQuestionsWheelTargetsTheHoveredPane(t *testing.T) {
	m, _ := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	m.resize(36, 14)
	questionKey(m, '2', 0)
	questionKey(m, tea.KeyPgUp, 0)
	_ = m.View()
	before := m.list.VisibleSurface().Top
	_, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 3, Y: 0})
	if m.list.VisibleSurface().Top >= before || m.questions.scroll != 0 {
		t.Fatal("wheel above picker did not scroll only the conversation")
	}
	before = m.list.VisibleSurface().Top
	_, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 3, Y: m.questionTop() + 3})
	_ = m.View()
	if m.questions.scroll != 1 || m.list.VisibleSurface().Top != before {
		t.Fatal("wheel over picker did not scroll only the question")
	}
	// The option row moves up with scrolling; hit testing must move with it.
	for y, row := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if strings.Contains(row, "1. US") {
			_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: y})
			if m.questions.choices[0] != 0 || m.questions.answers[0] != "" {
				t.Fatal("scrolled choice hit the wrong target")
			}
			return
		}
	}
	t.Fatal("scrolled option was not visible")
}

func TestQuestionsLongCustomTextAndTabsFitBothThemes(t *testing.T) {
	m, _ := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	questionKey(m, tea.KeyUp, 0)
	_, _ = m.Update(tea.PasteMsg{Content: strings.Repeat("東京🙂", 50)})
	for _, dark := range []bool{true, false} {
		m.setDarkBackground(dark)
		for _, size := range [][2]int{{36, 14}, {40, 16}, {80, 24}, {120, 40}} {
			m.resize(size[0], size[1])
			view := m.View().Content
			if strings.Count(view, "\n")+1 > size[1] {
				t.Fatalf("custom input exceeds height at %v:\n%s", size, ansi.Strip(view))
			}
			for _, row := range strings.Split(view, "\n") {
				if ansi.StringWidth(row) > size[0] {
					t.Fatalf("custom input exceeds width at %v: %q", size, ansi.Strip(row))
				}
			}
		}
	}
}
