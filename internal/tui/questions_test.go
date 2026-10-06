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

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/fake"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tools"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	conversationview "github.com/datadog-labs/bits-cli/internal/tui/conversations"
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
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	setConversationInput(m, "Help me choose")
	_, _ = m.submit()
	t.Cleanup(func() {
		if m.op.events != nil {
			m.cancelOperation()
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
	case ev, ok := <-m.op.events:
		if ok {
			_, _ = m.Update(turnEventMsg{generation: m.op.gen, ev: ev})
		} else {
			_, _ = m.Update(turnClosedMsg{generation: m.op.gen})
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a tool UI or turn event")
	}
}

func waitQuestions(t *testing.T, m *Model) {
	t.Helper()
	for dockedToolUI(m) == nil && m.op.events != nil {
		pumpToolUI(t, m)
	}
	if dockedToolUI(m) == nil {
		t.Fatal("turn ended without a question form")
	}
	// The engine queues the tool's transcript events before its handler runs.
	for len(m.op.events) > 0 {
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
			if m.editor.Focused() || waitingApprovals(m) != 0 {
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
			if dockedToolUI(m) != nil || !m.editor.Focused() {
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
	for dockedToolUI(m) == nil || len(queuedToolUIs(m)) < 2 {
		pumpToolUI(t, m)
	}
	for len(m.op.events) > 0 {
		pumpToolUI(t, m)
	}
	// Arrival order is up to the scheduler; queued forms follow the transcript.
	queued := queuedToolUIs(m)
	remaining := []string{queued[0].callID(), queued[1].callID()}
	slices.Sort(remaining)
	questionKey(m, tea.KeyEscape, 0)
	if dockedToolUI(m) == nil || dockedToolUI(m).Call.ID != remaining[0] {
		t.Fatalf("next form is not the earliest pending call %s", remaining[0])
	}
	// Cancelling a queued call must drop its form without stranding the others.
	if !m.engine.CancelTool(remaining[1]) {
		t.Fatal("cancellation command was not queued")
	}
	// CancelTool queues a command; wait for the engine to apply it before
	// dismissing the active form and advancing to the queued call.
	for !slices.ContainsFunc(m.transcript.Blocks, func(b agent.Block) bool {
		return b.ToolCallID() == remaining[1] && b.Tool.Status == agent.ToolCancelled
	}) {
		if m.op.events == nil {
			t.Fatal("turn ended before queued call was cancelled")
		}
		pumpToolUI(t, m)
	}
	for m.op.events != nil {
		if dockedToolUI(m) != nil {
			if dockedToolUI(m).Call.ID == remaining[1] {
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

func TestQuestionsReplaceTheComposerAndRestoreIt(t *testing.T) {
	m, backend := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"waiting for your answers", "Which region?"} {
		if !strings.Contains(view, want) {
			t.Fatalf("inline view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Working on it…") || !m.frame.replaced || m.editor.Focused() {
		t.Fatalf("the composer stayed behind the form:\n%s", view)
	}
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 10})
	questionKey(m, tea.KeyEscape, 0)
	if dockedToolUI(m) == nil || backend.calls != 1 {
		t.Fatal("hidden form consumed a key")
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	questionKey(m, tea.KeyEscape, 0)
	drainConversationRemote(t, m)
	if dockedToolUI(m) != nil || !m.frame.prompt.Empty() || !m.editor.Focused() {
		t.Fatal("dismissing the form did not hand the keyboard back to the composer")
	}
}

func TestQuestionsWheelTargetsTheHoveredPane(t *testing.T) {
	m, _ := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	m.Update(tea.WindowSizeMsg{Width: 36, Height: 14})
	questionKey(m, '2', 0)
	questionKey(m, tea.KeyPgUp, 0)
	_ = m.View()
	before, form := m.list.VisibleSurface().Top, m.frame.promptView
	_, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 3, Y: 0})
	if m.list.VisibleSurface().Top >= before || m.frame.promptView != form {
		t.Fatal("wheel above picker did not scroll only the conversation")
	}
	before = m.list.VisibleSurface().Top
	_, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 3, Y: m.frame.prompt.Min.Y + 3})
	if m.frame.promptView == form || m.list.VisibleSurface().Top != before {
		t.Fatal("wheel over picker did not scroll only the question")
	}
	// The option row moves up with scrolling; hit testing must move with it.
	for y, row := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if strings.Contains(row, "1. US") {
			_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: y})
			if !strings.Contains(ansi.Strip(m.frame.promptView), "› 1. US") {
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
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	if startup {
		_ = m.initChat()
	}
	t.Cleanup(func() {
		if m.op.events != nil {
			m.cancelOperation()
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
				if m.op.events == nil {
					t.Fatalf("resume failed: %+v", latestNotice(m))
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
			if m.engine.CanResumeTools(m.tools) || dockedToolUI(m) != nil {
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
				if dockedToolUI(reopened) != nil || reopened.engine.CanResumeTools(reopened.tools) {
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
			if dockedToolUI(m) != nil || m.op.events != nil {
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

// Focus gives the form the keyboard, but the pointer goes where it points: a
// click on the transcript above the form starts a transcript selection.
func TestQuestionsLeaveTheTranscriptSelectable(t *testing.T) {
	m, _ := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	form := m.frame.promptView
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: m.frame.transcript.Max.Y - 1})
	if !m.selection.selecting() || m.selection.scope != selectionScopeTranscript {
		t.Fatal("a click on the transcript did not start a transcript selection")
	}
	if m.frame.promptView != form {
		t.Fatal("a click on the transcript reached the form")
	}
	_, _ = m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 2, Y: m.frame.transcript.Max.Y - 1})
	if m.selection.selecting() || m.focus() != focusPrompt {
		t.Fatal("releasing the click did not hand the pointer back")
	}

	// Non-actionable form text also falls through to lower-pane selection.
	_, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 4, Y: m.frame.prompt.Min.Y + 2})
	if !m.selection.selecting() || m.selection.scope != selectionScopeLower {
		t.Fatal("a click on question text did not start a lower-pane selection")
	}
}

// Keys follow one rule with a form docked: pgup/pgdown page what owns the
// keyboard, shift+pgup/pgdown always page the transcript.
func TestQuestionsShiftPageScrollsTheTranscript(t *testing.T) {
	m, _ := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
	waitQuestions(t, m)
	for i := range 40 {
		m.notices = append(m.notices, chat.NoticeItem{ID: uint64(i + 1), Notice: chat.Notice{Level: chat.NoticeInfo, Text: fmt.Sprintf("line %d", i)}})
	}
	m.syncTranscript()
	m.Update(tea.WindowSizeMsg{Width: 36, Height: 16})

	before, form := m.list.VisibleSurface().Top, m.frame.promptView
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp, Mod: tea.ModShift})
	if m.list.VisibleSurface().Top >= before || m.frame.promptView != form {
		t.Fatal("shift+pgup did not page only the transcript")
	}
	before = m.list.VisibleSurface().Top
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.list.VisibleSurface().Top != before || m.frame.promptView == form {
		t.Fatal("pgdown did not page only the form")
	}
}
