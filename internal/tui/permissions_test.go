package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
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
	tools, err := agent.NewToolSet(mode, newApprovalTool())
	if err != nil {
		t.Fatal(err)
	}
	backend := &turnBackend{t: t}
	model := New(agent.New(backend, assistant.SendOptions{}), Config{Tools: tools})
	model.resize(80, 24)
	return model, backend
}

func TestPermissionsShowsCurrentMode(t *testing.T) {
	for _, mode := range []agent.PermissionsMode{agent.ModeManual, agent.ModeSkipPermissions} {
		t.Run(string(mode), func(t *testing.T) {
			m, _ := newPermissionsModel(t, mode)
			_, _ = m.dispatchCommand("permissions", "")
			if m.notice.Level != chat.NoticeInfo || !strings.Contains(m.notice.Text, string(mode)) {
				t.Fatalf("notice = %#v, want the current mode %q", m.notice, mode)
			}
			if got := m.tools.PermissionsMode(); got != mode {
				t.Fatalf("mode = %q, want it unchanged by the query", got)
			}
		})
	}
}

func TestPermissionsRejectsInvalidArgument(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "gated")
	if m.notice.Level != chat.NoticeError || !strings.Contains(m.notice.Text, "manual or skip-permissions") {
		t.Fatalf("notice = %#v, want the valid modes listed", m.notice)
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeManual {
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
			if m.notice.Level != chat.NoticeError || !strings.Contains(m.notice.Text, "manual or skip-permissions") {
				t.Fatalf("notice = %#v, want the valid modes listed", m.notice)
			}
			if got := m.tools.PermissionsMode(); got != tc.mode {
				t.Fatalf("mode = %q, want %q unchanged after a rejected switch", got, tc.mode)
			}
		})
	}
}

func TestPermissionsExtraSpacesAroundModeStillSwitch(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeSkipPermissions)
	setConversationInput(m, "/permissions   manual")
	_, _ = m.submit()
	if m.notice.Level != chat.NoticeInfo || !strings.Contains(m.notice.Text, "Tools will ask before running") {
		t.Fatalf("notice = %#v, want the manual switch notice", m.notice)
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual", got)
	}
}

func TestPermissionsSwitchingToCurrentModeIsANoOp(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "manual")
	if m.notice.Level != chat.NoticeInfo || !strings.Contains(m.notice.Text, "already manual") {
		t.Fatalf("notice = %#v, want an already-manual notice", m.notice)
	}
}

func TestPermissionsRejectedDuringActiveTurn(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	m.turnEvents = make(chan agent.Event)
	m.chatPhase = chat.PhaseStreaming
	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	if m.notice.Level != chat.NoticeWarn || m.notice.Empty() {
		t.Fatalf("notice = %#v, want an active-turn rejection", m.notice)
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual after a rejected switch", got)
	}
}

func TestPermissionsQueryAllowedDuringActiveTurn(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	m.turnEvents = make(chan agent.Event)
	m.chatPhase = chat.PhaseStreaming
	_, _ = m.dispatchCommand("permissions", "")
	if m.notice.Level != chat.NoticeInfo || !strings.Contains(m.notice.Text, "Permissions: manual (this session).") {
		t.Fatalf("notice = %#v, want the current-mode notice", m.notice)
	}
	if m.chatPhase != chat.PhaseStreaming || m.turnEvents == nil {
		t.Fatal("the query disturbed the active turn")
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual after the query", got)
	}
}

func TestPermissionsSwitchRejectedDuringActiveTurn(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	m.turnEvents = make(chan agent.Event)
	m.chatPhase = chat.PhaseStreaming
	_, _ = m.dispatchCommand("permissions", "manual")
	if m.notice.Level != chat.NoticeWarn || !strings.Contains(m.notice.Text, "Wait for the assistant response") {
		t.Fatalf("notice = %#v, want the active-turn rejection", m.notice)
	}
	if m.chatPhase != chat.PhaseStreaming {
		t.Fatal("the rejected switch disturbed the active turn")
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual after a rejected switch", got)
	}
}

func TestPermissionsRejectedWhileApprovalsPending(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	m.pendingApprovals = []agent.Block{{
		Kind: assistant.KindToolCall,
		Tool: &agent.ToolBlock{Status: agent.ToolAwaitingApproval},
	}}
	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	if m.notice.Level != chat.NoticeWarn || m.notice.Empty() {
		t.Fatalf("notice = %#v, want a pending-approval rejection", m.notice)
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual after a rejected switch", got)
	}
}

func TestPermissionsSwitchToSkipTakesEffectOnNextGatedTool(t *testing.T) {
	m, backend := newPermissionsModel(t, agent.ModeManual)
	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	for len(m.pendingApprovals) == 0 {
		msg := runConversationCmd(t, waitEvent(m.turnGen, m.turnEvents))
		_, _ = m.Update(msg)
	}
	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape}) // deny the panel
	drainConversationRemote(t, m)
	if backend.calls != 2 {
		t.Fatalf("backend calls = %d after the first turn", backend.calls)
	}

	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	if m.notice.Level != chat.NoticeWarn || !strings.Contains(m.notice.Text, "without asking") {
		t.Fatalf("notice = %#v, want the skip-permissions warning", m.notice)
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeSkipPermissions {
		t.Fatalf("mode = %q, want skip-permissions", got)
	}

	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	drainConversationRemote(t, m)
	if len(m.pendingApprovals) != 0 {
		t.Fatalf("skip-permissions still surfaced %d permission prompts", len(m.pendingApprovals))
	}
	if backend.calls != 4 {
		t.Fatalf("backend calls = %d, want a completed second turn", backend.calls)
	}
	if len(backend.responses) != 2 || len(backend.responses[1]) != 1 || !strings.Contains(backend.responses[1][0].Metadata.Output, `"status":"ok"`) {
		t.Fatalf("second-turn tool response = %+v, want the tool to have run unprompted", backend.responses)
	}
}

func TestPermissionsSwitchBackToManualPromptsAgain(t *testing.T) {
	m, backend := newPermissionsModel(t, agent.ModeSkipPermissions)
	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	drainConversationRemote(t, m)
	if len(m.pendingApprovals) != 0 {
		t.Fatalf("skip-permissions surfaced %d permission prompts", len(m.pendingApprovals))
	}

	_, _ = m.dispatchCommand("permissions", "manual")
	if m.notice.Level != chat.NoticeInfo || !strings.Contains(m.notice.Text, "Tools will ask before running") {
		t.Fatalf("notice = %#v, want the manual switch notice", m.notice)
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual", got)
	}

	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	for len(m.pendingApprovals) == 0 {
		msg := runConversationCmd(t, waitEvent(m.turnGen, m.turnEvents))
		_, _ = m.Update(msg)
	}
	if backend.calls != 3 {
		t.Fatalf("backend calls = %d, want the gated tool round", backend.calls)
	}
}

func TestPermissionsSwitchKeepsExistingSessionGrants(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	for len(m.pendingApprovals) == 0 {
		msg := runConversationCmd(t, waitEvent(m.turnGen, m.turnEvents))
		_, _ = m.Update(msg)
	}
	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyRight}) // select "Allow for session"
	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainConversationRemote(t, m)

	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	_, _ = m.dispatchCommand("permissions", "manual")
	if got := m.tools.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual", got)
	}

	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	drainConversationRemote(t, m)
	if len(m.pendingApprovals) != 0 {
		t.Fatalf("the earlier session grant was lost after the mode switches: %d prompts", len(m.pendingApprovals))
	}
}
