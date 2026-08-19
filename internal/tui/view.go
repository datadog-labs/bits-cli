package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// View lays out the transcript viewport, a status line, and the input. Alt-screen
// and mouse tracking, which were program options in Bubble Tea v1, are now
// declared on the returned view.
func (m *Model) View() tea.View {
	v := tea.View{AltScreen: true, MouseMode: tea.MouseModeCellMotion}
	if !m.ready {
		v.Content = "loading…"
		return v
	}
	base := strings.Join([]string{
		m.viewport.View(),
		m.statusLine(),
		m.editor.View(),
	}, "\n")

	menu := m.editor.MenuView()
	if menu == "" {
		v.Content = base
		return v
	}

	// Float the completion menu as a fixed overlay just above the input. The
	// editor's Height excludes the menu, so opening it covers the transcript's
	// bottom rows without reflowing the layout.
	menuW, menuH := lipgloss.Width(menu), lipgloss.Height(menu)
	x := 2 // align under the "› " prompt
	if width := m.viewport.Width(); x+menuW > width {
		x = max(0, width-menuW)
	}
	y := max(0, m.height-m.editor.Height()-menuH)
	v.Content = lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(menu).X(x).Y(y).Z(1),
	).Render()
	return v
}

func (m *Model) statusLine() string {
	var b strings.Builder
	b.WriteString(phaseLabel(m.chatPhase))
	if m.usage != nil && m.usage.TokensUsed > 0 {
		fmt.Fprintf(&b, " · %d tokens", m.usage.TokensUsed)
	}
	if m.convID != "" {
		fmt.Fprintf(&b, " · %s", shortID(m.convID))
	}
	if m.chatPhase == chat.PhaseError && m.errMsg != "" {
		fmt.Fprintf(&b, " · %s", m.errMsg)
	}
	return m.chatStyles.Meta.Render(ansi.Truncate(b.String(), max(1, m.viewport.Width()), "…"))
}

// phaseLabel is exhaustive over chat.Phase.
func phaseLabel(p chat.Phase) string {
	switch p {
	case chat.PhaseIdle:
		return "ready"
	case chat.PhaseLoading:
		return "loading history…"
	case chat.PhaseWaiting:
		return "waiting…"
	case chat.PhaseStreaming:
		return "streaming…"
	case chat.PhaseError:
		return "error"
	}
	return ""
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
