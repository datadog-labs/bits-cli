package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

// turnBackend scripts an alternating conversation: odd sends request the
// gated test tool, even sends acknowledge the tool batch and answer.
type turnBackend struct {
	t         *testing.T
	calls     int
	responses [][]assistant.ClientToolResponse
}

func (b *turnBackend) Send(_ context.Context, message any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.t.Helper()
	b.calls++
	if b.calls%2 == 1 {
		content := assistant.ToolCallContent("tool-call", approvalToolName, `{}`)
		content.Type = assistant.ContentClientToolCall
		var response assistant.AssistantResponse
		response.Data.Attributes.ConversationID = "conversation-1"
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("tool-message", content)
		return "conversation-1", emit(response)
	}
	batch, ok := message.([]assistant.ClientToolResponse)
	if !ok {
		b.t.Fatalf("tool follow-up has type %T", message)
	}
	b.responses = append(b.responses, batch)
	var response assistant.AssistantResponse
	response.Data.Attributes.ConversationID = "conversation-1"
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("Done."))
	return "conversation-1", emit(response)
}

func newPermissionsModel(t *testing.T, mode agent.PermissionsMode) (*Model, *turnBackend) {
	t.Helper()
	tools, err := agent.NewToolSet(newApprovalTool())
	if err != nil {
		t.Fatal(err)
	}
	backend := &turnBackend{t: t}
	model := New(agent.New(backend, agent.Config{PermissionsMode: mode}), Config{Tools: tools})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return model, backend
}

// press sends keys through Update, as the terminal does.
func press(m *Model, codes ...rune) {
	for _, code := range codes {
		_, _ = m.Update(tea.KeyPressMsg{Code: code})
	}
}

// screen lays out the model, as Update does, and returns the visible text.
func screen(m *Model) string {
	m.relayout()
	return ansi.Strip(m.View().Content)
}

func TestPermissionsShowsCurrentMode(t *testing.T) {
	for mode, row := range map[agent.PermissionsMode]string{
		agent.ModeManual:          "› Ask for Approval (current)",
		agent.ModeSkipPermissions: "› Full Access (current)",
	} {
		t.Run(string(mode), func(t *testing.T) {
			m, _ := newPermissionsModel(t, mode)
			_, _ = m.dispatchCommand("permissions", "")
			if view := screen(m); !strings.Contains(view, row) || !strings.Contains(view, "Permission changes take effect immediately.") {
				t.Fatalf("picker does not highlight %q:\n%s", row, view)
			}
			if got := m.engine.PermissionsMode(); got != mode {
				t.Fatalf("mode = %q, want it unchanged by the query", got)
			}
		})
	}
}

func TestPermissionsRejectsInvalidArgument(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "gated")
	if latestNotice(m).Level != chat.NoticeError || !strings.Contains(latestNotice(m).Text, "manual or skip-permissions") {
		t.Fatalf("notice = %#v, want the valid modes listed", latestNotice(m))
	}
	if got := m.engine.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual after a rejected switch", got)
	}
}

func TestPermissionsRejectsTrailingArguments(t *testing.T) {
	for _, tc := range []struct {
		argument string
		mode     agent.PermissionsMode
	}{
		{argument: "manual extra", mode: agent.ModeManual},
		{argument: "skip-permissions extra", mode: agent.ModeSkipPermissions},
	} {
		t.Run(tc.argument, func(t *testing.T) {
			m, _ := newPermissionsModel(t, tc.mode)
			_, _ = m.dispatchCommand("permissions", tc.argument)
			if latestNotice(m).Level != chat.NoticeError || !strings.Contains(latestNotice(m).Text, "manual or skip-permissions") {
				t.Fatalf("notice = %#v, want the valid modes listed", latestNotice(m))
			}
			if got := m.engine.PermissionsMode(); got != tc.mode {
				t.Fatalf("mode = %q, want %q unchanged after a rejected switch", got, tc.mode)
			}
		})
	}
}

func TestPermissionsExtraSpacesAroundModeStillSwitch(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeSkipPermissions)
	setConversationInput(m, "/permissions   manual")
	_, _ = m.submit()
	if latestNotice(m).Level != chat.NoticeInfo || latestNotice(m).Text != "Permissions set to Ask for Approval." {
		t.Fatalf("notice = %#v, want the permissions change announced", latestNotice(m))
	}
	if got := m.engine.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual", got)
	}
}

func TestPermissionsSwitchingToCurrentModeIsANoOp(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "manual")
	if !latestNotice(m).Empty() {
		t.Fatalf("notice = %#v, want no notification for the current mode", latestNotice(m))
	}
}

func TestPermissionsAppliesDuringActiveTurn(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	m.op = operation{kind: opTurn, run: 1}
	m.chatPhase = chat.PhaseStreaming
	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	if !strings.Contains(screen(m), "Yes, enable full access") {
		t.Fatal("full access did not require confirmation during the turn")
	}
	press(m, tea.KeyEnter)
	if m.commandPrompt != nil || m.engine.PermissionsMode() != agent.ModeSkipPermissions {
		t.Fatal("confirmed mode did not apply during the active turn")
	}
	if latestNotice(m).Text != "Full Access enabled. Actions will run without approval." {
		t.Fatalf("notice = %#v, want the switch announced at once", latestNotice(m))
	}
	if m.chatPhase != chat.PhaseStreaming || m.op.run == 0 {
		t.Fatal("switching the mode disturbed the active turn")
	}
	_, _ = m.dispatchCommand("permissions", "")
	if !strings.Contains(screen(m), "› Full Access (current)") {
		t.Fatal("picker did not mark the applied mode as current")
	}
}

// TestPermissionsSwitchToSkipReleasesPendingApproval switches to full access
// from /permissions while a call waits for approval: the confirmation shows
// over the approval, the engine re-gates the call, so it runs without an
// answer and the turn completes.
func TestPermissionsSwitchToSkipReleasesPendingApproval(t *testing.T) {
	m, backend := newPermissionsModel(t, agent.ModeManual)
	awaitApproval(t, m, 80)
	approval := m.prompt()
	_, _ = m.dispatchCommand("permissions", "")
	press(m, tea.KeyEscape)
	if m.prompt() != approval {
		t.Fatal("dismissing the picker did not return to the approval")
	}
	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	if !strings.Contains(screen(m), "Enabling full access") || m.engine.PermissionsMode() != agent.ModeManual {
		t.Fatal("confirmation is hidden while an approval is pending, or skipped")
	}
	press(m, tea.KeyEnter)
	if got := m.engine.PermissionsMode(); got != agent.ModeSkipPermissions {
		t.Fatalf("mode = %q, want full access applied mid-turn", got)
	}

	drainConversationRemote(t, m)
	if waitingApprovals(m) != 0 {
		t.Fatalf("%d approvals still waiting after full access", waitingApprovals(m))
	}
	if backend.calls != 2 || len(backend.responses) != 1 || !strings.Contains(backend.responses[0][0].Metadata.Output, `"status":"ok"`) {
		t.Fatalf("tool responses = %+v, want the parked call to have run unprompted", backend.responses)
	}
}

// TestPermissionsSwitchBackToManualPromptsAgain returns to manual mid-turn:
// the switch applies at once, and the next gated call asks.
func TestPermissionsSwitchBackToManualPromptsAgain(t *testing.T) {
	m, backend := newPermissionsModel(t, agent.ModeSkipPermissions)
	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	drainConversationRemote(t, m)
	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	_, _ = m.dispatchCommand("permissions", "manual")
	if got := m.engine.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual applied mid-turn", got)
	}
	if latestNotice(m).Level != chat.NoticeInfo || latestNotice(m).Text != "Permissions set to Ask for Approval." {
		t.Fatalf("notice = %#v, want the switch announced at once", latestNotice(m))
	}
	// The second turn's call may already have run under full access; if not,
	// it asks now. Either way the turn after that asks.
	for m.op.run != 0 && waitingApprovals(m) == 0 {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
	if waitingApprovals(m) == 0 {
		setConversationInput(m, "Run the action")
		_, _ = m.submit()
		awaitPendingApproval(t, m)
	}
	if backend.calls < 3 {
		t.Fatalf("backend calls = %d, want the gated tool round", backend.calls)
	}
}

func awaitPendingApproval(t *testing.T, m *Model) {
	t.Helper()
	for waitingApprovals(m) == 0 {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
}

func TestPermissionsSwitchBackToManualClearsExistingSessionGrants(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	for waitingApprovals(m) == 0 {
		msg := runConversationCmd(t, waitEvent(m.events))
		_, _ = m.Update(msg)
	}
	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyRight}) // select "Allow for session"
	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainConversationRemote(t, m)

	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	press(m, tea.KeyEnter)
	_, _ = m.dispatchCommand("permissions", "manual")
	if got := m.engine.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual", got)
	}

	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	for waitingApprovals(m) == 0 {
		msg := runConversationCmd(t, waitEvent(m.events))
		_, _ = m.Update(msg)
	}
	if waitingApprovals(m) != 1 {
		t.Fatalf("earlier session grant survived the switch back to manual: %d prompts", waitingApprovals(m))
	}
}

func TestPermissionsPickerNavigationAndCancel(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	setConversationInput(m, "draft message")
	_, _ = m.dispatchCommand("permissions", "")
	press(m, tea.KeyDown, tea.KeyEnter)
	if view := screen(m); !strings.Contains(view, "Enabling full access") || m.engine.PermissionsMode() != agent.ModeManual {
		t.Fatalf("picker did not require confirmation:\n%s", view)
	}
	press(m, tea.KeyDown, tea.KeyEnter)
	if m.commandPrompt != nil || m.engine.PermissionsMode() != agent.ModeManual {
		t.Fatal("cancel changed permissions")
	}
	_, _ = m.dispatchCommand("permissions", "")
	press(m, tea.KeyEscape)
	if m.commandPrompt != nil || m.engine.PermissionsMode() != agent.ModeManual {
		t.Fatal("escape changed permissions")
	}
	if got := m.editor.Value(); got != "draft message" {
		t.Fatalf("the picker lost the draft: %q", got)
	}
}

func TestPermissionsPickerCurrentModeAndResize(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeSkipPermissions)
	_, _ = m.dispatchCommand("permissions", "")
	_, _ = m.Update(tea.WindowSizeMsg{Width: 72, Height: 18})
	if !strings.Contains(screen(m), "› Full Access (current)") || m.engine.PermissionsMode() != agent.ModeSkipPermissions {
		t.Fatal("resize changed the selected or active mode")
	}
	press(m, tea.KeyEnter)
	if m.commandPrompt != nil || m.engine.PermissionsMode() != agent.ModeSkipPermissions {
		t.Fatal("selecting current mode did not close as a no-op")
	}
}

func TestPermissionsPickerCompactResizeKeepsSelection(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "")
	for _, width := range []int{30, minimumChatWidth} {
		_, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: 10})
		if !strings.Contains(screen(m), "Resize") {
			t.Fatalf("compact picker lacks resize hint at width %d", width)
		}
	}
	press(m, tea.KeyEnter)
	if m.commandPrompt == nil || m.engine.PermissionsMode() != agent.ModeManual {
		t.Fatal("hidden controls accepted Enter")
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	press(m, tea.KeyDown)
	if !strings.Contains(screen(m), "› Full Access") {
		t.Fatal("picker did not recover after resize")
	}
}

func TestPermissionsConfirmationAppliesDuringNewActiveWork(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	m.op = operation{kind: opTurn, run: 1}
	press(m, tea.KeyEnter)
	if m.engine.PermissionsMode() != agent.ModeSkipPermissions || m.commandPrompt != nil {
		t.Fatal("confirmation did not apply the mode during the new active turn")
	}
}
