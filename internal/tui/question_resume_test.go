package tui

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tools/spec"
	conversationview "github.com/DataDog/bits-cli/internal/tui/conversations"
)

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
				if m.turnEvents == nil {
					t.Fatalf("resume failed: %+v", m.notice)
				}
				cmd = waitEvent(m.turnGen, m.turnEvents)
			}
			driveResumedQuestion(t, m, cmd)
			if m.questions.page != 0 || m.questions.request == nil || !m.questions.request.Pending() {
				t.Fatalf("restored picker state = %+v", m.questions)
			}
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
			if m.engine.CanResumeTools(m.tools) || m.questions != nil {
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
			driveResumedQuestion(t, m, m.beginRemote(m.engine.Restore(ctx), cancel))
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

			reopened := New(agent.New(backend, assistant.SendOptions{ConversationID: resumeConversationID}), Config{Tools: m.tools})
			reopened.resize(100, 32)
			reopened.restoringHistory = true
			ctx, cancel = context.WithCancel(t.Context())
			cmd := reopened.beginRemote(reopened.engine.Restore(ctx), cancel)
			t.Cleanup(func() {
				if reopened.turnEvents != nil {
					reopened.cancelRemote()
					drainConversationRemote(t, reopened)
				}
			})
			if stop {
				drainConversationRemote(t, reopened)
				if reopened.questions != nil || reopened.engine.CanResumeTools(reopened.tools) {
					t.Fatal("cancelled questions reopened")
				}
			} else {
				driveResumedQuestion(t, reopened, cmd)
			}
		})
	}
}
