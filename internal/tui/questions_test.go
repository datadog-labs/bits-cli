package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/agent/fake"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tools/spec"
	conversationview "github.com/DataDog/bits-cli/internal/tui/conversations"
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
	host := tools.NewUI()
	set, err := agent.NewToolSet(mode, tools.NewAskUserQuestionTool(host))
	if err != nil {
		t.Fatal(err)
	}
	m := New(agent.New(backend, assistant.SendOptions{}), Config{Tools: set, ToolUI: host})
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

// pumpToolUI applies the next tool UI request or turn event, whichever comes first.
func pumpToolUI(t *testing.T, m *Model) {
	t.Helper()
	select {
	case request := <-m.toolUI.Requests():
		_, _ = m.Update(toolUIOpenedMsg{request: request})
	case ev, ok := <-m.turnEvents:
		if ok {
			_, _ = m.Update(turnEventMsg{generation: m.turnGen, ev: ev})
		} else {
			_, _ = m.Update(turnClosedMsg{generation: m.turnGen})
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a tool UI or turn event")
	}
}

func waitQuestions(t *testing.T, m *Model) {
	t.Helper()
	for m.activeToolUI == nil && m.turnEvents != nil {
		pumpToolUI(t, m)
	}
	if m.activeToolUI == nil {
		t.Fatal("turn ended without a question form")
	}
	// The engine queues the tool's transcript events before its handler runs.
	for len(m.turnEvents) > 0 {
		pumpToolUI(t, m)
	}
}

func questionKey(m *Model, code rune, mod tea.KeyMod) {
	_, _ = m.Update(tea.KeyPressMsg{Code: code, Mod: mod})
}

func TestQuestionsCompleteTheCall(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    agent.PermissionsMode
		keys    []rune
		status  assistant.ToolStatus
		message string
	}{
		// The extra Enter checks that a finished form cannot answer twice.
		{"answered", agent.ModeManual, []rune{tea.KeyEnter, tea.KeyEnter, tea.KeyEnter, tea.KeyEnter}, assistant.ToolStatusSuccess, "Q: Which region?\nA: US\n\nQ: Which service?\nA: API"},
		{"dismissed", agent.ModeSkipPermissions, []rune{tea.KeyEnter, tea.KeyEscape}, assistant.ToolStatusError, "without answering"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, backend := startQuestions(t, tc.mode, questionInput, 1)
			waitQuestions(t, m)
			if m.editor.Focused() || len(m.pendingApprovals) != 0 {
				t.Fatal("question incorrectly routed through editor or permissions")
			}
			if backend.calls != 1 || len(backend.definitions) != 1 || backend.definitions[0].Name != spec.AskUserQuestion {
				t.Fatalf("calls=%d definitions=%v", backend.calls, backend.definitions)
			}
			for _, key := range tc.keys {
				questionKey(m, key, 0)
			}
			drainConversationRemote(t, m)
			if backend.calls != 2 || len(backend.responses) != 1 {
				t.Fatalf("calls=%d responses=%v", backend.calls, backend.responses)
			}
			response := backend.responses[0]
			var result spec.AskUserQuestionOutput
			if err := json.Unmarshal([]byte(response.Metadata.Output), &result); err != nil {
				t.Fatal(err)
			}
			if response.ToolCallID != "question-0" || response.Status != tc.status || !strings.Contains(result.Message, tc.message) {
				t.Fatalf("response=%+v", response)
			}
			if m.activeToolUI != nil || !m.editor.Focused() {
				t.Fatal("form did not release input")
			}
			if !strings.Contains(ansi.Strip(m.list.Document()), "Continuing with your answers.") {
				t.Fatal("transcript missing the continuation")
			}
		})
	}
}

func TestQuestionsMultipleCallsAndToolCancellation(t *testing.T) {
	m, backend := startQuestions(t, agent.ModeSkipPermissions, questionInput, 3)
	for m.activeToolUI == nil || len(m.queuedToolUIs) < 2 {
		pumpToolUI(t, m)
	}
	for len(m.turnEvents) > 0 {
		pumpToolUI(t, m)
	}
	// Arrival order is up to the scheduler; queued forms follow the transcript.
	slices.Reverse(m.queuedToolUIs)
	remaining := []string{m.queuedToolUIs[0].request.Call.ID, m.queuedToolUIs[1].request.Call.ID}
	slices.Sort(remaining)
	questionKey(m, tea.KeyEscape, 0)
	if m.activeToolUI == nil || m.activeToolUI.request.Call.ID != remaining[0] {
		t.Fatalf("next form is not the earliest pending call %s", remaining[0])
	}
	// Cancelling a queued call must drop its form without stranding the others.
	m.engine.CancelTool(remaining[1])
	for m.turnEvents != nil {
		if m.activeToolUI != nil {
			if m.activeToolUI.request.Call.ID == remaining[1] {
				t.Fatal("cancelled call showed its form")
			}
			questionKey(m, tea.KeyEscape, 0)
		}
		pumpToolUI(t, m)
	}
	if backend.calls != 2 || len(backend.responses) != 3 {
		t.Fatalf("calls=%d responses=%+v", backend.calls, backend.responses)
	}
	for i, response := range backend.responses {
		if response.ToolCallID != fmt.Sprintf("question-%d", i) {
			t.Fatal("response order changed")
		}
		if cancelled := response.ToolCallID == remaining[1]; cancelled != strings.Contains(response.Metadata.Output, "cancelled") {
			t.Fatalf("response %s = %q", response.ToolCallID, response.Metadata.Output)
		}
	}
}

func TestQuestionsReplaceComposerAndRestoreIt(t *testing.T) {
	m, backend := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"Help me choose", "waiting for your answers", "Which region?"} {
		if !strings.Contains(view, want) {
			t.Fatalf("inline view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Working on it…") {
		t.Fatal("composer was rendered behind the form")
	}
	if m.list.Height() < 3 || m.list.Height()+m.activeToolUI.component.Height()+chatNoticeHeight+chatFooterHeight != m.height {
		t.Fatal("form did not reserve space for the conversation")
	}
	m.resize(30, 10)
	questionKey(m, tea.KeyEscape, 0)
	if m.activeToolUI == nil || backend.calls != 1 {
		t.Fatal("hidden form consumed a key")
	}
	m.resize(80, 24)
	questionKey(m, tea.KeyEscape, 0)
	drainConversationRemote(t, m)
	if m.activeToolUI != nil || m.list.Height() != m.height-chatNoticeHeight-chatFooterHeight-m.editor.Height() {
		t.Fatal("dismissing the form did not restore the transcript height")
	}
}

func TestQuestionsWheelTargetsTheHoveredPane(t *testing.T) {
	m, _ := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	m.resize(36, 14)
	questionKey(m, '2', 0)
	questionKey(m, tea.KeyPgUp, 0)
	_ = m.View()
	before, form := m.list.VisibleSurface().Top, m.activeToolUI.component.View()
	_, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 3, Y: 0})
	if m.list.VisibleSurface().Top >= before || m.activeToolUI.component.View() != form {
		t.Fatal("wheel above picker did not scroll only the conversation")
	}
	before = m.list.VisibleSurface().Top
	_, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 3, Y: m.toolUITop() + 3})
	if m.activeToolUI.component.View() == form || m.list.VisibleSurface().Top != before {
		t.Fatal("wheel over picker did not scroll only the question")
	}
	// The option row moves up with scrolling; hit testing must move with it.
	for y, row := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if strings.Contains(row, "1. US") {
			_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: y})
			if !strings.Contains(ansi.Strip(m.activeToolUI.component.View()), "› 1. US") {
				t.Fatal("scrolled choice hit the wrong target")
			}
			return
		}
	}
	t.Fatal("scrolled option was not visible")
}

// pushQuestions pushes a conversation an earlier session left with two
// unanswered questions.
func pushQuestions(t *testing.T) (*fake.Fake, string) {
	t.Helper()
	f := &fake.Fake{ContinueDir: t.TempDir()}
	script := fmt.Sprintf(`call([("ask_user_question", %q), ("ask_user_question", %q)]); say("Continuing after the answer.")`, questionInput, questionInput)
	id, err := f.PushConversation(t.Context(), "Questions", script)
	if err != nil {
		t.Fatal(err)
	}
	return f, id
}

// newResumedQuestionModel opens conversation id at startup, or idle so the
// test can pick it with /resume.
func newResumedQuestionModel(t *testing.T, f *fake.Fake, id string, startup bool) *Model {
	t.Helper()
	host := tools.NewUI()
	set, err := agent.NewToolSet(agent.ModeManual, tools.NewAskUserQuestionTool(host))
	if err != nil {
		t.Fatal(err)
	}
	opts := assistant.SendOptions{}
	if startup {
		opts.ConversationID = id
	}
	m := New(agent.New(f, opts), Config{Tools: set, ToolUI: host})
	m.resize(100, 32)
	if startup {
		_ = m.initChat()
	}
	t.Cleanup(func() {
		if m.turnEvents != nil {
			m.cancelRemote()
			drainConversationRemote(t, m)
		}
	})
	return m
}

// continued reports whether the assistant answered after the questions. The
// user block holds the same text as script source.
func continued(m *Model) bool {
	return slices.ContainsFunc(m.transcript.Blocks, func(b agent.Block) bool {
		return b.Role == assistant.RoleAssistant && b.Markdown != nil && strings.Contains(b.Markdown.Content, "Continuing after the answer.")
	})
}

// questionResponses returns the tool responses the conversation recorded.
func questionResponses(t *testing.T, f *fake.Fake, id string) []assistant.Content {
	t.Helper()
	history, err := f.ConversationHistory(t.Context(), assistant.ConversationHistoryInput{ConversationID: id})
	if err != nil {
		t.Fatal(err)
	}
	var responses []assistant.Content
	for _, message := range history.Data.Attributes.Messages {
		if message.Content.Type == assistant.ContentClientToolResponse {
			responses = append(responses, message.Content)
		}
	}
	return responses
}

func TestPendingQuestionRestoresOnStartupAndResume(t *testing.T) {
	for _, startup := range []bool{true, false} {
		name := "resume picker"
		if startup {
			name = "startup restore"
		}
		t.Run(name, func(t *testing.T) {
			f, id := pushQuestions(t)
			m := newResumedQuestionModel(t, f, id, startup)
			if !startup {
				cmd := m.openConversationPicker()
				_, _ = m.Update(runResumeCmd(t, cmd))
				_, cmd = m.Update(conversationview.SelectedMsg{Conversation: assistant.ConversationSummary{ConversationID: id}})
				_, _ = m.Update(runResumeCmd(t, cmd))
				if m.turnEvents == nil {
					t.Fatalf("resume failed: %+v", m.notice)
				}
			}
			waitQuestions(t, m)
			if len(questionResponses(t, f, id)) != 0 {
				t.Fatal("sent a response before the user answered")
			}
			for range 2 {
				waitQuestions(t, m)
				questionKey(m, tea.KeyEnter, 0) // first answer
				questionKey(m, tea.KeyEnter, 0) // second answer
				questionKey(m, tea.KeyEnter, 0) // submit review
			}
			drainConversationRemote(t, m)
			responses := questionResponses(t, f, id)
			if len(responses) != 2 || responses[0].Tool.Status != string(assistant.ToolStatusSuccess) || responses[1].Tool.Status != string(assistant.ToolStatusSuccess) {
				t.Fatalf("tool responses = %+v", responses)
			}
			if !continued(m) {
				t.Fatal("the pushed turn did not continue")
			}
			if m.engine.CanResumeTools(m.tools) || m.activeToolUI != nil {
				t.Fatal("question stayed pending after continuation")
			}
		})
	}
}

func TestQuestionExitAndStopHaveDifferentPersistedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		act       func(*Model)
		cancelled int // responses persisted for the two pending calls
	}{
		{"exit leaves questions resumable", func(m *Model) { _, _ = m.quit() }, 0},
		{"stop persists cancelled batch", func(m *Model) { questionKey(m, 'x', tea.ModCtrl) }, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, id := pushQuestions(t)
			m := newResumedQuestionModel(t, f, id, true)
			waitQuestions(t, m)
			tc.act(m)
			drainConversationRemote(t, m)
			responses := questionResponses(t, f, id)
			if len(responses) != tc.cancelled {
				t.Fatalf("persisted responses = %+v", responses)
			}
			for _, r := range responses {
				if r.Tool.Status != string(assistant.ToolStatusError) {
					t.Fatalf("response = %+v", r)
				}
			}
			if continued(m) {
				t.Fatal("ended turn displayed a continuation")
			}

			reopened := newResumedQuestionModel(t, f, id, true)
			if tc.cancelled > 0 {
				drainConversationRemote(t, reopened)
				if reopened.activeToolUI != nil || reopened.engine.CanResumeTools(reopened.tools) {
					t.Fatal("cancelled questions reopened")
				}
			} else {
				waitQuestions(t, reopened)
			}
		})
	}
}

func TestRestoredCallsThatWillNotResumeSettle(t *testing.T) {
	ask := fmt.Sprintf(`("ask_user_question", %q)`, questionInput)
	for _, tc := range []struct {
		name    string
		scripts []string
	}{
		// The tool set has no read_file, so the batch cannot resume.
		{"mixed batch", []string{`call([` + ask + `, ("read_file", {"path": "go.mod"})])`}},
		{"abandoned by a later turn", []string{`call([` + ask + `])`, `say("moved on")`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake.Fake{ContinueDir: t.TempDir()}
			id, err := f.PushConversation(t.Context(), "", tc.scripts...)
			if err != nil {
				t.Fatal(err)
			}
			m := newResumedQuestionModel(t, f, id, true)
			drainConversationRemote(t, m)
			if m.activeToolUI != nil || m.turnEvents != nil {
				t.Fatal("a call that cannot resume opened")
			}
			for _, block := range m.transcript.Blocks {
				if block.Tool != nil && block.Tool.Status != agent.ToolCancelled {
					t.Fatalf("restored %s still looks running", block.Tool.Name)
				}
			}
			if len(questionResponses(t, f, id)) != 0 {
				t.Fatal("settling persisted a response")
			}
		})
	}
}
