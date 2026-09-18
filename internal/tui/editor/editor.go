// Package editor is the chat input: a multiline textarea plus an @/ completion
// menu. The parent TUI owns search work and feeds checked results into it.
package editor

import (
	"slices"
	"strconv"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/components"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

const (
	// The completion menu stays compact on wide terminals and collapses to the
	// available editor width on narrow ones.
	menuMaxWidth    = 112
	menuMinWidth    = 40
	menuVisibleRows = 5
	menuSidePadding = 2
)

const (
	// maxHeight caps the visible input rows; taller content scrolls within the
	// box.
	maxHeight = 8
	// maxContentHeight caps total input rows so typing newlines isn't blocked at
	// the visible cap (maxHeight) — only at this larger hard limit. Without it,
	// the textarea blocks new lines once it reaches maxHeight logical lines.
	maxContentHeight = 500
	// maxMenuCandidates bounds the combined local and remote result rows.
	maxMenuCandidates = 10
)

// Editor is the chat input. The completion menu opens automatically for @
// tokens and for a / token only when it is the first token in the prompt.
type Editor struct {
	ta                textarea.Model
	styles            styles.Editor
	menu              menu
	attachments       []trackedAttachment
	fileQuery         string
	fileItems         []Candidate
	fileState         FileState
	remoteQuery       string
	remoteItems       []Candidate
	remoteState       RemoteState
	completedMentions []trackedMention
	dismissedValue    string

	// inputStyle is the shared input-block contract. width is the block's total
	// width; the textarea is sized to fit inside the block's horizontal frame.
	inputStyle  styles.Input
	view        string
	viewHeight  int
	viewCached  bool
	body        string
	bodyHeight  int
	bodyCached  bool
	width       int
	widthSet    bool
	menuHeight  int
	working     bool
	sweepFrame  int
	sweep       styles.BorderSweep
	sweepWidth  int
	sweepCached bool
}

// menu is the completion popup state rendered below the textarea.
type menu struct {
	open     bool
	items    []Candidate
	selector *components.Selector
	status   string
}

// New returns a chat editor. Call Focus to start the cursor and receive its
// blink command.
func New() *Editor {
	defaultStyles := styles.Default(true)
	ta := textarea.New()
	ta.Prompt = defaultStyles.Input.Prompt
	ta.Placeholder = "Ask Bits…"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = maxHeight
	ta.MaxContentHeight = maxContentHeight
	ta.MinHeight = 1
	ta.DynamicHeight = true

	// Enter submits (the parent handles it); newline moves to modifiers so the
	// textarea never swallows Enter.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "ctrl+j", "alt+enter"))

	// Drop the current-line highlight: a full-width bar behind a chat prompt is
	// visual noise.
	st := ta.Styles()
	st.Focused.CursorLine = lipgloss.NewStyle()
	st.Blurred.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(st)

	return &Editor{
		ta:         ta,
		styles:     defaultStyles.Editor,
		inputStyle: defaultStyles.Input,
	}
}

// ActiveEntityQuery returns the query associated with the @ trigger at the
// cursor. Local-file and remote-entity search share it; spaces are preserved.
func (e *Editor) ActiveEntityQuery() (string, bool) {
	span, ok := e.activeEntitySpan()
	return span.query, ok
}

// SetFileResults updates only the local-file half of a mixed @ menu.
func (e *Editor) SetFileResults(query string, state FileState, items []Candidate) {
	e.fileQuery = query
	e.fileState = state
	e.fileItems = append([]Candidate(nil), items...)
	e.recompute()
}

// SetEntityResults updates only the remote half of a mixed @ menu.
func (e *Editor) SetEntityResults(query string, state RemoteState, items []Candidate) {
	e.remoteQuery = query
	e.remoteState = state
	e.remoteItems = append([]Candidate(nil), items...)
	e.recompute()
}

// Attachments returns a copy of the selected canonical Datadog identities.
func (e *Editor) Attachments() []Attachment {
	attachments := make([]Attachment, 0, len(e.attachments))
	type entityKey struct{ entityType, id string }
	seen := make(map[entityKey]struct{}, len(e.attachments))
	for _, tracked := range e.attachments {
		key := entityKey{entityType: tracked.attachment.Type, id: tracked.attachment.ID}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		attachments = append(attachments, tracked.attachment)
	}
	return attachments
}

// RemoveLastAttachment removes the most recently selected Datadog entity and
// its visible mention.
func (e *Editor) RemoveLastAttachment() bool {
	if len(e.attachments) == 0 {
		return false
	}
	tracked := e.attachments[len(e.attachments)-1]
	before := e.ta.Value()
	runes := []rune(before)
	if tracked.start >= 0 && tracked.end <= len(runes) && tracked.start < tracked.end {
		after := string(runes[:tracked.start]) + string(runes[tracked.end:])
		e.ta.SetValue(after)
		e.applyMentionEdit(tracked.start, tracked.end, tracked.start, after)
	} else {
		e.attachments = e.attachments[:len(e.attachments)-1]
	}
	e.invalidateBody()
	return true
}

// SetInputStyles gives the editor the shared input-block look: a background
// fill, a colored caret, and one row of vertical padding above and below. The
// background lives on the textarea's Base style (inherited by every inner span,
// so the whole editor paints on bg with no gaps); the padding lives on an
// external wrapper (block) instead of Base, because textarea.placeholderView
// applies Base and the viewport itself and View applies both again — a padded
// Base would be applied twice and clip the placeholder and caret on empty input.
// The parent wires the shared input style so editor stays chat-free.
func (e *Editor) SetInputStyles(inputStyle styles.Input) {
	base := lipgloss.NewStyle().Background(inputStyle.Background)

	st := e.ta.Styles()
	st.Focused.Base, st.Blurred.Base = base, base
	st.Focused.Prompt, st.Blurred.Prompt = inputStyle.Marker, inputStyle.Marker
	// CursorLine needs the same foreground as Text: the cursor's line renders
	// through CursorLine, every other line through Text, so setting only Text
	// would leave the active line on the terminal's foreground.
	typed := lipgloss.NewStyle().Foreground(inputStyle.Text.GetForeground())
	st.Focused.Text = inputStyle.Text
	st.Focused.CursorLine = typed
	st.Blurred.CursorLine = lipgloss.NewStyle()
	// The textarea hardcodes ANSI 240 for the placeholder in both of its default
	// style sets, ignoring theme colors. Both states get the theme's color so an
	// empty composer reads the same whether or not it holds focus.
	st.Focused.Placeholder, st.Blurred.Placeholder = inputStyle.Placeholder, inputStyle.Placeholder
	st.Cursor.Color = inputStyle.Cursor
	e.ta.SetStyles(st)

	// Vertical padding only, matching the user block: the caret sits flush left
	// and the background fills the width.
	e.inputStyle = inputStyle
	e.resizeTextarea()
}

// SetStyles updates the completion-menu appearance.
func (e *Editor) SetStyles(menuStyles styles.Editor) {
	e.styles = menuStyles
	e.recompute()
}

// Focus focuses the textarea and returns its cursor-blink command.
func (e *Editor) Focus() tea.Cmd {
	e.invalidateBody()
	return e.ta.Focus()
}

// Blur removes focus from the textarea, hiding the cursor and making it ignore
// input. Used while another surface (a pending tool approval) owns the composer.
func (e *Editor) Blur() {
	e.invalidateBody()
	e.ta.Blur()
}

// Focused reports whether the textarea currently holds focus.
func (e *Editor) Focused() bool { return e.ta.Focused() }

// SetWidth sets the block's total width in cells. The textarea is sized to fit
// inside the block's horizontal frame so the block stays exactly w wide.
func (e *Editor) SetWidth(w int) {
	if e.widthSet && e.width == w {
		return
	}
	e.width = w
	e.widthSet = true
	e.resizeTextarea()
}

// resizeTextarea sizes the textarea to the width left inside the block frame.
func (e *Editor) resizeTextarea() {
	e.ta.SetWidth(max(1, e.width-e.inputStyle.Block.GetHorizontalFrameSize()))
	e.sweepCached = false
	e.invalidateBody()
}

// SetPlaceholder sets the hint shown while the input is empty.
func (e *Editor) SetPlaceholder(s string) {
	if e.ta.Placeholder != s {
		e.ta.Placeholder = s
		e.invalidateBody()
	}
}

// SetWorking toggles the animated border sweep shown while Bits is generating
// a response. Editor is a passive renderer — the parent model decides when to
// animate and drives frames through SetSweepFrame. Reduced-motion input styles
// retain this state without changing the static border.
func (e *Editor) SetWorking(working bool) {
	if e.working == working {
		return
	}
	e.working = working
	if e.inputStyle.SweepMotion {
		e.invalidateBody()
	}
}

// SetSweepFrame sets the current border-sweep animation frame. A frame set
// while not working is stored but does not invalidate the cache, since it
// has no visible effect until SetWorking(true).
func (e *Editor) SetSweepFrame(frame int) {
	if e.sweepFrame == frame {
		return
	}
	e.sweepFrame = frame
	if e.working && e.inputStyle.SweepMotion {
		e.viewCached = false
	}
}

// Value returns the current input text.
func (e *Editor) Value() string { return e.ta.Value() }

// SetValue replaces the prompt text. Tests use it to drive composer state
// without synthesising keypresses.
func (e *Editor) SetValue(s string) { e.ta.SetValue(s) }

// Reset clears the input and closes the menu.
func (e *Editor) Reset() {
	e.ta.Reset()
	e.attachments = nil
	e.fileQuery = ""
	e.fileItems = nil
	e.fileState = FileIdle
	e.remoteQuery = ""
	e.remoteItems = nil
	e.remoteState = RemoteIdle
	e.completedMentions = nil
	e.dismissedValue = ""
	e.closeMenu()
	e.invalidateBody()
}

// MenuOpen reports whether the completion menu is showing. The parent uses this
// to decide whether Enter/Esc drive the menu or submit/cancel.
func (e *Editor) MenuOpen() bool { return e.menu.open }

// MenuHasCandidates distinguishes a selectable menu from a loading, empty, or
// error status. Parents may submit directly when only status text is visible.
func (e *Editor) MenuHasCandidates() bool { return len(e.menu.items) > 0 }

// CloseMenu dismisses completion without changing the prompt.
func (e *Editor) CloseMenu() { e.closeMenu() }

// SelectedCommand returns the currently selected leading slash-command
// completion, without modifying the input. The parent uses it to dispatch a
// registered command directly when Enter is pressed.
func (e *Editor) SelectedCommand() (string, bool) {
	if !e.menu.open || len(e.menu.items) == 0 || e.menu.selector == nil || !e.commandTriggerActive(e.ta.Word()) {
		return "", false
	}
	insert := e.menu.items[e.menu.selector.Index()].Insert
	if !strings.HasPrefix(insert, "/") {
		return "", false
	}
	return strings.TrimPrefix(insert, "/"), true
}

// Height is the rendered height of the input in rows. It deliberately excludes
// the completion menu: the menu is an overlay (see MenuView), so opening it must
// not change the layout and reflow the transcript.
func (e *Editor) Height() int {
	e.renderView()
	return e.viewHeight
}

// Update handles one message. When the menu is open it consumes navigation keys
// (up/down/tab/enter/esc); otherwise the message is fed to the textarea and the
// menu is recomputed from the resulting value.
func (e *Editor) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		if k.String() == "ctrl+x" && e.RemoveLastAttachment() {
			return nil
		}
	}
	if k, ok := msg.(tea.KeyPressMsg); ok && e.menu.open {
		switch k.String() {
		case "up", "ctrl+p":
			if e.menu.selector != nil {
				e.menu.selector.UpdateKey("up")
			}
			return nil
		case "down", "ctrl+n":
			if e.menu.selector != nil {
				e.menu.selector.UpdateKey("down")
			}
			return nil
		case "tab", "enter":
			e.accept()
			return nil
		case "esc":
			e.dismissCompletion()
			e.closeMenu()
			return nil
		}
	}
	e.invalidateBody()
	before := e.ta.Value()
	beforeCursor := e.cursorOffset()
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(msg)
	after := e.ta.Value()
	if e.dismissedValue != "" && e.ta.Value() != e.dismissedValue {
		e.dismissedValue = ""
	}
	if start, oldEnd, newEnd, changed := textEditRange(before, after, beforeCursor, e.cursorOffset()); changed {
		e.applyMentionEdit(start, oldEnd, newEnd, after)
	}
	e.recompute()
	return cmd
}

// View renders the input block: the textarea wrapped in the shared background
// and one row of vertical padding. The completion menu is returned separately by
// MenuView so the parent can composite it as an overlay.
func (e *Editor) View() string {
	e.renderView()
	return e.view
}

func (e *Editor) renderView() {
	if e.viewCached {
		return
	}
	animated := e.working && e.inputStyle.SweepMotion
	if !e.bodyCached {
		block := e.inputStyle.Block
		if animated {
			// The sweep row replaces the block's own top border. BorderTop(false)
			// returns a new Style, leaving the idle style unaffected.
			block = block.BorderTop(false)
		}
		textareaView := e.ta.View()
		if e.width <= 0 {
			e.body = block.Render(textareaView)
		} else {
			e.body = block.Width(e.width).Render(textareaView)
		}
		e.bodyHeight = lipgloss.Height(e.body)
		e.bodyCached = true
	}
	if !animated {
		e.view = e.body
		e.viewHeight = e.bodyHeight
		e.viewCached = true
		return
	}
	rowWidth := e.width
	if rowWidth <= 0 {
		rowWidth = lipgloss.Width(e.body)
	}
	if !e.sweepCached || e.sweepWidth != rowWidth {
		e.sweep = styles.NewBorderSweep(rowWidth, e.inputStyle.SweepDim, e.inputStyle.SweepHot, e.inputStyle.Background)
		e.sweepWidth = rowWidth
		e.sweepCached = true
	}
	e.view = e.sweep.Row(e.sweepFrame) + "\n" + e.body
	e.viewHeight = e.bodyHeight + 1
	e.viewCached = true
}

// invalidateBody discards both layers of the composer cache. Animation frames
// invalidate only viewCached, allowing the static textarea and box to survive.
func (e *Editor) invalidateBody() {
	e.bodyCached = false
	e.viewCached = false
}

// ContentOffset returns the number of cells from the editor's left edge to its
// text content. Parents use it to align overlays with the input text.
func (e *Editor) ContentOffset() int { return e.inputStyle.ContentOffset() }

// SetMenuHeight limits the overlay to the rows available above the composer.
func (e *Editor) SetMenuHeight(height int) { e.menuHeight = max(0, height) }

// MenuView renders the completion menu as an opaque, fixed-width block, or ""
// when closed. The parent floats it above the input; giving every row a
// background makes it read as a popup over the transcript rather than letting
// transcript text show through the gaps.
func (e *Editor) MenuView() string {
	if !e.menu.open {
		return ""
	}
	outerWidth := e.menuWidth()
	frameWidth := e.styles.MenuFrame.GetHorizontalFrameSize()
	innerWidth := max(1, outerWidth-frameWidth-2*menuSidePadding)
	maxHeight := e.menuHeight
	if maxHeight <= 0 {
		maxHeight = menuVisibleRows + 5
	}
	if maxHeight < 4 || outerWidth < frameWidth+2*menuSidePadding+1 {
		return ""
	}

	parts := make([]string, 0, menuVisibleRows+2)
	remaining := maxHeight - e.styles.MenuFrame.GetVerticalFrameSize() - len(parts)
	statusRows := 0
	if e.menu.status != "" {
		statusRows = 1
	}
	rowBudget := min(menuVisibleRows, len(e.menu.items))
	rowBudget = min(rowBudget, max(0, remaining-statusRows))
	showFooter := len(e.menu.items) > rowBudget && remaining-statusRows >= 2
	if showFooter {
		rowBudget = min(rowBudget, remaining-statusRows-1)
	}
	showFooterGap := showFooter && remaining-statusRows-rowBudget-1 > 0

	window := components.SelectionWindow{}
	if e.menu.selector != nil && len(e.menu.items) > 0 && rowBudget > 0 {
		selector, visible := e.menu.selector.ViewWindow(innerWidth, rowBudget)
		parts = append(parts, selector)
		window = visible
	}
	if e.menu.status != "" {
		parts = append(parts, e.menuLine(e.menu.status, innerWidth, e.styles.MenuHelp))
	}
	if showFooter {
		if showFooterGap {
			parts = append(parts, e.menuLine("", innerWidth, e.styles.MenuHelp))
		}
		parts = append(parts, e.menuLine(overflowHint(window), innerWidth, e.styles.MenuHelp))
	}
	return e.styles.MenuFrame.Width(outerWidth).Padding(0, menuSidePadding).Render(strings.Join(parts, "\n"))
}

func (e *Editor) menuWidth() int {
	w := 0
	for _, it := range e.menu.items {
		w = max(w, ansi.StringWidth(it.Label)+ansi.StringWidth(it.Detail)+4)
	}
	w = max(w, ansi.StringWidth(e.menu.status))
	w += e.styles.MenuFrame.GetHorizontalFrameSize() + 2*menuSidePadding
	w = min(max(w, menuMinWidth), menuMaxWidth)
	if e.widthSet {
		return min(menuMaxWidth, max(1, e.width-e.ContentOffset()))
	}
	return w
}

func (e *Editor) menuLine(value string, width int, style lipgloss.Style) string {
	value = ansi.Truncate(value, width, "…")
	return style.Render(value + strings.Repeat(" ", max(0, width-ansi.StringWidth(value))))
}

func overflowHint(window components.SelectionWindow) string {
	if window.HiddenBelow == 0 {
		return "↓ back to top"
	}
	return "↓ " + strconv.Itoa(window.HiddenBelow) + " more below"
}

// recompute refreshes the menu from the word at the cursor. An empty candidate
// set (no "@"/"/" trigger, or nothing matched) closes the menu; the selection is
// kept when it still points at a valid item.
func (e *Editor) recompute() {
	if e.dismissedValue != "" && e.ta.Value() == e.dismissedValue {
		e.closeMenu()
		return
	}
	word := e.ta.Word()
	if strings.HasPrefix(word, "/") {
		if !e.commandTriggerActive(word) {
			e.closeMenu()
			return
		}
		e.setMenu(CommandCandidates(word[1:]), "")
		return
	}
	span, active := e.activeEntitySpan()
	if !active {
		e.closeMenu()
		return
	}
	items := make([]Candidate, 0, maxMenuCandidates)
	fileState := FileIdle
	if span.query == e.fileQuery {
		items = append(items, e.fileItems...)
		fileState = e.fileState
	}
	remoteState := RemoteIdle
	if span.query == e.remoteQuery {
		items = append(items, e.remoteItems...)
		remoteState = e.remoteState
	}
	if len(items) > maxMenuCandidates {
		items = items[:maxMenuCandidates]
	}
	status := completionStatus(fileState, remoteState, len(items))
	e.setMenu(items, status)
}

func completionStatus(fileState FileState, remoteState RemoteState, itemCount int) string {
	statuses := make([]string, 0, 2)
	switch fileState {
	case FileIdle, FileReady:
	case FileIndexing:
		statuses = append(statuses, "Indexing files…")
	case FileError:
		statuses = append(statuses, "File search unavailable")
	}
	switch remoteState {
	case RemoteIdle, RemoteReady:
	case RemoteLoading:
		statuses = append(statuses, "Searching entities…")
	case RemoteError:
		statuses = append(statuses, "Entity search unavailable")
	}
	if len(statuses) == 0 && itemCount == 0 && fileState == FileReady && remoteState == RemoteReady {
		return "No matching files or entities"
	}
	return strings.Join(statuses, " · ")
}

func (e *Editor) setMenu(items []Candidate, status string) {
	previous := 0
	var selected candidateIdentity
	hasSelected := false
	if e.menu.selector != nil {
		previous = e.menu.selector.Index()
		if previous >= 0 && previous < len(e.menu.items) {
			item := e.menu.items[previous]
			if item.ID != "" {
				selected = candidateIdentity{kind: item.Kind, id: item.ID}
				hasSelected = true
			}
		}
	}
	if hasSelected {
		for i, item := range items {
			if item.Kind == selected.kind && item.ID == selected.id {
				previous = i
				break
			}
		}
	}
	choices := make([]components.Choice, len(items))
	for i, item := range items {
		choices[i] = components.Choice{Label: item.Label, Detail: item.Detail}
	}
	selector := components.NewSelector(choices, e.selectorStyles())
	selector.SetFillWidth(true)
	selector.SetIndex(previous)
	e.menu = menu{open: len(items) > 0 || status != "", items: items, selector: selector, status: status}
}

// candidateIdentity is the stable completion identity used to retain a
// selection while asynchronous sources reorder the visible rows.
type candidateIdentity struct {
	kind CandidateKind
	id   string
}

func (e *Editor) selectorStyles() styles.Selector {
	return styles.Selector{
		Item: e.styles.MenuItem, Selected: e.styles.MenuSelected,
		Detail: e.styles.MenuDetail, SelectedDetail: e.styles.MenuSelectedDetail,
		Marker: "", SelectedMarker: "", ColumnGap: 4,
	}
}

// commandTriggerActive reports whether word is the first token on the first
// input line. A slash elsewhere is ordinary prompt text, not a command.
func (e *Editor) commandTriggerActive(word string) bool {
	if !strings.HasPrefix(word, "/") || e.ta.Line() != 0 {
		return false
	}
	lines := strings.Split(e.ta.Value(), "\n")
	if len(lines) == 0 {
		return false
	}
	runes := []rune(lines[0])
	col := min(max(e.ta.Column(), 0), len(runes))
	start, _ := wordBounds(runes, col)
	return start == 0
}

// accept replaces the active trigger span with the selected candidate's insert
// text plus a trailing space, then closes the menu. The cursor lands after the
// inserted space when the edit is on the final line (the common single-line
// case); otherwise it falls back to the buffer end.
func (e *Editor) accept() {
	if !e.menu.open || len(e.menu.items) == 0 || e.menu.selector == nil {
		e.closeMenu()
		return
	}
	candidate := e.menu.items[e.menu.selector.Index()]
	insert := candidate.Insert

	before := e.ta.Value()
	lines := strings.Split(before, "\n")
	row := e.ta.Line()
	if row < 0 || row >= len(lines) {
		e.closeMenu()
		return
	}
	runes := []rune(lines[row])
	col := min(max(e.ta.Column(), 0), len(runes))
	lineStart := 0
	for i := range row {
		lineStart += len([]rune(lines[i])) + 1
	}
	start, end := wordBounds(runes, col)
	if candidate.Kind != CandidateCommand {
		if span, ok := e.activeEntitySpan(); ok {
			start = span.start
			end = span.replacementEnd
		}
	}

	repl := []rune(insert + " ")
	lines[row] = string(runes[:start]) + string(repl) + string(runes[end:])
	value := strings.Join(lines, "\n")
	e.ta.SetValue(value)
	// Completion is an edit like typing or pasting. Move or remove existing
	// mention spans before registering this completion.
	e.applyMentionEdit(lineStart+start, lineStart+end, lineStart+start+len(repl), value)
	e.invalidateBody()
	if row == len(lines)-1 {
		e.ta.SetCursorColumn(start + len(repl))
	}
	mention := trackedMention{text: insert, start: lineStart + start, end: lineStart + start + len([]rune(insert))}
	if candidate.Kind != CandidateCommand {
		e.completedMentions = append(e.completedMentions, mention)
	}
	if candidate.Attachment != nil {
		e.attachments = append(e.attachments, trackedAttachment{attachment: *candidate.Attachment, trackedMention: mention})
	}
	e.closeMenu()
}

func (e *Editor) closeMenu() { e.menu = menu{} }

type trackedMention struct {
	text       string
	start, end int // rune offsets in the entire prompt
}

type trackedAttachment struct {
	attachment Attachment
	trackedMention
}

// applyEdit shifts intact mentions and invalidates those touched by the edit.
func (m *trackedMention) applyEdit(start, oldEnd, newEnd int, after []rune) bool {
	switch {
	case oldEnd <= m.start:
		m.start += newEnd - oldEnd
		m.end += newEnd - oldEnd
	case start >= m.end:
		// The edit follows the mention.
	default:
		return false
	}
	return m.start >= 0 && m.end <= len(after) && string(after[m.start:m.end]) == m.text
}

func (e *Editor) applyMentionEdit(start, oldEnd, newEnd int, after string) {
	runes := []rune(after)
	kept := e.attachments[:0]
	for _, tracked := range e.attachments {
		if tracked.applyEdit(start, oldEnd, newEnd, runes) {
			kept = append(kept, tracked)
		}
	}
	e.attachments = kept
	completed := e.completedMentions[:0]
	for _, mention := range e.completedMentions {
		if mention.applyEdit(start, oldEnd, newEnd, runes) {
			completed = append(completed, mention)
		}
	}
	e.completedMentions = completed
}

func (e *Editor) cursorOffset() int {
	lines := strings.Split(e.ta.Value(), "\n")
	row := min(max(e.ta.Line(), 0), len(lines)-1)
	offset := 0
	for i := range row {
		offset += len([]rune(lines[i])) + 1
	}
	return offset + min(max(e.ta.Column(), 0), len([]rune(lines[row])))
}

func textEditRange(before, after string, beforeCursor, afterCursor int) (start, oldEnd, newEnd int, changed bool) {
	oldRunes, newRunes := []rune(before), []rune(after)
	if slices.Equal(oldRunes, newRunes) {
		return 0, 0, 0, false
	}
	delta := len(newRunes) - len(oldRunes)
	switch {
	case delta > 0:
		start, oldEnd, newEnd = beforeCursor, beforeCursor, beforeCursor+delta
	case delta < 0 && afterCursor < beforeCursor:
		start, oldEnd, newEnd = afterCursor, afterCursor-delta, afterCursor
	case delta < 0:
		start, oldEnd, newEnd = beforeCursor, beforeCursor-delta, beforeCursor
	default:
		start = 0
		for start < len(oldRunes) && oldRunes[start] == newRunes[start] {
			start++
		}
		oldEnd, newEnd = len(oldRunes), len(newRunes)
		for oldEnd > start && newEnd > start && oldRunes[oldEnd-1] == newRunes[newEnd-1] {
			oldEnd--
			newEnd--
		}
	}
	if validTextEdit(oldRunes, newRunes, start, oldEnd, newEnd) {
		return start, oldEnd, newEnd, true
	}
	start = 0
	for start < len(oldRunes) && start < len(newRunes) && oldRunes[start] == newRunes[start] {
		start++
	}
	oldEnd, newEnd = len(oldRunes), len(newRunes)
	for oldEnd > start && newEnd > start && oldRunes[oldEnd-1] == newRunes[newEnd-1] {
		oldEnd--
		newEnd--
	}
	return start, oldEnd, newEnd, true
}

func validTextEdit(before, after []rune, start, oldEnd, newEnd int) bool {
	if start < 0 || start > oldEnd || oldEnd > len(before) || newEnd < start || newEnd > len(after) {
		return false
	}
	rebuilt := make([]rune, 0, len(after))
	rebuilt = append(rebuilt, before[:start]...)
	rebuilt = append(rebuilt, after[start:newEnd]...)
	rebuilt = append(rebuilt, before[oldEnd:]...)
	return slices.Equal(rebuilt, after)
}

type entitySpan struct {
	start          int
	replacementEnd int
	query          string
}

// activeEntitySpan finds the last valid @ trigger before the cursor on the
// current line. A letter, digit, or underscore immediately before @ makes it
// ordinary word content. Query text may contain spaces.
func (e *Editor) activeEntitySpan() (entitySpan, bool) {
	if e.dismissedValue != "" && e.ta.Value() == e.dismissedValue {
		return entitySpan{}, false
	}
	lines := strings.Split(e.ta.Value(), "\n")
	row := e.ta.Line()
	if row < 0 || row >= len(lines) {
		return entitySpan{}, false
	}
	runes := []rune(lines[row])
	col := min(max(e.ta.Column(), 0), len(runes))
	lineStart := 0
	for i := range row {
		lineStart += len([]rune(lines[i])) + 1
	}
	start, quoteStart := -1, -1
	var quote mentionQuote
	// Scan forward so an @ inside a quoted query cannot become a trigger.
	// Quotes in ordinary prose do not affect mention parsing.
	for i := range col {
		if e.insideCompletedMention(lineStart + i) {
			start = -1
			quote = mentionQuote{}
			continue
		}
		if start >= 0 && quote.consume(runes[i], i == quoteStart) {
			continue
		}
		if runes[i] != '@' {
			continue
		}
		if i > 0 && (unicode.IsLetter(runes[i-1]) || unicode.IsDigit(runes[i-1]) || runes[i-1] == '_') {
			continue
		}
		start = i
		quoteStart = entityQuoteStart(runes, start)
	}
	if start < 0 {
		return entitySpan{}, false
	}
	replacementEnd := entityReplacementEnd(runes, start, col)
	for _, tracked := range e.completedMentions {
		trackedStart := tracked.start - lineStart
		if trackedStart > start && trackedStart < replacementEnd {
			replacementEnd = trackedStart
		}
	}
	return entitySpan{
		start:          start,
		replacementEnd: replacementEnd,
		query:          string(runes[start+1 : col]),
	}, true
}

// entityQuoteStart recognizes only @"label" and @type:"label" openers.
// Later quotes, including closing quotes around prose, are ordinary text.
func entityQuoteStart(runes []rune, start int) int {
	if start+1 < len(runes) && runes[start+1] == '"' {
		return start + 1
	}
	for i := start + 1; i < len(runes); i++ {
		if runes[i] == ':' {
			if i > start+1 && i+1 < len(runes) && runes[i+1] == '"' {
				return i + 1
			}
			return -1
		}
		if unicode.IsSpace(runes[i]) || runes[i] == '"' || runes[i] == '@' {
			return -1
		}
	}
	return -1
}

type mentionQuote struct {
	quoted, escaped bool
}

// consume reports whether r is quote syntax or quoted content.
func (q *mentionQuote) consume(r rune, opening bool) bool {
	if !q.quoted && !opening {
		return false
	}
	switch {
	case q.escaped:
		q.escaped = false
	case r == '\\':
		q.escaped = true
	case r == '"':
		q.quoted = !q.quoted
	}
	return true
}

func entityReplacementEnd(runes []rune, start, col int) int {
	_, end := wordBounds(runes, col)
	quoteStart := entityQuoteStart(runes, start)
	var quote mentionQuote
	// Include quotes ahead of the caret before deciding where the token ends.
	for i := start + 1; i < len(runes); i++ {
		if i >= end && !quote.quoted {
			return i
		}
		if i >= col && runes[i] == '"' && !quote.quoted && i != quoteStart {
			return i // preserve an ordinary prose quote after the caret
		}
		wasQuoted := quote.quoted
		quote.consume(runes[i], i == quoteStart)
		if i >= col && wasQuoted && !quote.quoted {
			return i + 1
		}
	}
	return len(runes)
}

func (e *Editor) insideCompletedMention(position int) bool {
	for _, tracked := range e.completedMentions {
		if position >= tracked.start && position < tracked.end {
			return true
		}
	}
	return false
}

func (e *Editor) dismissCompletion() {
	e.dismissedValue = e.ta.Value()
}

// wordBounds returns the [start, end) rune indices of the word at col, using the
// same scan as textarea.Word (the reference char is col-1). It returns an empty
// range at col when the cursor is at the start, past the end, or on whitespace.
func wordBounds(runes []rune, col int) (start, end int) {
	c := col - 1
	if c < 0 || c >= len(runes) || unicode.IsSpace(runes[c]) {
		return col, col
	}
	start, end = c, c
	for start > 0 && !unicode.IsSpace(runes[start-1]) {
		start--
	}
	for end < len(runes) && !unicode.IsSpace(runes[end]) {
		end++
	}
	return start, end
}
