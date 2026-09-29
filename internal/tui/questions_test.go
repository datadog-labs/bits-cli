package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
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
	host := NewToolUI()
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
	case session := <-m.toolUI.requests:
		_, _ = m.Update(toolUIOpenedMsg{session: session})
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

func TestQuestionsAnswerReviewAndContinue(t *testing.T) {
	for _, mode := range []agent.PermissionsMode{agent.ModeManual, agent.ModeSkipPermissions} {
		t.Run(string(mode), func(t *testing.T) {
			m, backend := startQuestions(t, mode, questionInput, 1)
			waitQuestions(t, m)
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
			if m.activeToolUI != nil || !m.editor.Focused() {
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
	if m.activeToolUI == nil || !strings.Contains(ansi.Strip(m.View().Content), "› 1. US") {
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
	remaining := []string{m.queuedToolUIs[0].callID, m.queuedToolUIs[1].callID}
	slices.Sort(remaining)
	questionKey(m, tea.KeyEscape, 0)
	if m.activeToolUI == nil || m.activeToolUI.callID != remaining[0] {
		t.Fatalf("next form is not the earliest pending call %s", remaining[0])
	}
	// Cancelling a queued call must drop its form without stranding the others.
	m.engine.CancelTool(remaining[1])
	for m.turnEvents != nil {
		if m.activeToolUI != nil {
			if m.activeToolUI.callID == remaining[1] {
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

type resumedQuestionBackend struct {
	messages    []assistant.Message
	answers     chan []assistant.ClientToolResponse
	wantContext *assistant.AssistantContext
}

func (b *resumedQuestionBackend) Send(_ context.Context, message any, opts assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	responses, ok := message.([]assistant.ClientToolResponse)
	if !ok {
		return "", errors.New("resumed question sent a new user message")
	}
	if opts.ConversationID != resumeConversationID {
		return "", errors.New("resumed question used a different conversation")
	}
	if !reflect.DeepEqual(opts.Context, b.wantContext) {
		return "", errors.New("resumed turn lost its context")
	}
	for _, r := range responses {
		result := assistant.ToolResultContent(r.ToolCallID, r.Metadata.Name, r.Status, r.Metadata.Output)
		result.Type = assistant.ContentClientToolResponse
		b.messages = append(b.messages, assistant.Message{Role: "user", MessageID: r.ToolCallID + "-result", Content: result})
	}
	b.answers <- responses
	return resumeConversationID, emitMessages(emit, assistant.AssistantMessage("continued", assistant.TextContent("Continuing after the answer.")))
}

func (b *resumedQuestionBackend) ConversationHistory(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
	return history(b.messages...), nil
}

func (b *resumedQuestionBackend) UserConversations(context.Context) (*assistant.UserConversationsResponse, error) {
	return summaries(assistant.ConversationSummary{ConversationID: resumeConversationID, Title: "Questions"}), nil
}

func newResumedQuestionModel(t *testing.T, startup bool) (*Model, *resumedQuestionBackend) {
	t.Helper()
	call := assistant.ToolCallContent("resumed-question", spec.AskUserQuestion, questionInput)
	call.Type = assistant.ContentClientToolCall
	backend := &resumedQuestionBackend{
		messages: []assistant.Message{assistant.AssistantMessage("question-message", call)},
		answers:  make(chan []assistant.ClientToolResponse, 1),
	}
	second := assistant.ToolCallContent("second-question", spec.AskUserQuestion, questionInput)
	second.Type = assistant.ContentClientToolCall
	user := assistant.Message{MessageID: "original-user", Role: "user", Content: assistant.TextContent("investigate"), ContextEntities: json.RawMessage(`[{"type":"service","id":"api"}]`), ContextResources: json.RawMessage(`[{"name":"dashboard"}]`)}
	backend.messages = append([]assistant.Message{user}, backend.messages...)
	backend.messages = append(backend.messages, assistant.AssistantMessage("second", second), assistant.Message{Results: &assistant.Results{}}, assistant.AssistantMessage("end", assistant.Content{Type: assistant.ContentTurnStatus, TurnStatus: &assistant.TurnStatusPayload{Status: "ended"}}), assistant.AssistantMessage("internal", assistant.Content{Type: assistant.ContentProviderCompaction, Compaction: &assistant.CompactionPayload{Summary: "bookkeeping"}}))
	backend.wantContext = &assistant.AssistantContext{Entities: []assistant.ContextEntity{{Type: assistant.EntityService, ID: "api"}}, Resources: []json.RawMessage{json.RawMessage(`{"name":"dashboard"}`)}}
	host := NewToolUI()
	set, err := agent.NewToolSet(agent.ModeManual, tools.NewAskUserQuestionTool(host))
	if err != nil {
		t.Fatal(err)
	}
	opts := assistant.SendOptions{}
	if startup {
		opts.ConversationID = resumeConversationID
	}
	m := New(agent.New(backend, opts), Config{Tools: set, ToolUI: host})
	m.resize(100, 32)
	t.Cleanup(func() {
		if m.turnEvents != nil {
			m.cancelRemote()
			drainConversationRemote(t, m)
		}
	})
	return m, backend
}

func TestPendingQuestionRestoresOnStartupAndResume(t *testing.T) {
	for _, startup := range []bool{true, false} {
		name := "resume picker"
		if startup {
			name = "startup restore"
		}
		t.Run(name, func(t *testing.T) {
			m, backend := newResumedQuestionModel(t, startup)
			if startup {
				m.restoringHistory = true
				ctx, cancel := context.WithCancel(context.Background())
				m.beginRemote(m.engine.Restore(ctx), cancel)
			} else {
				cmd := m.openConversationPicker()
				_, _ = m.Update(runResumeCmd(t, cmd))
				_, cmd = m.Update(conversationview.SelectedMsg{Conversation: assistant.ConversationSummary{ConversationID: resumeConversationID}})
				_, _ = m.Update(runResumeCmd(t, cmd))
				if m.turnEvents == nil {
					t.Fatalf("resume failed: %+v", m.notice)
				}
			}
			waitQuestions(t, m)
			if len(backend.answers) != 0 {
				t.Fatal("sent a response before the user answered")
			}
			for range 2 {
				waitQuestions(t, m)
				questionKey(m, tea.KeyEnter, 0) // first answer
				questionKey(m, tea.KeyEnter, 0) // second answer
				questionKey(m, tea.KeyEnter, 0) // submit review
			}
			drainConversationRemote(t, m)
			if len(backend.answers) != 1 {
				t.Fatalf("backend received %d answer batches, want one", len(backend.answers))
			}
			responses := <-backend.answers
			if len(responses) != 2 || responses[0].ToolCallID != "resumed-question" || responses[1].ToolCallID != "second-question" {
				t.Fatalf("tool responses = %+v", responses)
			}
			if m.engine.CanResumeTools(m.tools) || m.activeToolUI != nil {
				t.Fatal("question stayed pending after continuation")
			}
		})
	}
}

func TestQuestionExitAndStopHaveDifferentPersistedOutcomes(t *testing.T) {
	for _, stop := range []bool{false, true} {
		name := "exit leaves questions resumable"
		if stop {
			name = "stop persists cancelled batch"
		}
		t.Run(name, func(t *testing.T) {
			m, backend := newResumedQuestionModel(t, true)
			m.restoringHistory = true
			ctx, cancel := context.WithCancel(t.Context())
			m.beginRemote(m.engine.Restore(ctx), cancel)
			waitQuestions(t, m)
			if stop {
				questionKey(m, 'x', tea.ModCtrl)
			} else {
				_, _ = m.quit()
			}
			drainConversationRemote(t, m)
			if stop {
				if len(backend.answers) != 1 {
					t.Fatal("cancellation was not persisted")
				}
				responses := <-backend.answers
				if len(responses) != 2 {
					t.Fatalf("cancelled responses = %v", responses)
				}
				for _, r := range responses {
					if r.Status != assistant.ToolStatusError {
						t.Fatalf("response = %+v", r)
					}
				}
				if hasTextBlock(m.transcript.Blocks, "Continuing after the answer.") {
					t.Fatal("stopped turn displayed a continuation")
				}
			} else if len(backend.answers) != 0 {
				t.Fatal("exit persisted an answer")
			}

			reopened := New(agent.New(backend, assistant.SendOptions{ConversationID: resumeConversationID}), Config{Tools: m.tools, ToolUI: m.toolUI})
			reopened.resize(100, 32)
			reopened.restoringHistory = true
			ctx, cancel = context.WithCancel(t.Context())
			reopened.beginRemote(reopened.engine.Restore(ctx), cancel)
			t.Cleanup(func() {
				if reopened.turnEvents != nil {
					reopened.cancelRemote()
					drainConversationRemote(t, reopened)
				}
			})
			if stop {
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
