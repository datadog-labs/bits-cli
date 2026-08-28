package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools/localtime"
)

type approvalBackend struct {
	t         *testing.T
	responses []assistant.ClientToolResponse
	calls     int
}

func (b *approvalBackend) Send(_ context.Context, message any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.t.Helper()
	b.calls++
	if b.calls == 1 {
		content := assistant.ToolCallContent("time-call", localtime.Name, `{}`)
		content.Type = assistant.ContentClientToolCall
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("time-message", content)
		return "conversation-1", emit(response)
	}

	var ok bool
	b.responses, ok = message.([]assistant.ClientToolResponse)
	if !ok {
		b.t.Fatalf("tool follow-up has type %T", message)
	}
	var response assistant.AssistantResponse
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("It is noon."))
	return "conversation-1", emit(response)
}

func TestToolApprovalComposerE2E(t *testing.T) {
	tests := []struct {
		name      string
		navigate  int
		deny      bool
		selection string
	}{
		{name: "deny", deny: true, selection: "[ Deny ]"},
		{name: "allow once", navigate: 1, selection: "[ Allow once ]"},
		{name: "allow for session", navigate: 2, selection: "[ Allow for session ]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &approvalBackend{t: t}
			fixedTime := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
			tools, err := agent.NewToolSet(localtime.New(func() time.Time { return fixedTime }))
			if err != nil {
				t.Fatal(err)
			}
			model := New(agent.New(backend, assistant.SendOptions{}), Config{Tools: tools})
			model.resize(80, 24)
			setConversationInput(model, "What time is it?")
			_, _ = model.submit()

			for len(model.pendingApprovals) == 0 {
				msg := runConversationCmd(t, waitEvent(model.turnGen, model.turnEvents))
				_, _ = model.Update(msg)
			}
			for range tt.navigate {
				_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
			}

			view := ansi.Strip(model.View().Content)
			if !strings.Contains(view, "Approval required") || !strings.Contains(view, "Share your local time?") || !strings.Contains(view, tt.selection) {
				t.Fatalf("approval composer not rendered:\n%s", view)
			}
			lines := strings.Split(view, "\n")
			if len(lines) > 24 {
				t.Fatalf("approval view height = %d, want <= 24", len(lines))
			}
			for _, line := range lines {
				if width := ansi.StringWidth(line); width > 80 {
					t.Fatalf("approval view width = %d, want <= 80", width)
				}
			}
			_, _ = model.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
			if model.editor.Value() != "" {
				t.Fatal("approval input leaked into the editor")
			}

			if tt.deny {
				_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
			} else {
				_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			}
			drainConversationRemote(t, model)

			if strings.Contains(ansi.Strip(model.View().Content), "Approval required") {
				t.Fatal("approval composer remained after the decision")
			}
			if tt.deny {
				if backend.calls != 1 {
					t.Fatalf("backend calls = %d, want 1", backend.calls)
				}
				return
			}
			if len(backend.responses) != 1 || !strings.Contains(backend.responses[0].Metadata.Output, `"utc_offset":"+02:00"`) {
				t.Fatalf("local-time response = %+v", backend.responses)
			}
		})
	}
}
