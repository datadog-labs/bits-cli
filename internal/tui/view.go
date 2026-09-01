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
	minimumApprovalWidth  = 36
	minimumApprovalHeight = 12

	// approvalCompactWidth is the terminal width below which the approval block
	// switches to condensed action labels so the choice row still fits.
	approvalCompactWidth = 50
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
		if m.chatViewTooSmall() {
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
	case ModeStatus:
		if m.status != nil {
			v.Content = m.status.View()
		}
	}
	return v
}

// chatView stacks the transcript, an optional docked approval block, the notice
// bar, and the editor. The approval block sits above the notice bar and editor
// and pushes the transcript up instead of covering it; input is routed to the
// approval while one is pending, so the editor stays visible but inert.
func (m *Model) chatView() string {
	m.editor.SetPlaceholder(m.promptPlaceholder())
	sections := []string{m.list.Render()}
	if approval := m.approvalView(); approval != "" {
		sections = append(sections, approval)
	}
	sections = append(sections, m.noticeBar(), m.editor.View())
	base := strings.Join(sections, "\n")

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

// chatViewTooSmall reports whether the chat cannot be usably rendered, so View
// replaces the whole screen with a resize hint. In that state the UI is
// invisible, so Update suppresses user input: nothing — a pending approval, the
// composer, transcript scrolling — can be driven blind. Only the global quit
// (ctrl+c) still works; the user resizes to interact.
func (m *Model) chatViewTooSmall() bool {
	if m.mode != ModeChat {
		return false
	}
	if m.width < minimumChatWidth || m.height < minimumChatHeight {
		return true
	}
	// A pending approval needs more room than the bare chat; when it doesn't fit,
	// its prompt is hidden behind the resize hint too.
	return len(m.pendingApprovals) > 0 &&
		(m.width < minimumApprovalWidth || m.height < minimumApprovalHeight)
}

// approvalView renders the docked approval block, or "" when nothing awaits
// approval. It mirrors the editor block's shape (prompt marker, aligned indent,
// top and bottom rules) on a distinct background so it reads as a sibling of
// the input rather than an overlay.
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

	sty := m.styles.Approval
	inner := max(1, m.width-sty.Block.GetHorizontalFrameSize())
	indent := strings.Repeat(" ", ansi.StringWidth(sty.Prompt))
	textWidth := max(1, inner-ansi.StringWidth(sty.Prompt))

	queue := "Approval required"
	if count := len(m.pendingApprovals); count > 1 {
		queue += " · " + strconv.Itoa(count) + " waiting"
	}
	lines := []string{
		sty.Marker.Render(sty.Prompt) + sty.Title.Render(ansi.Truncate(queue, textWidth, "…")),
		sty.Text.Render(indent + ansi.Truncate(title, textWidth, "…")),
	}
	if detail != "" {
		lines = append(lines, sty.Detail.Render(indent+ansi.Truncate(detail, textWidth, "…")))
	}

	lines = append(lines,
		sty.Action.Render(indent)+m.approvalActions(inner-ansi.StringWidth(indent)),
		sty.Detail.Render(indent+ansi.Truncate("←/→ choose · Enter confirm · Esc deny", textWidth, "…")),
	)
	return sty.Block.Width(m.width).Render(strings.Join(lines, "\n"))
}

// approvalActions renders the choice row within width, condensing labels on
// narrow terminals. The focused choice is bracketed and emphasized.
func (m *Model) approvalActions(width int) string {
	sty := m.styles.Approval
	labels := [...]string{"Deny", "Allow once", "Allow for session"}
	if m.width < approvalCompactWidth {
		labels = [...]string{"Deny", "Once", "Session"}
	}
	actions := make([]string, len(labels))
	for i, label := range labels {
		if i == m.approvalChoice {
			actions[i] = sty.Selected.Render("[ " + label + " ]")
		} else {
			actions[i] = sty.Action.Render("  " + label + "  ")
		}
	}
	// Join with a background-carrying space so the block fill stays continuous
	// between choices; a plain separator would leave gaps in the surface.
	return ansi.Truncate(strings.Join(actions, sty.Action.Render(" ")), max(1, width), "")
}

func (m *Model) composerHeight() int {
	h := m.editor.Height()
	if approval := m.approvalView(); approval != "" {
		h += lipgloss.Height(approval)
	}
	return h
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
