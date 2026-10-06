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
	"github.com/DataDog/bits-cli/internal/tui/components"
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

// frame is the chat layout built once per update and shared by drawing and hit-testing.
//
// Rows stack as transcript, prompt, gap, composer, and footer. A replacing
// prompt occupies the composer slot and dims the transcript.
type frame struct {
	transcript  image.Rectangle
	prompt      image.Rectangle
	composerGap image.Rectangle
	composer    image.Rectangle
	footer      image.Rectangle

	// promptView is the shown prompt as its one Layout of the update drew it.
	promptView string

	// replaced means the prompt takes the composer slot and dims chat.
	replaced bool

	// tooSmall means the chat cannot be usably rendered, so View replaces the
	// whole screen with a resize hint. The UI is then invisible and Update drops
	// user input: nothing — a shown prompt, the composer, transcript
	// scrolling — can be driven blind. Only ctrl+c still works.
	tooSmall bool
}

// region names the part of the frame under a screen point.
type region uint8

const (
	regionTranscript region = iota
	regionPrompt
	regionComposer
	regionChrome // the gap and footer around the composer
)

// at reports which region of the frame contains p. The prompt comes first,
// so it stays on top should it ever float over the rest.
func (f frame) at(p image.Point) region {
	switch {
	case p.In(f.prompt):
		return regionPrompt
	case p.In(f.transcript):
		return regionTranscript
	case p.In(f.composer):
		return regionComposer
	default:
		return regionChrome
	}
}

// inputTop is the first row of what fills the input slot: the composer, or a
// prompt replacing it. Popups float just above it.
func (f frame) inputTop() int {
	if f.replaced {
		return f.prompt.Min.Y
	}
	return f.composer.Min.Y
}

// layout stacks the chat bottom-up and lays out the shown prompt.
func (m *Model) layout() frame {
	var f frame
	y := m.height
	take := func(rows int) image.Rectangle {
		y -= rows
		return image.Rect(0, y, m.width, y+rows)
	}

	f.footer = take(chatFooterHeight)
	p, answerable := m.prompt(), true
	f.replaced = p != nil && p.Placement() == components.ReplacesInput
	if !f.replaced {
		f.composer = take(m.editor.Height())
		f.composerGap = take(chatComposerGapHeight)
	}
	if p != nil {
		free := y
		if f.replaced {
			free -= chatComposerGapHeight // the gap stays above the prompt
		}
		slot := components.Slot{Width: m.width, Height: promptRows(free), Waiting: len(m.waitingAsks())}
		if f.promptView, answerable = p.Layout(slot); f.promptView != "" {
			f.prompt = take(lipgloss.Height(f.promptView))
		}
	}
	if f.replaced {
		f.composerGap = take(chatComposerGapHeight)
	}
	f.transcript = image.Rect(0, 0, m.width, max(1, y))

	switch {
	case m.mode != ModeChat && m.mode != ModePermissions:
	case m.width < minimumChatWidth || m.height < minimumChatHeight:
		f.tooSmall = true
	case !answerable:
		// Hide prompts that lack room for the request or its controls.
		f.tooSmall = true
	}
	return f
}

// chatView stacks the frame's surfaces and floats at most one popup — the
// permissions picker or the completion menu — above the composer. Input is
// routed to a shown prompt; a docked one leaves the editor visible but inert.
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
	y := max(0, m.frame.inputTop()-popupH)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(popup).X(x).Y(y).Z(1),
	).Render()
}

// chatViewBase composes the chat surface without popups. Selection uses this
// exact composition so every rendered row stays in the same screen coordinate
// space as the normal chat view.
func (m *Model) chatViewBase(transcript string) string {
	gap := slices.Repeat([]string{""}, m.frame.composerGap.Dy())
	if m.frame.replaced {
		// The footer row stays, blank, so the prompt sits where the composer did.
		transcript = lipgloss.NewStyle().Faint(true).Render(transcript)
		return strings.Join(slices.Concat([]string{transcript}, gap, []string{m.frame.promptView, ""}), "\n")
	}
	sections := []string{transcript}
	if m.frame.promptView != "" {
		sections = append(sections, m.frame.promptView)
	}
	sections = append(sections, gap...)
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
