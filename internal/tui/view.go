package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

const (
	minimumChatWidth      = 12
	minimumChatHeight     = 5
	minimumApprovalWidth  = 32
	minimumApprovalHeight = 9
)

// View lays out the transcript viewport, a status line, and the input. Alt-screen
// and mouse tracking, which were program options in Bubble Tea v1, are now
// declared on the returned view.
func (m *Model) View() tea.View {
	if m.mode == ModeLogin && m.loginModel != nil {
		return m.loginModel.View()
	}
	v := tea.View{AltScreen: true, MouseMode: tea.MouseModeCellMotion}
	switch m.mode {
	case ModeTermInit:
		v.Content = "loading…"
	case ModeChat:
		approvalTooSmall := len(m.pendingApprovals) > 0 &&
			(m.width < minimumApprovalWidth || m.height < minimumApprovalHeight)
		if m.width < minimumChatWidth || m.height < minimumChatHeight || approvalTooSmall {
			v.MouseMode = tea.MouseModeNone
			message := ansi.Truncate("Resize terminal to use Bits", max(1, m.width), "")
			v.Content = lipgloss.Place(max(1, m.width), max(1, m.height), lipgloss.Center, lipgloss.Center, message)
			break
		}
		v.Content = m.chatView()
	case ModeLogin:
		// A valid login model returns above. Keep a bounded fallback for the
		// defensive nil-model shutdown path rather than flashing a blank frame.
		v.MouseMode = tea.MouseModeNone
		v.Content = "loading…"
	case ModeConversations:
		if m.picker != nil {
			v.Content = m.picker.View()
		}
	}
	return v
}

// chatView keeps the transcript stable while the composer switches between
// normal input and tool approval.
func (m *Model) chatView() string {
	composer := m.approvalView()
	if composer == "" {
		m.editor.SetPlaceholder(m.promptPlaceholder())
		composer = m.editor.View()
	}
	base := strings.Join([]string{
		m.list.Render(),
		m.noticeBar(),
		composer,
	}, "\n")

	if len(m.pendingApprovals) > 0 {
		return base
	}
	menu := m.editor.MenuView()
	if menu == "" {
		return base
	}

	// Float the completion menu as a fixed overlay just above the input. The
	// editor's Height excludes the menu, so opening it covers the transcript's
	// bottom rows without reflowing the layout.
	menuW, menuH := lipgloss.Width(menu), lipgloss.Height(menu)
	x := m.editor.ContentOffset()
	if width := m.list.Width(); x+menuW > width {
		x = max(0, width-menuW)
	}
	y := max(0, m.height-m.editor.Height()-menuH)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(menu).X(x).Y(y).Z(1),
	).Render()
}

func (m *Model) approvalView() string {
	if len(m.pendingApprovals) == 0 || m.pendingApprovals[0].Tool == nil {
		return ""
	}

	block := m.pendingApprovals[0]
	prompt := block.Tool.Approval
	title := "Run " + block.Tool.Name + "?"
	detail := ""
	if prompt != nil {
		if prompt.Title != "" {
			title = prompt.Title
		}
		detail = prompt.Detail
	}

	frame := m.styles.Panel.Frame.Padding(0, 1)
	width := max(1, m.list.Width()-frame.GetHorizontalFrameSize())
	queue := "Approval required"
	if count := len(m.pendingApprovals); count > 1 {
		queue += " · " + strconv.Itoa(count) + " waiting"
	}
	lines := []string{
		m.styles.Panel.Title.Bold(true).Render(queue),
		m.styles.Text.Body.Render(ansi.Truncate(title, width, "…")),
	}
	if detail != "" {
		lines = append(lines, m.styles.Text.Muted.Render(ansi.Truncate(detail, width, "…")))
	}

	labels := [...]string{"Deny", "Allow once", "Allow for session"}
	if width < 48 {
		labels = [...]string{"Deny", "Once", "Session"}
	}
	actions := make([]string, len(labels))
	for i, label := range labels {
		text := "  " + label + "  "
		if i == m.approvalChoice {
			text = "[ " + label + " ]"
			actions[i] = m.styles.Selector.Selected.Render(text)
		} else {
			actions[i] = m.styles.Selector.Item.Render(text)
		}
	}
	lines = append(lines,
		ansi.Truncate(strings.Join(actions, " "), width, ""),
		m.styles.Text.Help.Render(ansi.Truncate("←/→ choose · Enter confirm · Esc deny", width, "")),
	)
	return frame.Width(m.list.Width()).Render(strings.Join(lines, "\n"))
}

func (m *Model) composerHeight() int {
	if approval := m.approvalView(); approval != "" {
		return lipgloss.Height(approval)
	}
	return m.editor.Height()
}

// promptPlaceholder returns the current editor prompt placeholder.
func (m *Model) promptPlaceholder() string {
	switch m.chatPhase {
	case chat.PhaseLoading:
		return "Loading…"
	case chat.PhaseWaiting, chat.PhaseStreaming:
		return "Working on it…"
	case chat.PhaseError:
		return "Error"
	default:
		return "Ask Bits…"
	}
}

// noticeBar renders the transient notification bar between the transcript and
// the input.
func (m *Model) noticeBar() string {
	if m.notice.Empty() {
		return ""
	}
	width := max(1, m.list.Width())
	text := m.notice.Text
	if err := m.notice.Err; err != nil {
		if detail := err.Error(); detail != "" && detail != m.notice.Text {
			text += " (" + detail + ")"
		}
	}
	text = ansi.Truncate(text, max(1, width-2), "…")
	return m.chatStyles.Notice(m.notice.Level).Width(width).Render(text)
}
