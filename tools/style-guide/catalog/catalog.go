package catalog

import (
	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// pageScrollLines is how many lines PgUp/PgDn (and space) move.
const pageScrollLines = 10

// wheelScrollLines is how many lines one mouse-wheel notch moves.
const wheelScrollLines = 3

// Model is the catalog's Bubble Tea model. It holds no engine or backend.
type Model struct {
	group  group
	isDark bool
	offset int
	width  int
	height int
	styles chat.Styles
}

// New returns a catalog model, defaulting to dark until the terminal background
// is detected.
func New() *Model {
	return &Model{isDark: true, styles: chat.DefaultStyles(true)}
}

// Init requests the terminal background so the catalog opens in the user's real
// color mode; it can still be toggled with `t`.
func (m *Model) Init() tea.Cmd {
	return func() tea.Msg { return tea.RequestBackgroundColor() }
}

// Update handles resize, background detection, mouse wheel, and keys.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.offset = clampOffset(m.offset, m.contentLines(), m.viewHeight())
		return m, nil
	case tea.BackgroundColorMsg:
		m.setDark(msg.IsDark())
		return m, nil
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.scroll(-wheelScrollLines)
		case tea.MouseWheelDown:
			m.scroll(wheelScrollLines)
		}
		return m, nil
	case tea.KeyPressMsg:
		return m, m.key(msg.String())
	}
	return m, nil
}

// key applies a keypress given its string form; split out from Update so it is
// testable without constructing terminal key messages.
func (m *Model) key(s string) tea.Cmd {
	switch s {
	case "q", "esc", "ctrl+c":
		return tea.Quit
	case "tab", "right", "l":
		m.group = nextGroup(m.group)
		m.offset = 0
	case "shift+tab", "left", "h":
		m.group = prevGroup(m.group)
		m.offset = 0
	case "up", "k":
		m.scroll(-1)
	case "down", "j":
		m.scroll(1)
	case "pgup":
		m.scroll(-pageScrollLines)
	case "pgdown", " ":
		m.scroll(pageScrollLines)
	case "t":
		m.setDark(!m.isDark)
	}
	return nil
}

// scroll moves the offset by delta lines, clamped to the current content.
func (m *Model) scroll(delta int) {
	m.offset = clampOffset(m.offset+delta, m.contentLines(), m.viewHeight())
}

// setDark switches color mode, rebuilding the styles and re-clamping.
func (m *Model) setDark(isDark bool) {
	if isDark == m.isDark {
		return
	}
	m.isDark = isDark
	m.styles = chat.DefaultStyles(isDark)
	m.offset = clampOffset(m.offset, m.contentLines(), m.viewHeight())
}

// viewHeight is the body area height (window minus the footer line).
func (m *Model) viewHeight() int {
	if m.height <= 1 {
		return 1
	}
	return m.height - 1
}

// contentLines is the number of lines in the current group's body.
func (m *Model) contentLines() int {
	return lineCount(renderGroup(m.group, m.width, m.styles, m.isDark))
}

// View renders the scrolled body of the current group plus the footer line.
func (m *Model) View() tea.View {
	body := renderGroup(m.group, m.width, m.styles, m.isDark)
	visible := visibleLines(body, m.offset, m.viewHeight())
	content := visible + "\n" + footer(m.width, m.group, m.isDark)
	return tea.View{
		Content:   content,
		AltScreen: true,
		MouseMode: tea.MouseModeCellMotion,
	}
}
