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
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return model, backend
}

func TestPermissionsShowsCurrentMode(t *testing.T) {
	for _, mode := range []agent.PermissionsMode{agent.ModeManual, agent.ModeSkipPermissions} {
		t.Run(string(mode), func(t *testing.T) {
			m, _ := newPermissionsModel(t, mode)
			_, _ = m.dispatchCommand("permissions", "")
			if m.mode != ModePermissions || m.permissionCursor != map[agent.PermissionsMode]int{agent.ModeManual: 0, agent.ModeSkipPermissions: 1}[mode] {
				t.Fatalf("picker = (%v, %d), want %q selected", m.mode, m.permissionCursor, mode)
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
	if !m.notice.Empty() {
		t.Fatalf("notice = %#v, want no success notification", m.notice)
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual", got)
	}
}

func TestPermissionsSwitchingToCurrentModeIsANoOp(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "manual")
	if !m.notice.Empty() {
		t.Fatalf("notice = %#v, want no notification for the current mode", m.notice)
	}
}

func TestPermissionsQueuesDuringActiveTurn(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	m.chatPhase = chat.PhaseStreaming
	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	if m.mode != ModePermissions || !m.permissionConfirm {
		t.Fatal("full access did not require confirmation during the turn")
	}
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.pendingPermissions != agent.ModeSkipPermissions || m.tools.PermissionsMode() != agent.ModeManual {
		t.Fatal("mode changed before the active turn closed")
	}
	if strings.Contains(ansi.Strip(m.chatFooter()), "Full Access after this response") || !m.notice.Empty() {
		t.Fatal("queuing a mode changed the footer or showed a notification")
	}
	_, _ = m.dispatchCommand("permissions", "")
	if m.permissionCursor != 1 || strings.Contains(ansi.Strip(m.permissionsView()), "(queued)") || !strings.Contains(ansi.Strip(m.permissionsView()), "› Full Access (current)") {
		t.Fatal("picker did not mark the selected choice as current")
	}
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.mode != ModeChat || m.pendingPermissions != agent.ModeSkipPermissions {
		t.Fatal("reselecting the queued choice changed it")
	}
	_, _ = m.handleTurnClosed(turnClosedMsg{generation: m.op.gen})
	if m.pendingPermissions != "" || m.tools.PermissionsMode() != agent.ModeSkipPermissions {
		t.Fatal("queued mode did not apply when the turn closed")
	}
}

func TestPermissionsPickerOpensDuringActiveTurn(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	m.chatPhase = chat.PhaseStreaming
	_, _ = m.dispatchCommand("permissions", "")
	if m.mode != ModePermissions || !strings.Contains(ansi.Strip(m.permissionsView()), "Permission changes take effect on the next turn.") {
		t.Fatal("picker did not explain when changes take effect")
	}
	if m.chatPhase != chat.PhaseStreaming || m.op.events == nil {
		t.Fatal("opening the picker disturbed the active turn")
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual while the picker is open", got)
	}
}

func TestPermissionsCurrentModeCancelsQueuedChange(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	m.chatPhase = chat.PhaseStreaming
	m.pendingPermissions = agent.ModeSkipPermissions
	_, _ = m.dispatchCommand("permissions", "manual")
	if m.pendingPermissions != "" || !m.notice.Empty() {
		t.Fatal("selecting the current mode did not cancel the queued change")
	}
	if m.chatPhase != chat.PhaseStreaming {
		t.Fatal("cancelling the queued change disturbed the active turn")
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual", got)
	}
}

func TestPermissionsQueuesWhileApprovalsPending(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	// Approvals only arrive inside a running operation.
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	m.pendingApprovals = []agent.Block{{
		Kind: assistant.KindToolCall,
		Tool: &agent.ToolBlock{Status: agent.ToolAwaitingApproval},
	}}
	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	if m.mode != ModePermissions || !m.permissionConfirm {
		t.Fatal("pending approval prevented the confirmation from opening")
	}
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.pendingPermissions != agent.ModeSkipPermissions || m.tools.PermissionsMode() != agent.ModeManual || len(m.pendingApprovals) != 1 {
		t.Fatal("queued change altered the current approval")
	}
}

func TestPermissionsSwitchToSkipTakesEffectOnNextGatedTool(t *testing.T) {
	m, backend := newPermissionsModel(t, agent.ModeManual)
	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	for len(m.pendingApprovals) == 0 {
		msg := runConversationCmd(t, waitEvent(m.op.gen, m.op.events))
		_, _ = m.Update(msg)
	}
	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	if m.mode != ModePermissions || !m.permissionConfirm || m.tools.PermissionsMode() != agent.ModeManual {
		t.Fatal("skip-permissions switched before confirmation")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "Full Access") {
		t.Fatal("confirmation is hidden while an approval is pending")
	}
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.notice.Empty() {
		t.Fatalf("notice = %#v, want no success notification", m.notice)
	}
	if m.pendingPermissions != agent.ModeSkipPermissions || m.tools.PermissionsMode() != agent.ModeManual || len(m.pendingApprovals) != 1 {
		t.Fatal("current tool approval changed before the turn ended")
	}
	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape}) // deny the current tool
	drainConversationRemote(t, m)
	if backend.calls != 2 {
		t.Fatalf("backend calls = %d after the first turn", backend.calls)
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeSkipPermissions || m.pendingPermissions != "" {
		t.Fatalf("mode = %q, queued = %q; want full access applied after the turn", got, m.pendingPermissions)
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
	_, _ = m.dispatchCommand("permissions", "manual")
	if m.pendingPermissions != agent.ModeManual || m.tools.PermissionsMode() != agent.ModeSkipPermissions {
		t.Fatal("manual mode did not queue during the active turn")
	}
	drainConversationRemote(t, m)
	if len(m.pendingApprovals) != 0 {
		t.Fatalf("skip-permissions surfaced %d permission prompts", len(m.pendingApprovals))
	}
	if !m.notice.Empty() {
		t.Fatalf("notice = %#v, want no success notification", m.notice)
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeManual || m.pendingPermissions != "" {
		t.Fatalf("mode = %q, queued = %q; want manual after the first turn", got, m.pendingPermissions)
	}

	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	for len(m.pendingApprovals) == 0 {
		msg := runConversationCmd(t, waitEvent(m.op.gen, m.op.events))
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
		msg := runConversationCmd(t, waitEvent(m.op.gen, m.op.events))
		_, _ = m.Update(msg)
	}
	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyRight}) // select "Allow for session"
	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainConversationRemote(t, m)

	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	_, _ = m.dispatchCommand("permissions", "manual")
	if got := m.tools.PermissionsMode(); got != agent.ModeManual {
		t.Fatalf("mode = %q, want manual", got)
	}

	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	for len(m.pendingApprovals) == 0 {
		msg := runConversationCmd(t, waitEvent(m.op.gen, m.op.events))
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
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyRight})
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.mode != ModeChat || m.tools.PermissionsMode() != agent.ModeManual {
		t.Fatal("cancel changed permissions")
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
	if m.permissionCursor != 1 || !strings.Contains(m.permissionsView(), "› Full Access (current)") {
		t.Fatal("picker did not focus the startup mode")
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 72, Height: 18})
	if m.permissionCursor != 1 || m.tools.PermissionsMode() != agent.ModeSkipPermissions {
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
	if !strings.Contains(view, "draft message remains visible") || !strings.Contains(view, "Manage Bits Permissions") {
		t.Fatalf("picker replaced the conversation view: %q", view)
	}
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := m.editor.Value(); got != "draft message remains visible" {
		t.Fatalf("Escape lost composer input: %q", got)
	}
}

// panelRowText strips a rendered row down to its copy, discarding the panel's
// left indent and border columns.
func panelRowText(row string) string { return strings.Trim(row, " │") }

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
	title := find("Manage Bits Permissions")
	if panelRowText(rows[title-1]) != "" {
		t.Fatalf("title needs one blank row of padding above it: %q", rows)
	}
	if note := find("Permission changes take effect on the next turn."); note != title+1 {
		t.Fatalf("timing note should sit directly below the title: %q", rows)
	}
	for _, pair := range [][2]string{{"Permission changes take effect on the next turn.", "Ask for Approval"}, {"workspace", "Full Access"}} {
		start, end := find(pair[0]), find(pair[1])
		if end-start != 2 || panelRowText(rows[start+1]) != "" {
			t.Fatalf("expected one blank row between %q and %q", pair[0], pair[1])
		}
	}
	if strings.Contains(ansi.Strip(m.permissionsView()), "to choose") {
		t.Fatal("picker still renders keyboard hints")
	}
}

func TestPermissionsPickerSpacingIsStableAcrossCurrentModes(t *testing.T) {
	type layout struct{ firstRow, secondRow, firstDetailColumn, secondDetailColumn, height int }
	measure := func(mode agent.PermissionsMode) layout {
		m, _ := newPermissionsModel(t, mode)
		_, _ = m.dispatchCommand("permissions", "")
		rows := strings.Split(ansi.Strip(m.permissionsView()), "\n")
		result := layout{height: len(rows)}
		for i, row := range rows {
			if strings.Contains(row, "Ask for Approval") {
				result.firstRow = i
				result.firstDetailColumn = -1
				if start := strings.Index(row, "Bits will ask"); start >= 0 {
					result.firstDetailColumn = ansi.StringWidth(row[:start])
				}
			}
			if strings.Contains(row, "Full Access") {
				result.secondRow = i
				result.secondDetailColumn = -1
				if start := strings.Index(row, "Use with caution"); start >= 0 {
					result.secondDetailColumn = ansi.StringWidth(row[:start])
				}
			}
		}
		if result.firstDetailColumn < 0 || result.secondDetailColumn < 0 {
			t.Fatalf("missing option details for %s: %q", mode, rows)
		}
		return result
	}
	manual, fullAccess := measure(agent.ModeManual), measure(agent.ModeSkipPermissions)
	if manual != fullAccess {
		t.Fatalf("picker layout changes with current mode: manual=%+v full access=%+v", manual, fullAccess)
	}
}

func TestPermissionsConfirmationHasNoFooterHints(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	rows := strings.Split(ansi.Strip(m.permissionsView()), "\n")
	if strings.Contains(strings.Join(rows, "\n"), "Enter to select") {
		t.Fatal("confirmation still renders footer hints")
	}
	if panelRowText(rows[1]) != "" || !strings.Contains(rows[2], "Full Access") || strings.Contains(rows[2], "Full Access?") || !strings.Contains(rows[3], "Enabling full access") {
		t.Fatalf("confirmation needs one blank row of padding above the title and its explanation directly below: %q", rows)
	}
	explanation := panelRowText(rows[3]) + " " + panelRowText(rows[4])
	if explanation != "Enabling full access will automatically approve all actions without requiring confirmation." {
		t.Fatalf("confirmation explanation = %q", explanation)
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
	if confirm < 0 || cancel != confirm+2 || panelRowText(rows[confirm+1]) != "" || !strings.Contains(rows[confirm], "› Yes, enable full access") {
		t.Fatalf("confirmation actions need one blank row with Yes selected: %q", rows)
	}
}

func TestPermissionsPickerCompactResizeKeepsSelection(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "")
	for _, width := range []int{30, minimumChatWidth} {
		_, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: 10})
		if !strings.Contains(ansi.Strip(m.View().Content), "Resize") {
			t.Fatalf("compact picker lacks resize hint at width %d", width)
		}
	}
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.mode != ModePermissions || m.tools.PermissionsMode() != agent.ModeManual {
		t.Fatal("hidden controls accepted Enter")
	}
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.permissionCursor != 1 || !strings.Contains(ansi.Strip(m.View().Content), "Full Access") {
		t.Fatal("picker did not recover after resize")
	}
}

func TestPermissionsConfirmationCannotBypassNewActiveWork(t *testing.T) {
	m, _ := newPermissionsModel(t, agent.ModeManual)
	_, _ = m.dispatchCommand("permissions", "skip-permissions")
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	_ = m.updatePermissionsKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.tools.PermissionsMode() != agent.ModeManual || m.pendingPermissions != agent.ModeSkipPermissions || m.mode != ModeChat {
		t.Fatal("stale confirmation did not queue the mode for the active turn")
	}
}
