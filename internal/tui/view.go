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

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/components"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
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
	if m.mode == ModeLogin && m.loginModel != nil {
		return m.terminalView(m.loginModel.View())
	}
	v := tea.View{AltScreen: true, MouseMode: tea.MouseModeCellMotion, BackgroundColor: m.styles.Background}
	switch m.mode {
	case ModeTermInit:
		v.Content = "loading…"
	case ModeChat:
		if m.frame.tooSmall {
			v.MouseMode = tea.MouseModeNone
			message := "Resize terminal to use Bits"
			switch {
			case m.quitConfirm.armed:
				message = quitConfirmHint
			case m.commandPrompt != nil:
				message = "Resize terminal or press esc"
			}
			message = ansi.Truncate(message, max(1, m.width), "")
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
	return m.terminalView(v)
}

// frame is the chat layout built once per update and shared by drawing and hit-testing.
//
// Rows stack as transcript, queue, prompt, gap, composer, tray, and footer. A
// replacing prompt occupies the composer slot and dims the transcript.
// The tray shows while background work runs, in every mode.
type frame struct {
	transcript  image.Rectangle
	queue       image.Rectangle
	queueView   string
	prompt      image.Rectangle
	composerGap image.Rectangle
	composer    image.Rectangle
	tray        image.Rectangle
	trayView    string
	footer      image.Rectangle

	// promptView is the shown prompt as its one Layout of the update drew it.
	promptView string

	// popup floats over the rest: the permissions picker, the exit card, or
	// the completion menu. Only one shows at a time.
	popup     image.Rectangle
	popupView string

	// follow is the jump-to-latest control floating over the transcript.
	follow      image.Rectangle
	followLabel string

	// replaced means the prompt takes the composer slot and dims chat.
	replaced bool

	// tooSmall means the chat cannot be usably rendered, so View replaces the
	// whole screen with a resize hint. The UI is then invisible and Update drops
	// user input: nothing — a shown prompt, the composer, transcript
	// scrolling — can be driven blind. Only ctrl+c (pressed twice) still works.
	tooSmall bool
}

// region names the part of the frame under a screen point.
type region uint8

const (
	regionTranscript region = iota
	regionPopup
	regionFollow
	regionPrompt
	regionQueue
	regionComposer
	regionChrome // the gap, tray, and footer around the composer
)

// at reports which region of the frame contains p, and p relative to that
// region. Floating surfaces come first, since they cover the rest.
func (f frame) at(p image.Point) (region, image.Point) {
	switch {
	case p.In(f.popup):
		return regionPopup, p.Sub(f.popup.Min)
	case p.In(f.follow):
		return regionFollow, p.Sub(f.follow.Min)
	case p.In(f.prompt):
		return regionPrompt, p.Sub(f.prompt.Min)
	case p.In(f.queue):
		return regionQueue, p.Sub(f.queue.Min)
	case p.In(f.transcript):
		return regionTranscript, p.Sub(f.transcript.Min)
	case p.In(f.composer):
		return regionComposer, p.Sub(f.composer.Min)
	default:
		return regionChrome, p
	}
}

// inputTop is the first row of what fills the input slot: the composer, or a
// prompt replacing it. Popups float just above it.
func (f frame) inputTop() int {
	if !f.queue.Empty() {
		return f.queue.Min.Y
	}
	if f.replaced {
		return f.prompt.Min.Y
	}
	return f.composer.Min.Y
}

// layout sizes the chat's surfaces in order of priority, then stacks them
// bottom-up. The tray sits below the composer but ranks below the prompt: it
// keeps one row while a prompt is sized and takes what the prompt leaves.
func (m *Model) layout() frame {
	var f frame
	p, answerable := m.prompt(), true
	f.replaced = p != nil && p.Placement() == components.ReplacesInput
	tasks := taskLines(m.state.Tasks)
	// Every surface leaves the transcript at least a row.
	const minimumTranscriptRows = 1

	free := m.height - chatFooterHeight - chatComposerGapHeight
	if !f.replaced {
		// A docked prompt sizes around a composer that typing cannot grow.
		m.editor.SetCompact(p != nil)
		free -= m.editor.Height()
	}
	if p != nil {
		slot := components.Slot{Width: m.width, Height: promptRows(free - min(1, len(tasks))), Waiting: len(m.waitingAsks()), Focused: !m.drafting}
		if f.promptView, answerable = p.Layout(slot); f.promptView != "" {
			free -= lipgloss.Height(f.promptView)
		}
	}
	// The tray keeps its summary line while the transcript keeps its row; a
	// prompt was sized around both.
	if free > minimumTranscriptRows {
		f.trayView = m.trayView(tasks, max(1, free-trayTranscriptRows))
	}

	y := m.height
	take := func(rows int) image.Rectangle {
		y -= rows
		return image.Rect(0, y, m.width, y+rows)
	}
	f.footer = take(chatFooterHeight)
	if f.trayView != "" {
		f.tray = take(lipgloss.Height(f.trayView))
	}
	if !f.replaced {
		f.composer = take(m.editor.Height())
		f.composerGap = take(chatComposerGapHeight)
	}
	if f.promptView != "" {
		f.prompt = take(lipgloss.Height(f.promptView))
	}
	if f.replaced {
		f.composerGap = take(chatComposerGapHeight)
	}
	// Give the queue only spare rows, always retaining transcript space. Its
	// own cap and overflow control keep the lower pane bounded.
	spareRows := max(0, y-minimumTranscriptRows)
	queueRows := spareRows / 2
	f.queueView = m.editor.QueueView(queueRows)
	if f.queueView != "" {
		f.queue = take(lipgloss.Height(f.queueView))
	}
	f.transcript = image.Rect(0, 0, m.width, max(minimumTranscriptRows, y))

	switch {
	case m.mode != ModeChat:
	case m.width < minimumChatWidth || m.height < minimumChatHeight:
		f.tooSmall = true
	case !answerable:
		// Hide prompts that lack room for the request or its controls.
		f.tooSmall = true
	}
	return f
}

// chatView stacks the frame's surfaces and floats its popup over them. Input
// is routed to a shown prompt; a docked one leaves the editor visible but
// inert.
func (m *Model) chatView() string {
	if m.selection.active() {
		return m.selection.render(m.visibleSelectionFrame(m.selection.scope), m.selectionCellStyle())
	}
	base := m.followOverlay(m.chatViewBase(m.list.Render()))
	if m.frame.popupView == "" {
		return base
	}
	layers := []*lipgloss.Layer{lipgloss.NewLayer(base)}
	if m.focus() == focusExitCard {
		// A monochrome terminal cannot show the fade behind the exit card, so
		// it keeps the chat as drawn.
		if !m.monochrome {
			layers[0] = lipgloss.NewLayer(fade(base, m.width, m.height, m.styles.ExitCard.Text, m.styles.Background))
		}
	} else {
		// A popup above the input blanks the whole rows it covers: transcript
		// cells beside it, such as a notice's marker, would read as stale parts
		// of the popup.
		rows := strings.TrimSuffix(strings.Repeat(strings.Repeat(" ", m.list.Width())+"\n", m.frame.popup.Dy()), "\n")
		layers = append(layers, lipgloss.NewLayer(rows).Y(m.frame.popup.Min.Y).Z(1))
	}
	layers = append(layers, lipgloss.NewLayer(m.frame.popupView).X(m.frame.popup.Min.X).Y(m.frame.popup.Min.Y).Z(2))
	return lipgloss.NewCompositor(layers...).Render()
}

// layoutPopup places the one surface floating over the chat. Selection, which
// redraws the screen as cells, shows none.
func (m *Model) layoutPopup() (string, image.Rectangle) {
	if m.frame.tooSmall || m.selection.active() {
		return "", image.Rectangle{}
	}
	switch m.focus() {
	case focusExitCard:
		return m.layoutExitCard()
	case focusEditor:
		return m.aboveInput(m.editor.MenuView(), m.editor.ContentOffset())
	case focusPrompt, focusPicker, focusStatus, focusLogin:
	}
	return "", image.Rectangle{}
}

// aboveInput floats a popup above the composer without reflowing the
// transcript, anchored at x and clamped so it never overflows the width.
func (m *Model) aboveInput(popup string, x int) (string, image.Rectangle) {
	if popup == "" {
		return "", image.Rectangle{}
	}
	popupW, popupH := lipgloss.Size(popup)
	if width := m.list.Width(); x+popupW > width {
		x = max(0, width-popupW)
	}
	y := max(0, m.frame.inputTop()-popupH)
	return popup, image.Rect(x, y, x+popupW, y+popupH)
}

// chatViewBase composes the chat surface without popups. Selection uses this
// exact composition so every rendered row stays in the same screen coordinate
// space as the normal chat view.
func (m *Model) chatViewBase(transcript string) string {
	gap := slices.Repeat([]string{""}, m.frame.composerGap.Dy())
	queue := []string{}
	if m.frame.queueView != "" {
		queue = append(queue, m.frame.queueView)
	}
	tray := []string{}
	if m.frame.trayView != "" {
		tray = append(tray, m.frame.trayView)
	}
	if m.frame.replaced {
		// The footer row stays so the prompt sits where the composer did.
		transcript = lipgloss.NewStyle().Faint(true).Render(transcript)
		footer := ""
		if m.quitConfirm.armed {
			footer = m.chatFooter()
		}
		return strings.Join(slices.Concat([]string{transcript}, queue, gap, []string{m.frame.promptView}, tray, []string{footer}), "\n")
	}
	sections := append([]string{transcript}, queue...)
	if m.frame.promptView != "" {
		sections = append(sections, m.frame.promptView)
	}
	sections = append(sections, gap...)
	sections = append(sections, m.editor.View())
	sections = append(sections, tray...)
	sections = append(sections, m.chatFooter())
	return strings.Join(sections, "\n")
}

// trayView renders the tray in at most rows rows, aligned with the footer.
func (m *Model) trayView(lines []trayLine, rows int) string {
	width, indent := m.lowerPaneText()
	view := m.tray.view(lines, max(1, width-indent), rows, m.styles)
	if view == "" {
		return ""
	}
	pad := strings.Repeat(" ", indent)
	return pad + strings.ReplaceAll(view, "\n", "\n"+pad)
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
	if m.op.kind == opFork {
		return "Forking…"
	}
	if m.op.kind == opRename {
		return "Renaming…"
	}
	if m.op.kind == opShare {
		return "Sharing…"
	}
	switch m.chatPhase {
	case chat.PhaseLoading:
		return "Loading…"
	default:
		return "Ask Bits…"
	}
}

// lowerPaneText is the width of the rows under the composer and the indent
// that aligns their text with typed input.
func (m *Model) lowerPaneText() (width, indent int) {
	width = max(1, m.list.Width())
	return width, min(m.editor.ContentOffset(), max(0, width-1))
}

// chatFooter renders low-attention workspace and context usage metadata below
// the editor.
func (m *Model) chatFooter() string {
	width, indent := m.lowerPaneText()
	text := chatFooterText(m.workspaceDisplayPath, m.backgroundCommands.label(), m.state.Usage, max(1, width-indent))
	if m.quitConfirm.armed {
		text = ansi.Truncate(quitConfirmHint, max(1, width-indent), "…")
	}
	if text == "" {
		return ""
	}
	// Secondary, not tertiary: the path and token count are meant to be read.
	return strings.Repeat(" ", indent) + m.styles.Text.Secondary.Render(text)
}

func chatFooterText(path, commands string, usage *assistant.Usage, width int) string {
	path = escape.SingleLine(path)
	var details []string
	if commands != "" {
		details = append(details, commands)
	}
	if usage != nil {
		details = append(details, compactTokenCount(usage.TokensUsed)+" used")
	}
	used := strings.Join(details, " · ")

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
