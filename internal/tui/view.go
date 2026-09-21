package tui

import (
	"os"
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
	minimumApprovalWidth  = 36
	minimumApprovalHeight = 13
	// The compact approval needs six rows to show its heading, request title,
	// detail, and every action. Below that, input stays disabled behind the
	// resize hint so a hidden choice cannot be confirmed.
	minimumApprovalPanelHeight = 6
	chatNoticeHeight           = 1
	chatFooterHeight           = 1

	// approvalCompactWidth is the terminal width below which the approval block
	// switches to condensed action labels so the choice row still fits.
	approvalCompactWidth = 50
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

// chatMouseMode escalates to AllMotion so accordion hover can be reported,
// following charmbracelet/crush's per-frame MouseMode computation. It never
// escalates under a terminal multiplexer.
func (m *Model) chatMouseMode() tea.MouseMode {
	if terminalMultiplexerActive() {
		return tea.MouseModeCellMotion
	}
	return tea.MouseModeAllMotion
}

// View lays out the transcript viewport, notice row, input, and metadata footer.
// Alt-screen and mouse tracking, which were program options in Bubble Tea v1,
// are now declared on the returned view.
func (m *Model) View() tea.View {
	if m.mode == ModeLogin && m.loginModel != nil {
		return m.loginModel.View()
	}
	v := tea.View{AltScreen: true, MouseMode: tea.MouseModeCellMotion, BackgroundColor: m.styles.Background}
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
		v.MouseMode = m.chatMouseMode()
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
	v.Content += m.pointerShape()
	return v
}

// pointerShape swaps the OS mouse pointer to a hand over a clickable
// accordion row via OSC 22 (kitty implements it fully; xterm and foot carry
// an older, simpler version of the same sequence; other terminals ignore it).
// Appended once per frame rather than spliced into the hovered row itself, so
// two rows changing hover state in the same frame can't race and leave the
// terminal on the wrong shape.
func (m *Model) pointerShape() string {
	if m.mode == ModeChat && m.list.Hovered() {
		return ansi.SetPointerShape("pointer")
	}
	return ansi.SetPointerShape("default")
}

// chatView stacks the transcript, an optional docked approval block, the notice
// bar, the editor, and a persistent metadata footer. The approval block sits
// above the notice bar and editor and pushes the transcript up instead of
// covering it; input is routed to the approval while one is pending, so the
// editor stays visible but inert.
func (m *Model) chatView() string {
	m.editor.SetPlaceholder(m.promptPlaceholder())
	if m.selection.selecting() || m.selection.selected() {
		return m.selection.render(m.visibleSelectionFrame(m.selection.scope))
	}

	base := m.chatViewBase(m.list.Render())
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
	y := max(0, m.height-chatFooterHeight-m.editor.Height()-menuH)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(menu).X(x).Y(y).Z(1),
	).Render()
}

// chatViewBase composes the chat surface without transient completion-menu
// overlays. Selection uses this exact composition so every rendered row stays
// in the same screen coordinate space as the normal chat view.
func (m *Model) chatViewBase(transcript string) string {
	sections := []string{transcript}
	if approval := m.approvalView(); approval != "" {
		sections = append(sections, approval)
	}
	sections = append(sections, m.noticeBar(), m.editor.View(), m.chatFooter())
	return strings.Join(sections, "\n")
}

// visibleSelectionFrame returns the current terminal-sized chat surface in the
// coordinate space of the pane that owns the selection. Transcript rows use
// virtual document coordinates; the fixed lower pane uses screen coordinates.
func (m *Model) visibleSelectionFrame(scope selectionScope) selectionFrame {
	surface := m.list.VisibleSurface()
	transcriptHeight := max(0, m.list.Height())
	headerRows := m.list.HeaderRows()
	rows := make([]int, max(0, m.height))
	for y := range rows {
		switch document := surface.Top + y; {
		case scope == selectionScopeLower:
			rows[y] = y
		case y >= transcriptHeight:
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
	return newSelectionFrame(
		m.chatViewBase(surface.Content),
		m.width,
		m.height,
		rows,
		floor,
	)
}

// transcriptSelectionFrame creates the full transcript used to copy a
// transcript-scoped selection when the drag is released.
func (m *Model) transcriptSelectionFrame() selectionFrame {
	content := m.list.Document()
	return newSelectionFrame(
		content,
		m.width,
		selectionRowCount(content),
		nil,
		m.list.HeaderRows(),
	)
}

func selectionRowCount(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(content, "\n") + 1
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
		(m.width < minimumApprovalWidth ||
			m.height < minimumApprovalHeight ||
			m.approvalAvailableHeight() < minimumApprovalPanelHeight)
}

// approvalView renders the docked approval panel, or "" when nothing awaits
// approval. It uses the shared bounded panel while keeping the action row and
// its selection state local to the approval flow.
func (m *Model) approvalView() string {
	if len(m.pendingApprovals) == 0 || m.pendingApprovals[0].Tool == nil {
		return ""
	}

	block := m.pendingApprovals[0]
	prompt := block.Tool.Approval
	title := "Run " + escape.Inline(block.Tool.Name) + "?"
	detail := ""
	if prompt != nil {
		if prompt.Title != "" {
			title = escape.Inline(prompt.Title)
		}
		detail = escape.Inline(prompt.Detail)
	}

	queue := "Permission Required"
	if count := len(m.pendingApprovals); count > 1 {
		queue += " · " + strconv.Itoa(count) + " waiting"
	}
	content := components.PanelContent{
		Title:   queue,
		Dismiss: "ESC x",
		Body: func(width int) string {
			return m.approvalBody(width, title, detail)
		},
		CompactTitle: queue,
		CompactBody: func(width int) string {
			lines := []string{m.styles.Approval.Text.Render(ansi.Truncate(title, width, "…"))}
			if detail != "" {
				lines = append(lines, m.styles.Approval.Detail.Render(ansi.Truncate(detail, width, "…")))
			}
			lines = append(lines, "", m.approvalActions(width))
			return strings.Join(lines, "\n")
		},
		TinyMessage: "Resize terminal to approve",
	}
	panel := components.NewPanel(m.styles.Approval.Panel).Render(m.width, max(1, m.approvalAvailableHeight()), content)
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, panel)
}

func (m *Model) approvalAvailableHeight() int {
	return m.height - chatNoticeHeight - chatFooterHeight - m.editor.Height()
}

func (m *Model) approvalBody(width int, title, detail string) string {
	sty := m.styles.Approval
	lines := []string{sty.Text.Render(ansi.Wordwrap(title, width, "-"))}
	if detail != "" {
		lines = append(lines, sty.Detail.Render(ansi.Wordwrap(detail, width, "-")))
	}
	lines = append(lines, "", m.approvalActions(width))
	return strings.Join(lines, "\n")
}

// approvalActions renders the choice row within width, condensing labels on
// narrow terminals. The focused choice uses the interactive fill.
func (m *Model) approvalActions(width int) string {
	sty := m.styles.Approval
	labels := [...]string{"Deny", "Allow once", "Allow for session"}
	if width < approvalCompactWidth {
		labels = [...]string{"Deny", "Once", "Session"}
	}
	actions := make([]string, len(labels))
	for i, label := range labels {
		if i == m.approvalChoice {
			actions[i] = sty.Selected.Render(label)
		} else {
			actions[i] = sty.Action.Render(label)
		}
	}
	return ansi.Truncate(strings.Join(actions, "  "), max(1, width), "")
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
	text = ansi.Truncate(escape.Inline(text), max(1, width-2), "…")
	return m.chatStyles.Notice(m.notice.Level).Width(width).Render(text)
}

// chatFooter renders low-attention workspace and context usage metadata below
// the editor. Transient notices keep their separate row above the editor.
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
