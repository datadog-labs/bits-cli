package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

const (
	minimumChatWidth  = 12
	minimumChatHeight = 5
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
		if m.width < minimumChatWidth || m.height < minimumChatHeight {
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

// chatView renders the chat screen: the transcript viewport, the status line,
// and the input, floating the completion menu as an overlay above the input
// when it is open.
func (m *Model) chatView() string {
	m.editor.SetPlaceholder(m.promptPlaceholder())
	base := strings.Join([]string{
		m.list.Render(),
		m.noticeBar(),
		m.editor.View(),
	}, "\n")

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
