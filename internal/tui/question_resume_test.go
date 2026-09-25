package tui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tools/spec"
	conversationview "github.com/DataDog/bits-cli/internal/tui/conversations"
)

type resumedQuestionBackend struct {
	messages []assistant.Message
	answers  chan []assistant.ClientToolResponse
}

func (b *resumedQuestionBackend) Send(_ context.Context, message any, opts assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	responses, ok := message.([]assistant.ClientToolResponse)
	if !ok {
		return "", errors.New("resumed question sent a new user message")
	}
	if opts.ConversationID != resumeConversationID {
		return "", errors.New("resumed question used a different conversation")
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
	set, err := agent.NewToolSet(agent.ModeManual, tools.NewAskUserQuestionTool())
	if err != nil {
		t.Fatal(err)
	}
	opts := assistant.SendOptions{}
	if startup {
		opts.ConversationID = resumeConversationID
	}
	m := New(agent.New(backend, opts), Config{Tools: set})
	m.resize(100, 32)
	t.Cleanup(func() {
		if m.turnEvents != nil {
			m.cancelRemote()
			drainConversationRemote(t, m)
		}
	})
	return m, backend
}

func driveResumedQuestion(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	for i := 0; i < 20 && m.questions == nil; i++ {
		if cmd == nil {
			t.Fatal("resume stopped before opening the question picker")
		}
		msg := runConversationCmd(t, cmd)
		_, next := m.Update(msg)
		if _, ok := msg.(turnEventMsg); ok {
			cmd = waitEvent(m.turnGen, m.turnEvents)
		} else {
			cmd = next
		}
	}
	if m.questions == nil {
		t.Fatal("question picker did not reopen")
	}
}

func TestPendingQuestionRestoresOnStartupAndResume(t *testing.T) {
	for _, startup := range []bool{true, false} {
		name := "resume picker"
		if startup {
			name = "startup restore"
		}
		t.Run(name, func(t *testing.T) {
			m, backend := newResumedQuestionModel(t, startup)
			var cmd tea.Cmd
			if startup {
				m.restoringHistory = true
				ctx, cancel := context.WithCancel(context.Background())
				cmd = m.beginRemote(m.engine.Restore(ctx), cancel)
			} else {
				cmd = m.openConversationPicker()
				_, _ = m.Update(runResumeCmd(t, cmd))
				_, cmd = m.Update(conversationview.SelectedMsg{Conversation: assistant.ConversationSummary{ConversationID: resumeConversationID}})
				_, _ = m.Update(runResumeCmd(t, cmd))
				cmd = waitEvent(m.turnGen, m.turnEvents)
			}
			driveResumedQuestion(t, m, cmd)
			if m.questions.page != 0 || m.questions.request == nil || !m.questions.request.Pending() {
				t.Fatalf("restored picker state = %+v", m.questions)
			}
			if len(backend.answers) != 0 {
				t.Fatal("sent a response before the user answered")
			}
			questionKey(m, tea.KeyEnter, 0) // first answer
			questionKey(m, tea.KeyEnter, 0) // second answer
			questionKey(m, tea.KeyEnter, 0) // submit review
			drainConversationRemote(t, m)
			if len(backend.answers) != 1 {
				t.Fatalf("backend received %d answer batches, want one", len(backend.answers))
			}
			responses := <-backend.answers
			if len(responses) != 1 || responses[0].ToolCallID != "resumed-question" {
				t.Fatalf("tool responses = %+v", responses)
			}
			if m.engine.HasPendingQuestion() || m.questions != nil {
				t.Fatal("question stayed pending after continuation")
			}
		})
	}
}
