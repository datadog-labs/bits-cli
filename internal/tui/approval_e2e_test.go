package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

const approvalToolName = "confirm_action"

// newApprovalTool is a self-contained tool that always requires approval,
// used to drive the approval composer in these end-to-end tests.
func newApprovalTool() agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        approvalToolName,
			Description: "Test tool that requires approval before running.",
			InputSchema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		},
		Approval: func(agent.ToolCall) (agent.ApprovalRequirement, bool) {
			return agent.ApprovalRequirement{
				Key: agent.ApprovalKey{Tool: approvalToolName, Resource: "test"},
				Prompt: agent.ApprovalPrompt{
					Title:  "Run the test action?",
					Detail: "This test tool requires approval before it runs.",
				},
			}, true
		},
		Handler: func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
			return agent.ToolResult{Title: "Done", Output: `{"status":"ok"}`}, nil
		},
	}
}

type approvalBackend struct {
	t         *testing.T
	responses []assistant.ClientToolResponse
	calls     int
}

func (b *approvalBackend) Send(_ context.Context, message any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.t.Helper()
	b.calls++
	if b.calls == 1 {
		content := assistant.ToolCallContent("tool-call", approvalToolName, `{}`)
		content.Type = assistant.ContentClientToolCall
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("tool-message", content)
		return "conversation-1", emit(response)
	}

	var ok bool
	b.responses, ok = message.([]assistant.ClientToolResponse)
	if !ok {
		b.t.Fatalf("tool follow-up has type %T", message)
	}
	var response assistant.AssistantResponse
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("Done."))
	return "conversation-1", emit(response)
}

func TestApprovalBlursEditorUntilResolved(t *testing.T) {
	backend := &approvalBackend{t: t}
	tools, err := agent.NewToolSet(agent.ModeGated, newApprovalTool())
	if err != nil {
		t.Fatal(err)
	}
	model := New(agent.New(backend, assistant.SendOptions{}), Config{Tools: tools})
	model.Init() // focuses the editor, starting the cursor blink
	model.resize(80, 24)
	if !model.editor.Focused() {
		t.Fatal("editor not focused after init")
	}

	setConversationInput(model, "Run the action")
	_, _ = model.submit()
	for len(model.pendingApprovals) == 0 {
		msg := runConversationCmd(t, waitEvent(model.turnGen, model.turnEvents))
		_, _ = model.Update(msg)
	}
	if model.editor.Focused() {
		t.Fatal("editor stayed focused while an approval owned the composer")
	}

	_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape}) // deny
	drainConversationRemote(t, model)
	if !model.editor.Focused() {
		t.Fatal("editor not refocused after the approval resolved")
	}
}

func TestConcealedApprovalIgnoresAllKeysUntilResized(t *testing.T) {
	backend := &approvalBackend{t: t}
	tools, err := agent.NewToolSet(agent.ModeGated, newApprovalTool())
	if err != nil {
		t.Fatal(err)
	}
	model := New(agent.New(backend, assistant.SendOptions{}), Config{Tools: tools})
	model.resize(80, 24)
	setConversationInput(model, "Run the action")
	_, _ = model.submit()
	for len(model.pendingApprovals) == 0 {
		msg := runConversationCmd(t, waitEvent(model.turnGen, model.turnEvents))
		_, _ = model.Update(msg)
	}

	// Shrink below the approval minimum: View hides the whole chat behind the
	// resize hint, so no keypress may drive the hidden prompt — not even Esc.
	model.resize(minimumApprovalWidth-1, minimumApprovalHeight)
	if !model.chatViewTooSmall() {
		t.Fatal("chat not concealed at the reduced size")
	}
	if view := ansi.Strip(model.View().Content); strings.Contains(view, "Permission Required") {
		t.Fatalf("concealed approval still rendered:\n%s", view)
	}

	for _, code := range []rune{tea.KeyRight, tea.KeyEnter, tea.KeyEscape} {
		_, _ = model.Update(tea.KeyPressMsg{Code: code})
	}
	if len(model.pendingApprovals) == 0 {
		t.Fatal("a concealed keypress answered the approval")
	}
	if backend.calls != 1 {
		t.Fatalf("backend calls = %d while concealed, want 1 (no follow-up)", backend.calls)
	}

	// Resizing back restores the prompt and its controls; Esc then denies.
	model.resize(80, 24)
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	drainConversationRemote(t, model)
	// The denial is answered on the wire; the follow-up answer never renders.
	if backend.calls != 2 || len(backend.responses) != 1 {
		t.Fatalf("deny after resize not honored: calls=%d responses=%d", backend.calls, len(backend.responses))
	}
	if backend.responses[0].Status != assistant.ToolStatusError || backend.responses[0].ToolCallID != "tool-call" {
		t.Fatalf("denial response = %+v", backend.responses[0])
	}
	if view := ansi.Strip(model.View().Content); strings.Contains(view, "Done.") {
		t.Fatalf("aborted deny still delivered the model's follow-up answer:\n%s", view)
	}
}

func TestToolApprovalComposerSuppressedInAllowAll(t *testing.T) {
	backend := &approvalBackend{t: t}
	tools, err := agent.NewToolSet(agent.ModeAllowAll, newApprovalTool())
	if err != nil {
		t.Fatal(err)
	}
	model := New(agent.New(backend, assistant.SendOptions{}), Config{Tools: tools})
	model.resize(80, 24)
	setConversationInput(model, "Run the action")
	_, _ = model.submit()
	drainConversationRemote(t, model)

	if len(model.pendingApprovals) != 0 {
		t.Fatalf("allow-all surfaced %d approval prompts", len(model.pendingApprovals))
	}
	view := ansi.Strip(model.View().Content)
	if strings.Contains(view, "Permission Required") || strings.Contains(view, "Run the test action?") {
		t.Fatalf("approval composer rendered in allow-all mode:\n%s", view)
	}
	if !model.editor.Focused() {
		t.Fatal("editor lost focus without an approval owning the composer")
	}
	if len(backend.responses) != 1 || !strings.Contains(backend.responses[0].Metadata.Output, `"status":"ok"`) {
		t.Fatalf("tool response = %+v, want the tool to have run unprompted", backend.responses)
	}
}

func TestApprovalPanelResponsiveLayout(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			backend := &approvalBackend{t: t}
			tools, err := agent.NewToolSet(agent.ModeGated, newApprovalTool())
			if err != nil {
				t.Fatal(err)
			}
			model := New(agent.New(backend, assistant.SendOptions{}), Config{Tools: tools})
			model.resize(width, 24)
			setConversationInput(model, "Run the action")
			_, _ = model.submit()
			for len(model.pendingApprovals) == 0 {
				msg := runConversationCmd(t, waitEvent(model.turnGen, model.turnEvents))
				_, _ = model.Update(msg)
			}

			view := model.approvalView()
			plain := ansi.Strip(view)
			for _, want := range []string{"Permission Required", "ESC x", "Run the test action?", "This test tool requires", "Deny"} {
				if !strings.Contains(plain, want) {
					t.Errorf("approval panel missing %q:\n%s", want, plain)
				}
			}
			for lineNo, line := range strings.Split(view, "\n") {
				if got := ansi.StringWidth(line); got > width {
					t.Fatalf("line %d width = %d, want <= %d", lineNo+1, got, width)
				}
			}
			if width < approvalCompactWidth {
				if !strings.Contains(plain, "Once") || !strings.Contains(plain, "Session") || strings.Contains(plain, "Allow once") {
					t.Fatalf("compact actions not used:\n%s", plain)
				}
			} else if !strings.Contains(plain, "Allow once") || !strings.Contains(plain, "Allow for session") {
				t.Fatalf("full actions missing:\n%s", plain)
			}
			if width == 120 {
				firstLine := strings.Split(plain, "\n")[0]
				if got, want := strings.Index(firstLine, "╭"), (width-model.styles.Approval.Panel.MaxWidth)/2; got != want {
					t.Fatalf("wide panel left offset = %d, want %d:\n%s", got, want, plain)
				}
			}
		})
	}
}

func TestApprovalPanelRemainsUsableAtMinimumHeight(t *testing.T) {
	backend := &approvalBackend{t: t}
	tools, err := agent.NewToolSet(agent.ModeGated, newApprovalTool())
	if err != nil {
		t.Fatal(err)
	}
	model := New(agent.New(backend, assistant.SendOptions{}), Config{Tools: tools})
	model.resize(80, minimumApprovalHeight)
	setConversationInput(model, "Run the action")
	_, _ = model.submit()
	for len(model.pendingApprovals) == 0 {
		msg := runConversationCmd(t, waitEvent(model.turnGen, model.turnEvents))
		_, _ = model.Update(msg)
	}

	view := ansi.Strip(model.View().Content)
	for _, want := range []string{"Permission Required", "Run the test action?", "This test tool requires approval", "Deny", "Once", "Session"} {
		if !strings.Contains(view, want) {
			t.Errorf("minimum-height approval missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Resize terminal") {
		t.Fatalf("minimum supported height rendered resize fallback:\n%s", view)
	}
}

func TestTallDraftConcealsApprovalAndSuppressesInput(t *testing.T) {
	backend := &approvalBackend{t: t}
	tools, err := agent.NewToolSet(agent.ModeGated, newApprovalTool())
	if err != nil {
		t.Fatal(err)
	}
	model := New(agent.New(backend, assistant.SendOptions{}), Config{Tools: tools})
	model.resize(80, minimumApprovalHeight)
	setConversationInput(model, "Run the action")
	_, _ = model.submit()
	for len(model.pendingApprovals) == 0 {
		msg := runConversationCmd(t, waitEvent(model.turnGen, model.turnEvents))
		_, _ = model.Update(msg)
	}

	setConversationInput(model, strings.Repeat("draft\n", 12))
	model.layoutTranscript()
	if model.approvalAvailableHeight() >= minimumApprovalPanelHeight {
		t.Fatalf("approval height = %d, test did not force the concealed state", model.approvalAvailableHeight())
	}
	if !model.chatViewTooSmall() {
		t.Fatal("approval with a tall draft was not concealed")
	}
	if view := ansi.Strip(model.View().Content); !strings.Contains(view, "Resize terminal") {
		t.Fatalf("concealed approval did not render the resize hint:\n%s", view)
	}

	for _, code := range []rune{tea.KeyRight, tea.KeyEnter, tea.KeyEscape} {
		_, _ = model.Update(tea.KeyPressMsg{Code: code})
	}
	if len(model.pendingApprovals) == 0 {
		t.Fatal("a concealed keypress answered the approval")
	}
	if backend.calls != 1 {
		t.Fatalf("backend calls = %d while concealed, want 1", backend.calls)
	}
}

func TestToolApprovalComposerE2E(t *testing.T) {
	tests := []struct {
		name      string
		navigate  int
		deny      bool
		selection string
	}{
		{name: "deny", deny: true, selection: "Deny"},
		{name: "allow once", navigate: 1, selection: "Allow once"},
		{name: "allow for session", navigate: 2, selection: "Allow for session"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &approvalBackend{t: t}
			tools, err := agent.NewToolSet(agent.ModeGated, newApprovalTool())
			if err != nil {
				t.Fatal(err)
			}
			model := New(agent.New(backend, assistant.SendOptions{}), Config{Tools: tools})
			model.resize(80, 24)
			setConversationInput(model, "Run the action")
			_, _ = model.submit()

			for len(model.pendingApprovals) == 0 {
				msg := runConversationCmd(t, waitEvent(model.turnGen, model.turnEvents))
				_, _ = model.Update(msg)
			}
			for range tt.navigate {
				_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
			}

			approval := model.approvalView()
			view := ansi.Strip(model.View().Content)
			if !strings.Contains(view, "Permission Required") || !strings.Contains(view, "Run the test action?") || !strings.Contains(approval, model.styles.Approval.Selected.Render(tt.selection)) {
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

			if strings.Contains(ansi.Strip(model.View().Content), "Permission Required") {
				t.Fatal("approval composer remained after the decision")
			}
			if tt.deny {
				// The denial is answered on the wire; the follow-up answer never renders.
				if backend.calls != 2 || len(backend.responses) != 1 {
					t.Fatalf("backend calls = %d responses = %d, want the denial answered on the wire", backend.calls, len(backend.responses))
				}
				if backend.responses[0].Status != assistant.ToolStatusError || backend.responses[0].ToolCallID != "tool-call" {
					t.Fatalf("denial response = %+v", backend.responses[0])
				}
				if strings.Contains(ansi.Strip(model.View().Content), "Done.") {
					t.Fatal("aborted deny still delivered the model's follow-up answer")
				}
				return
			}
			if len(backend.responses) != 1 || !strings.Contains(backend.responses[0].Metadata.Output, `"status":"ok"`) {
				t.Fatalf("tool response = %+v", backend.responses)
			}
		})
	}
}
