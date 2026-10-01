package catalog

import (
	"time"

	tea "charm.land/bubbletea/v2"

	tuistyles "github.com/DataDog/bits-cli/internal/tui/styles"
)

// pageScrollLines is how many lines PgUp/PgDn (and space) move.
const pageScrollLines = 10

// wheelScrollLines is how many lines one mouse-wheel notch moves.
const wheelScrollLines = 3

// animInterval is one animation frame, matching the tui's own rate so the
// catalog shows the motion at the speed a transcript does.
const animInterval = 50 * time.Millisecond

// stepMsg advances the animated page. generation identifies the armed tick
// chain, so a chain superseded by navigation dies on its next tick instead of
// advancing the frame alongside a newer one.
type stepMsg struct{ generation uint64 }

// Model is the catalog's Bubble Tea model. It holds no engine or backend.
type Model struct {
	group  group
	isDark bool
	offset int
	width  int
	height int
	theme  tuistyles.Theme

	// Animation state, live only while a page whose animates() is true is on
	// screen. Every other page costs nothing.
	frame          int
	animGeneration uint64
	animArmed      bool
}

// New returns a catalog model, defaulting to dark until the terminal background
// is detected.
func New() *Model {
	return &Model{isDark: true, theme: tuistyles.Default(true)}
}

// Init requests the terminal background so the catalog opens in the user's real
// color mode; it can still be toggled with `t`.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg { return tea.RequestBackgroundColor() },
		m.syncAnimation(),
	)
}

// syncAnimation arms or disarms the frame tick to match the visible page. It
// returns a command only when it starts a chain, so calling it after every
// navigation is safe and idempotent.
func (m *Model) syncAnimation() tea.Cmd {
	want := m.group.animates()
	if want == m.animArmed {
		return nil
	}

	m.animGeneration++
	m.animArmed = want
	if !want {
		m.frame = 0
		return nil
	}
	generation := m.animGeneration
	return tea.Tick(animInterval, func(time.Time) tea.Msg {
		return stepMsg{generation: generation}
	})
}

// advanceAnimation handles one frame of the armed chain and re-arms it. A tick
// from a superseded chain returns no command, ending that chain.
func (m *Model) advanceAnimation(msg stepMsg) tea.Cmd {
	if !m.animArmed || msg.generation != m.animGeneration {
		return nil
	}
	m.frame++
	generation := m.animGeneration
	return tea.Tick(animInterval, func(time.Time) tea.Msg {
		return stepMsg{generation: generation}
	})
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
		default:
		}
		return m, nil
	case stepMsg:
		return m, m.advanceAnimation(msg)
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
		return m.syncAnimation()
	case "shift+tab", "left", "h":
		m.group = prevGroup(m.group)
		m.offset = 0
		return m.syncAnimation()
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
	m.theme = tuistyles.Default(isDark)
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
	return lineCount(renderGroup(m.group, m.width, m.theme, m.frame))
}

// View renders the scrolled body of the current group plus the footer line.
func (m *Model) View() tea.View {
	body := renderGroup(m.group, m.width, m.theme, m.frame)
	visible := visibleLines(body, m.offset, m.viewHeight())
	content := visible + "\n" + footer(m.width, m.group, m.isDark, m.theme.Text.Tertiary)
	return tea.View{
		Content:   content,
		AltScreen: true,
		MouseMode: tea.MouseModeCellMotion,
		// Paint the app's own pinned background so swatches are judged against
		// the surface they'll actually sit on, not the author's terminal.
		BackgroundColor: m.theme.Background,
	}
}
