package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

const forkSourceID = "12345678-1234-1234-1234-123456781234"

type tuiForkBackend struct {
	*resumeBackend
	calls int
	err   error
}

func (b *tuiForkBackend) ForkConversation(context.Context, string) (*assistant.ConversationSummary, error) {
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	return &assistant.ConversationSummary{ID: resumeConversationID, ConversationID: resumeConversationID}, nil
}

func TestForkSwitchesInPlace(t *testing.T) {
	b := &tuiForkBackend{resumeBackend: newResumeBackend()}
	response := history(assistant.AssistantMessage("copy", assistant.TextContent("Copied answer")))
	response.Data.Attributes.ForkedFrom = &assistant.ForkedFrom{ConversationID: forkSourceID, ForkMessageID: "copy"}
	b.histories <- resumeHistoryReply{response: response}
	e := agent.New(b, agent.Config{ConversationID: forkSourceID})
	defer e.Close()
	m := New(e)
	m.setMode(ModeChat)
	defer m.Detach()
	list := m.list
	_, cmd := m.dispatchCommand("branch", "")
	if cmd == nil || m.mode != ModeChat || !m.op.busy() {
		t.Fatalf("fork did not start in place: cmd=%v mode=%v op=%+v notices=%+v", cmd != nil, m.mode, m.op, m.notices)
	}
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "next draft"})
	msg := cmd().(forkResultMsg)
	m.applyForkResult(msg)
	if m.state.ConversationID != resumeConversationID || m.list != list || m.op.busy() || m.editor.Value() != "next draft" {
		t.Fatalf("fork state id=%s list=%v busy=%v draft=%q notices=%+v", m.state.ConversationID, m.list == list, m.op.busy(), m.editor.Value(), m.notices)
	}
	notices := m.transcriptNotices()
	if len(notices) != 1 || notices[0].Notice.Level != chat.NoticeFork || notices[0].After != m.state.Blocks[len(m.state.Blocks)-1].ID {
		t.Fatalf("notices=%+v", m.notices)
	}
	if !strings.Contains(notices[0].Notice.Text, forkSourceID) {
		t.Fatal("missing original identity")
	}
}

func TestForkLoadFailureKeepsSourceAndCreatedID(t *testing.T) {
	b := &tuiForkBackend{resumeBackend: newResumeBackend()}
	b.histories <- resumeHistoryReply{err: errors.New("offline")}
	e := agent.New(b, agent.Config{ConversationID: forkSourceID})
	defer e.Close()
	m := New(e)
	m.setMode(ModeChat)
	defer m.Detach()
	_, cmd := m.dispatchCommand("fork", "")
	m.applyForkResult(cmd().(forkResultMsg))
	if m.state.ConversationID != forkSourceID || m.op.busy() || b.calls != 1 {
		t.Fatal("failure changed state")
	}
	if !strings.Contains(m.notices[len(m.notices)-1].Notice.Text, resumeConversationID) {
		t.Fatal("lost created fork identity")
	}
}

func TestForkGuards(t *testing.T) {
	for _, kind := range []string{"empty", "busy", "arguments", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			b := &tuiForkBackend{resumeBackend: newResumeBackend()}
			e := agent.New(b, agent.Config{ConversationID: forkSourceID})
			defer e.Close()
			m := New(e)
			defer m.Detach()
			argument := ""
			switch kind {
			case "empty":
				m.state.ConversationID = ""
			case "busy":
				m.op.kind = opRestore
			case "arguments":
				argument = "name"
			case "duplicate":
				_, _ = m.dispatchCommand("fork", "")
				defer m.op.cancel()
			}
			_, cmd := m.dispatchCommand("fork", argument)
			if cmd != nil || b.calls != 0 {
				t.Fatal("guard started work")
			}
		})
	}
}

func TestForkCancellationAndFollowUps(t *testing.T) {
	for _, action := range []string{"cancel", "new", "logout", "quit", "stale"} {
		t.Run(action, func(t *testing.T) {
			b := &tuiForkBackend{resumeBackend: newResumeBackend()}
			b.histories <- resumeHistoryReply{response: history(assistant.AssistantMessage("copy", assistant.TextContent("Copied answer")))}
			e := agent.New(b, agent.Config{ConversationID: forkSourceID})
			defer e.Close()
			m := New(e)
			defer m.Detach()
			_, cmd := m.dispatchCommand("fork", "")
			msg := cmd().(forkResultMsg)
			switch action {
			case "cancel":
				m.cancelOperation()
			case "new":
				m.requestNewConversation()
			case "logout":
				m.logout = func(context.Context) (bool, error, error) { return true, nil, nil }
				m.requestLogout()
			case "quit":
				m.quit()
			case "stale":
				m.op.gen++
				defer m.op.cancel()
			}
			m.applyForkResult(msg)
			if m.state.ConversationID == resumeConversationID {
				t.Fatal("canceled fork installed")
			}
			switch action {
			case "new":
				if m.state.ConversationID != "" || m.op.busy() {
					t.Fatal("new did not drain")
				}
				if !strings.Contains(m.notices[len(m.notices)-1].Notice.Text, resumeConversationID) {
					t.Fatal("new conversation lost fork recovery notice")
				}
			case "logout":
				if m.op.kind != opLogout {
					t.Fatal("logout did not drain")
				}
				m.op.cancel()
			case "stale":
				if m.op.kind != opFork {
					t.Fatal("stale result ended newer operation")
				}
			default:
				if m.op.busy() {
					t.Fatal("cancel did not drain")
				}
				if !strings.Contains(m.notices[len(m.notices)-1].Notice.Text, resumeConversationID) {
					t.Fatal("lost created ID after cancellation")
				}
			}
		})
	}
}

func TestForkDoesNotResumeCopiedTools(t *testing.T) {
	b := &tuiForkBackend{resumeBackend: newResumeBackend()}
	call := assistant.AssistantMessage("pending", assistant.Content{Type: assistant.ContentClientToolCall, Tool: &assistant.ToolPayload{ToolCallID: "pending", IsClientSide: true, Metadata: &assistant.ToolMetadata{Name: "input", Input: "{}"}}})
	response := history(call)
	response.Data.Attributes.ForkedFrom = &assistant.ForkedFrom{ConversationID: forkSourceID, ForkMessageID: call.MessageID}
	b.histories <- resumeHistoryReply{response: response}
	toolSet, err := agent.NewToolSet(agent.Tool{Definition: assistant.ClientTool{Name: "input"}, Resumable: true, Handler: func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
		t.Error("fork executed copied tool")
		return agent.ToolResult{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	e := agent.New(b, agent.Config{ConversationID: forkSourceID})
	defer e.Close()
	m := New(e, Config{Tools: toolSet})
	defer m.Detach()
	_, cmd := m.dispatchCommand("fork", "")
	follow := m.applyForkResult(cmd().(forkResultMsg))
	if follow != nil || m.op.busy() || e.OperationActive() {
		t.Fatal("fork started an operation")
	}
	if e.CanResumeTools(toolSet) {
		t.Fatal("copied call remained resumable")
	}
	if len(m.state.Blocks) != 1 || m.state.Blocks[0].Tool.Status != agent.ToolCancelled {
		t.Fatal("copied call was not settled")
	}
}

type blockedForkHistory struct {
	*tuiForkBackend
	started chan struct{}
}

func (b *blockedForkHistory) ConversationHistory(ctx context.Context, _ assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
	close(b.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestForkCancelStopsHistoryRead(t *testing.T) {
	b := &blockedForkHistory{tuiForkBackend: &tuiForkBackend{resumeBackend: newResumeBackend()}, started: make(chan struct{})}
	e := agent.New(b, agent.Config{ConversationID: forkSourceID})
	defer e.Close()
	m := New(e)
	defer m.Detach()
	_, cmd := m.dispatchCommand("fork", "")
	results := make(chan tea.Msg, 1)
	go func() { results <- cmd() }()
	select {
	case <-b.started:
	case <-time.After(5 * time.Second):
		t.Fatal("history did not start")
	}
	m.cancelOperation()
	select {
	case result := <-results:
		m.applyForkResult(result.(forkResultMsg))
		if m.op.busy() || m.state.ConversationID != forkSourceID {
			t.Fatal("cancellation did not preserve source")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("history request was not canceled")
	}
}

func TestForkMarkerReconstructedFromHistory(t *testing.T) {
	b := newResumeBackend()
	response := history(assistant.AssistantMessage("copy", assistant.TextContent("COPIED")), assistant.AssistantMessage("later", assistant.TextContent("LATER")))
	response.Data.Attributes.ForkedFrom = &assistant.ForkedFrom{ConversationID: forkSourceID, ForkMessageID: "copy", SourceTitle: "Source title"}
	b.histories <- resumeHistoryReply{response: response}
	e := agent.New(b, agent.Config{})
	defer e.Close()
	conversation, err := e.LoadConversation(context.Background(), resumeConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.InstallConversation(context.Background(), conversation); err != nil {
		t.Fatal(err)
	}
	m := New(e)
	defer m.Detach()
	m.list.SetWidth(100)
	m.list.SetHeight(20)
	m.syncTranscript()
	m.syncTranscript()
	doc := m.list.Document()
	if strings.Count(doc, "Forked conversation") != 1 || !strings.Contains(doc, "Source title") || !strings.Contains(doc, resumeConversationID) {
		t.Fatalf("missing/duplicate marker: %s", doc)
	}
	if strings.Index(doc, "COPIED") >= strings.Index(doc, "Forked conversation") || strings.Index(doc, "Forked conversation") >= strings.Index(doc, "LATER") {
		t.Fatalf("wrong boundary: %s", doc)
	}
	if len(m.notices) != 0 {
		t.Fatal("persisted marker duplicated into local notices")
	}
}

func TestForkTimeoutDoesNotAdviseRetry(t *testing.T) {
	b := &tuiForkBackend{resumeBackend: newResumeBackend(), err: &assistant.APIError{StatusCode: 504}}
	e := agent.New(b, agent.Config{ConversationID: forkSourceID})
	defer e.Close()
	m := New(e)
	defer m.Detach()
	_, cmd := m.dispatchCommand("fork", "")
	m.applyForkResult(cmd().(forkResultMsg))
	for _, n := range m.notices {
		if strings.Contains(strings.ToLower(n.Notice.Text), "please try again") {
			t.Fatal("fork timeout advises duplicate mutation")
		}
	}
}
