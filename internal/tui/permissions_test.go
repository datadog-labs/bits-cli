package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"charm.land/lipgloss/v2"
	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/charmbracelet/x/ansi"
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
			if m.mode != ModePermissions || m.permissionChoice != map[agent.PermissionsMode]int{agent.ModeManual: 0, agent.ModeSkipPermissions: 1}[mode] {
				t.Fatalf("picker = (%v, %d), want %q selected", m.mode, m.permissionChoice, mode)
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
	if m.notice.Level != chat.NoticeInfo || !strings.Contains(m.notice.Text, "Changes require an idle session") {
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
	if m.mode != ModePermissions || !m.permissionConfirm || m.tools.PermissionsMode() != agent.ModeManual {
		t.Fatal("skip-permissions switched before confirmation")
	}
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyRight})
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
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

func TestPermissionsSwitchBackToManualClearsExistingSessionGrants(t *testing.T) {
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
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyRight})
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	_, _ = m.dispatchCommand("permissions", "manual")
	if got := m.tools.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual", got)
	}

	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	for len(m.pendingApprovals) == 0 {
		msg := runConversationCmd(t, waitEvent(m.turnGen, m.turnEvents))
		_, _ = m.Update(msg)
	}
	if len(m.pendingApprovals) != 1 {
		t.Fatalf("earlier session grant survived the switch back to manual: %d prompts", len(m.pendingApprovals))
	}
}

func TestPermissionsPickerNavigationAndCancel(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "")
	if !strings.Contains(m.permissionsView(), "› Ask for Approval (current)") {
		t.Fatal("picker did not focus manual")
	}
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyDown})
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.permissionConfirm || m.tools.PermissionsMode() != agent.ModeManual {
		t.Fatal("picker did not require confirmation")
	}
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.mode != ModeChat || m.tools.PermissionsMode() != agent.ModeManual {
		t.Fatal("default cancel changed permissions")
	}
	_, _ = m.dispatchCommand("permissions", "")
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.mode != ModeChat || m.tools.PermissionsMode() != agent.ModeManual {
		t.Fatal("escape changed permissions")
	}
}

func TestPermissionsPickerCurrentModeAndResize(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeSkipPermissions)
	_, _ = m.dispatchCommand("permissions", "")
	if m.permissionChoice != 1 || !strings.Contains(m.permissionsView(), "› Full access (current)") {
		t.Fatal("picker did not focus the startup mode")
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 72, Height: 18})
	if m.permissionChoice != 1 || m.tools.PermissionsMode() != agent.ModeSkipPermissions {
		t.Fatal("resize changed the selected or active mode")
	}
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.mode != ModeChat || m.tools.PermissionsMode() != agent.ModeSkipPermissions {
		t.Fatal("selecting current mode did not close as a no-op")
	}
}

func TestPermissionsPickerOverlaysConversation(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	setConversationInput(m, "draft message remains visible")
	_, _ = m.dispatchCommand("permissions", "")
	popup := m.permissionsView()
	if lipgloss.Width(popup) >= m.width || lipgloss.Height(popup) >= m.height {
		t.Fatalf("picker fills the terminal: %d×%d in %d×%d", lipgloss.Width(popup), lipgloss.Height(popup), m.width, m.height)
	}
	if !strings.Contains(ansi.Strip(popup), "ESC x") {
		t.Fatal("picker lacks top-right Escape hint")
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "draft message remains visible") || !strings.Contains(view, "Permissions") {
		t.Fatalf("picker replaced the conversation view: %q", view)
	}
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := m.editor.Value(); got != "draft message remains visible" {
		t.Fatalf("Escape lost composer input: %q", got)
	}
}

func TestPermissionsPickerSectionSpacing(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeSkipPermissions)
	_, _ = m.dispatchCommand("permissions", "")
	rows := strings.Split(ansi.Strip(m.permissionsView()), "\n")
	find := func(label string) int {
		for i, row := range rows {
			if strings.Contains(row, label) {
				return i
			}
		}
		t.Fatalf("missing %q from picker: %q", label, ansi.Strip(m.permissionsView()))
		return -1
	}
	for _, pair := range [][2]string{
		{"Permissions", "Ask for Approval"},
		{"workspace", "Full access"},
	} {
		start, end := find(pair[0]), find(pair[1])
		if end-start != 2 || strings.TrimSpace(strings.Trim(rows[start+1], "│")) != "" {
			t.Fatalf("expected one blank row between %q and %q", pair[0], pair[1])
		}
	}
	if strings.Contains(ansi.Strip(m.permissionsView()), "to choose") {
		t.Fatal("picker still renders keyboard hints")
	}
}

func TestPermissionsPickerUsesResumeRowStyles(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeSkipPermissions)
	_, _ = m.dispatchCommand("permissions", "")
	view := m.permissionsView()
	if !strings.Contains(view, m.styles.Panel.Title.Render("Permissions")) {
		t.Fatal("picker title does not use the resume title style")
	}
	if !strings.Contains(view, m.styles.Selector.Selected.Render("› Full access (current)")) {
		t.Fatal("selected mode does not use the resume selected title style")
	}
	inner := min(100, m.width-m.editor.ContentOffset()) - m.styles.Editor.MenuFrame.GetHorizontalFrameSize() - 2
	labels := m.permissionOptionLabels()
	leftWidth := max(ansi.StringWidth(labels[0]), ansi.StringWidth(labels[1])) + 2
	detailWidth := inner - leftWidth - 2
	selectedDetail := wrapPermissionDetail(permissionOptionDetails[1], detailWidth)[0]
	if !strings.Contains(view, m.styles.Selector.Selected.Render(selectedDetail)) {
		t.Fatal("selected explanation does not use the resume selected detail style")
	}
	unselectedDetail := wrapPermissionDetail(permissionOptionDetails[0], detailWidth)[0]
	if !strings.Contains(view, m.styles.Text.Tertiary.Render(unselectedDetail)) {
		t.Fatal("unselected explanation does not use the resume time style")
	}
	rows := strings.Split(ansi.Strip(view), "\n")
	for _, pair := range [][2]string{{"Ask for Approval", unselectedDetail}, {"Full access", selectedDetail}} {
		found := false
		for _, row := range rows {
			found = found || strings.Contains(row, pair[0]) && strings.Contains(row, pair[1])
		}
		if !found {
			t.Fatalf("%q explanation does not start on the same row", pair[0])
		}
	}
}

func TestPermissionsConfirmationHasNoFooterHints(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	rows := strings.Split(ansi.Strip(m.permissionsView()), "\n")
	if strings.Contains(strings.Join(rows, "\n"), "Enter to select") {
		t.Fatal("confirmation still renders footer hints")
	}
	if !strings.Contains(rows[1], "Full access?") || strings.TrimSpace(strings.Trim(rows[2], "│")) != "" || !strings.Contains(rows[3], "Tools will run") {
		t.Fatalf("confirmation title needs a blank row before its explanation: %q", rows)
	}
	confirm, cancel := -1, -1
	for i, row := range rows {
		if strings.Contains(row, "Yes, enable full access") {
			confirm = i
		}
		if strings.Contains(row, "Cancel") {
			cancel = i
		}
	}
	if confirm < 0 || cancel != confirm+1 || !strings.Contains(rows[cancel], "› Cancel") {
		t.Fatalf("confirmation actions are not ordered with Cancel selected: %q", rows)
	}
}

func TestPermissionsPickerCompactResizeKeepsSelection(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "")
	_, _ = m.Update(tea.WindowSizeMsg{Width: 30, Height: 10})
	if !strings.Contains(ansi.Strip(m.View().Content), "Resize") {
		t.Fatal("compact picker lacks resize hint")
	}
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.mode != ModePermissions || m.tools.PermissionsMode() != agent.ModeManual {
		t.Fatal("hidden controls accepted Enter")
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.permissionChoice != 1 || !strings.Contains(ansi.Strip(m.View().Content), "Full access") {
		t.Fatal("picker did not recover after resize")
	}
}

func TestPermissionsConfirmationCannotBypassNewActiveWork(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	m.turnEvents = make(chan agent.Event)
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyRight})
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.tools.PermissionsMode() != agent.ModeManual || m.mode != ModeChat {
		t.Fatal("stale confirmation changed mode during active work")
	}
}
