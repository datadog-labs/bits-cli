package tui

import (
	"image"
	"os"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/escape"
)

const (
	minimumChatWidth      = 12
	minimumChatHeight     = 8
	chatComposerGapHeight = 1
	chatFooterHeight      = 1
)

// terminalMultiplexerActive reports whether the process runs inside tmux,
// Zellij, or screen. AllMotion forwards every pointer move, which these
// multiplexers noticeably lag on, so hover degrades to CellMotion under them
// (following earendil-works/pi's mouse handling).
func terminalMultiplexerActive() bool {
	for _, env := range []string{"TMUX", "ZELLIJ", "STY"} {
		if os.Getenv(env) != "" {
			return true
		}
	}
	term := os.Getenv("TERM")
	return strings.HasPrefix(term, "tmux") || strings.HasPrefix(term, "screen")
}

// chatMouseMode escalates to AllMotion so accordion hover can be reported. It
// never escalates under a terminal multiplexer. Resolved once, in newShell.
func chatMouseMode() tea.MouseMode {
	if terminalMultiplexerActive() {
		return tea.MouseModeCellMotion
	}
	return tea.MouseModeAllMotion
}

// View lays out the transcript viewport, input, and metadata footer.
// Alt-screen and mouse tracking, which were program options in Bubble Tea v1,
// are now declared on the returned view.
func (m *Model) View() tea.View {
	m.follow.area = image.Rectangle{}
	if m.mode == ModeLogin && m.loginModel != nil {
		return m.loginModel.View()
	}
	v := tea.View{AltScreen: true, MouseMode: tea.MouseModeCellMotion, BackgroundColor: m.styles.Background}
	switch m.mode {
	case ModeTermInit:
		v.Content = "loading…"
	case ModeChat, ModePermissions:
		if m.frame.tooSmall {
			v.MouseMode = tea.MouseModeNone
			message := ansi.Truncate("Resize terminal to use Bits", max(1, m.width), "")
			v.Content = lipgloss.Place(max(1, m.width), max(1, m.height), lipgloss.Center, lipgloss.Center, message)
			break
		}
		v.MouseMode = m.chatMouseMode
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
		v.Content = m.status.View()
	}
	return v
}

// frame is the chat screen laid out for one update. relayout derives it once
// from state at the end of every Update; View, hit-testing, and every size
// query read it instead of measuring again.
//
// Rows stack top to bottom: transcript, dock, composer gap, composer, footer.
// The dock holds the prompt waiting on the user, and is empty when none is.
type frame struct {
	transcript  image.Rectangle
	dock        image.Rectangle
	composerGap image.Rectangle
	composer    image.Rectangle
	footer      image.Rectangle

	// dockView is the docked prompt, rendered once so it can be measured.
	dockView string

	// tooSmall means the chat cannot be usably rendered, so View replaces the
	// whole screen with a resize hint. The UI is then invisible and Update drops
	// user input: nothing — a docked prompt, the composer, transcript
	// scrolling — can be driven blind. Only ctrl+c still works.
	tooSmall bool
}

// region names the part of the frame under a screen point.
type region uint8

const (
	regionTranscript region = iota
	regionDock
	regionComposer
	regionChrome // the gap and footer around the composer
)

// at reports which region of the frame contains p.
func (f frame) at(p image.Point) region {
	switch {
	case p.In(f.transcript):
		return regionTranscript
	case p.In(f.dock):
		return regionDock
	case p.In(f.composer):
		return regionComposer
	default:
		return regionChrome
	}
}

// layout stacks the chat bottom-up, so each surface is sized against the rows
// left below the transcript, which takes the rest.
func (m *Model) layout() frame {
	var f frame
	y := m.height
	take := func(rows int) image.Rectangle {
		y -= rows
		return image.Rect(0, y, m.width, y+rows)
	}

	f.footer = take(chatFooterHeight)
	f.composer = take(m.editor.Height())
	f.composerGap = take(chatComposerGapHeight)
	dock := m.prompt()
	room := dockRows(y)
	if dock != nil {
		if f.dockView = dock.Layout(m.width, room); f.dockView != "" {
			f.dock = take(lipgloss.Height(f.dockView))
		}
	}
	f.transcript = image.Rect(0, 0, m.width, max(1, y))

	switch {
	case m.mode != ModeChat && m.mode != ModePermissions:
	case m.width < minimumChatWidth || m.height < minimumChatHeight:
		f.tooSmall = true
	case dock != nil:
		// A prompt that cannot be answered is hidden behind the resize hint too.
		minWidth, minHeight := dock.MinSize()
		f.tooSmall = m.width < minWidth || room < minHeight
	}
	return f
}

// chatView stacks the frame's surfaces and floats at most one popup — the
// permissions picker or the completion menu — above the composer. Input is
// routed to a docked prompt, so the editor stays visible but inert.
func (m *Model) chatView() string {
	if m.selection.active() {
		return m.selection.render(m.visibleSelectionFrame(m.selection.scope))
	}

	base := m.followOverlay(m.chatViewBase(m.list.Render()))
	switch {
	case m.mode == ModePermissions:
		// Spans the terminal inside its margin, so it does not track the composer.
		return m.chatOverlay(base, m.permissionsView(), m.styles.Permissions.HorizontalMargin)
	case m.focus() == focusEditor:
		return m.chatOverlay(base, m.editor.MenuView(), m.editor.ContentOffset())
	default:
		return base
	}
}

// chatOverlay floats a popup above the composer without reflowing the
// transcript, anchored at x and clamped so it never overflows the width.
func (m *Model) chatOverlay(base, popup string, x int) string {
	if popup == "" {
		return base
	}
	popupW, popupH := lipgloss.Size(popup)
	if width := m.list.Width(); x+popupW > width {
		x = max(0, width-popupW)
	}
	y := max(0, m.frame.composer.Min.Y-popupH)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(popup).X(x).Y(y).Z(1),
	).Render()
}

// chatViewBase composes the chat surface without popups. Selection uses this
// exact composition so every rendered row stays in the same screen coordinate
// space as the normal chat view.
func (m *Model) chatViewBase(transcript string) string {
	sections := []string{transcript}
	if m.frame.dockView != "" {
		sections = append(sections, m.frame.dockView)
	}
	sections = append(sections, slices.Repeat([]string{""}, m.frame.composerGap.Dy())...)
	sections = append(sections, m.editor.View(), m.chatFooter())
	return strings.Join(sections, "\n")
}

// visibleSelectionFrame returns the current terminal-sized chat surface in the
// coordinate space of the pane that owns the selection. Transcript rows use
// virtual document coordinates; the fixed lower pane uses screen coordinates.
func (m *Model) visibleSelectionFrame(scope selectionScope) selectionFrame {
	surface := m.list.VisibleSurface()
	split := m.frame.transcript.Max.Y
	headerRows := m.list.HeaderRows()
	rows := make([]int, max(0, m.height))
	for y := range rows {
		switch document := surface.Top + y; {
		case scope == selectionScopeLower:
			rows[y] = y
		case y >= split:
			// Lower-pane rows do not belong to a transcript selection.
			rows[y] = -1
		case document < headerRows:
			// The startup header is decoration, not transcript.
			rows[y] = -1
		default:
			rows[y] = document
		}
	}
	// The lower pane is in screen coordinates, where the header's document rows
	// mean nothing, so it keeps every row selectable.
	floor := headerRows
	if scope == selectionScopeLower {
		floor = 0
	}
	return selectionFrame{
		content:      m.chatViewBase(surface.Content),
		width:        m.width,
		height:       m.height,
		documentRows: rows,
		floor:        floor,
		split:        split,
	}
}

// transcriptSelectionFrame creates the full transcript used to copy a
// transcript-scoped selection when the drag is released.
func (m *Model) transcriptSelectionFrame() selectionFrame {
	content := m.list.Document()
	return selectionFrame{
		content: content,
		width:   m.width,
		height:  selectionRowCount(content),
		floor:   m.list.HeaderRows(),
	}
}

func selectionRowCount(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
}

// promptPlaceholder returns the current editor prompt placeholder.
func (m *Model) promptPlaceholder() string {
	switch {
	case m.op.then == thenLogout || m.op.kind == opLogout:
		return "Logging out…"
	case m.op.then == thenNewConversation:
		return "Starting a new conversation…"
	}
	switch m.chatPhase {
	case chat.PhaseLoading:
		return "Loading…"
	case chat.PhaseWaiting, chat.PhaseStreaming:
		return "Working on it…"
	default:
		return "Ask Bits…"
	}
}

// chatFooter renders low-attention workspace and context usage metadata below
// the editor.
func (m *Model) chatFooter() string {
	width := max(1, m.list.Width())
	indent := min(m.editor.ContentOffset(), max(0, width-1))
	text := chatFooterText(m.workspaceDisplayPath, m.usage, max(1, width-indent))
	if text == "" {
		return ""
	}
	return strings.Repeat(" ", indent) + m.styles.Text.Tertiary.Render(text)
}

func chatFooterText(path string, usage *assistant.Usage, width int) string {
	path = escape.SingleLine(path)
	used := ""
	if usage != nil {
		used = compactTokenCount(usage.TokensUsed) + " used"
	}

	switch {
	case path == "":
		return ansi.Truncate(used, width, "…")
	case used == "":
		return truncateLeft(path, width)
	}

	suffix := " · " + used
	if ansi.StringWidth(suffix) >= width {
		return ansi.Truncate(used, width, "…")
	}
	return truncateLeft(path, width-ansi.StringWidth(suffix)) + suffix
}

func truncateLeft(value string, width int) string {
	if width <= 0 {
		return ""
	}
	valueWidth := ansi.StringWidth(value)
	if valueWidth <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return ansi.TruncateLeft(value, valueWidth-width+1, "…")
}

func compactTokenCount(value int) string {
	var unit int
	var suffix string
	switch {
	case value >= 1_000_000_000:
		unit, suffix = 1_000_000_000, "B"
	case value >= 1_000_000:
		unit, suffix = 1_000_000, "M"
	case value >= 1_000:
		unit, suffix = 1_000, "K"
	default:
		return strconv.Itoa(value)
	}

	whole := value / unit
	remainder := value % unit
	if tenths := remainder / (unit / 10); whole < 10 && tenths > 0 {
		return strconv.Itoa(whole) + "." + strconv.Itoa(tenths) + suffix
	}
	rounded := whole
	if remainder >= unit/2 {
		rounded++
	}
	return strconv.Itoa(rounded) + suffix
}
