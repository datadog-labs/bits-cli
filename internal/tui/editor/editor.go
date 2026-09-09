// Package editor is the chat input: a multiline textarea plus an @/ completion
// menu. The parent TUI owns remote work and feeds checked results into it.
package editor

import (
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

// menuMaxWidth caps the completion popup width in cells.
const menuMaxWidth = 60

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
	ta                 textarea.Model
	styles             styles.Editor
	menu               menu
	attachments        []Attachment
	attachmentMentions []string
	remoteQuery        string
	remoteItems        []Candidate
	remoteState        RemoteState
	dismissedMentions  []dismissedMention
	dismissedValue     string

	// inputStyle is the shared input-block contract. width is the block's total
	// width; the textarea is sized to fit inside the block's horizontal frame.
	inputStyle styles.Input
	view       string
	viewHeight int
	viewCached bool
	width      int
	widthSet   bool
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

// ActiveEntityQuery returns the remote query associated with the @ trigger at
// the cursor. Spaces are preserved.
func (e *Editor) ActiveEntityQuery() (string, bool) {
	span, ok := e.activeEntitySpan()
	return span.query, ok
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
	return append([]Attachment(nil), e.attachments...)
}

// RemoveLastAttachment removes the most recently selected Datadog entity and
// its visible mention.
func (e *Editor) RemoveLastAttachment() bool {
	if len(e.attachments) == 0 {
		return false
	}
	mention := e.attachmentMentions[len(e.attachmentMentions)-1]
	e.attachments = e.attachments[:len(e.attachments)-1]
	e.attachmentMentions = e.attachmentMentions[:len(e.attachmentMentions)-1]
	if index := strings.LastIndex(e.ta.Value(), mention); index >= 0 {
		value := e.ta.Value()
		e.ta.SetValue(value[:index] + value[index+len(mention):])
	}
	e.viewCached = false
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
	// Drop the current-line highlight; it inherits Base's background instead.
	st.Focused.CursorLine = lipgloss.NewStyle()
	st.Blurred.CursorLine = lipgloss.NewStyle()
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
	e.viewCached = false
	return e.ta.Focus()
}

// Blur removes focus from the textarea, hiding the cursor and making it ignore
// input. Used while another surface (a pending tool approval) owns the composer.
func (e *Editor) Blur() {
	e.viewCached = false
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
	e.viewCached = false
}

// SetPlaceholder sets the hint shown while the input is empty.
func (e *Editor) SetPlaceholder(s string) {
	if e.ta.Placeholder != s {
		e.ta.Placeholder = s
		e.viewCached = false
	}
}

// Value returns the current input text.
func (e *Editor) Value() string { return e.ta.Value() }

// Reset clears the input and closes the menu.
func (e *Editor) Reset() {
	e.ta.Reset()
	e.attachments = nil
	e.attachmentMentions = nil
	e.remoteItems = nil
	e.remoteState = RemoteIdle
	e.dismissedMentions = nil
	e.dismissedValue = ""
	e.closeMenu()
	e.viewCached = false
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
			e.dismissActiveMention()
			e.closeMenu()
			return nil
		}
	}
	e.viewCached = false
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(msg)
	if e.dismissedValue != "" && e.ta.Value() != e.dismissedValue {
		e.dismissedValue = ""
	}
	e.reconcileAttachments()
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
	textareaView := e.ta.View()
	if e.width <= 0 {
		e.view = e.inputStyle.Block.Render(textareaView)
	} else {
		e.view = e.inputStyle.Block.Width(e.width).Render(textareaView)
	}
	e.viewHeight = lipgloss.Height(e.view)
	e.viewCached = true
}

// ContentOffset returns the number of cells from the editor's left edge to its
// text content. Parents use it to align overlays with the input text.
func (e *Editor) ContentOffset() int { return e.inputStyle.ContentOffset() }

// MenuView renders the completion menu as an opaque, fixed-width block, or ""
// when closed. The parent floats it above the input; giving every row a
// background makes it read as a popup over the transcript rather than letting
// transcript text show through the gaps.
func (e *Editor) MenuView() string {
	if !e.menu.open {
		return ""
	}
	w := e.menuWidth()
	parts := make([]string, 0, 2)
	if e.menu.selector != nil && len(e.menu.items) > 0 {
		parts = append(parts, e.menu.selector.View(w))
	}
	if e.menu.status != "" {
		parts = append(parts, e.styles.MenuItem.Width(w).Render(ansi.Truncate("  "+e.menu.status, w, "…")))
	}
	return strings.Join(parts, "\n")
}

func (e *Editor) menuWidth() int {
	w := 0
	for _, it := range e.menu.items {
		w = max(w, ansi.StringWidth(it.Label)+ansi.StringWidth(it.Detail)+4)
	}
	w = max(w, ansi.StringWidth(e.menu.status)+2)
	if e.widthSet {
		w = min(w, max(1, e.width-e.ContentOffset()))
	}
	return min(max(w, 12), menuMaxWidth)
}

// recompute refreshes the menu from the word at the cursor. An empty candidate
// set (no "@"/"/" trigger, or nothing matched) closes the menu; the selection is
// kept when it still points at a valid item.
func (e *Editor) recompute() {
	word := e.ta.Word()
	if strings.HasPrefix(word, "/") {
		if !e.commandTriggerActive(word) {
			e.closeMenu()
			return
		}
		e.setMenu(FakeCommands(word[1:]), "")
		return
	}
	span, active := e.activeEntitySpan()
	if !active {
		e.closeMenu()
		return
	}
	items := FileCandidates(span.query)
	state := RemoteIdle
	if span.query == e.remoteQuery {
		items = append(items, e.remoteItems...)
		state = e.remoteState
	}
	if len(items) > maxMenuCandidates {
		items = items[:maxMenuCandidates]
	}
	status := ""
	switch state {
	case RemoteIdle:
	case RemoteLoading:
		status = "Searching Datadog…"
	case RemoteError:
		status = "Datadog search unavailable"
	case RemoteReady:
		if len(items) == 0 {
			status = "No matching files or Datadog entities"
		}
	}
	e.setMenu(items, status)
}

func (e *Editor) setMenu(items []Candidate, status string) {
	previous := 0
	if e.menu.selector != nil {
		previous = e.menu.selector.Index()
	}
	choices := make([]components.Choice, len(items))
	for i, item := range items {
		choices[i] = components.Choice{Label: item.Label, Detail: item.Detail}
	}
	selector := components.NewSelector(choices, e.selectorStyles())
	selector.SetCompactDetail(true)
	selector.SetFillWidth(true)
	selector.SetIndex(previous)
	e.menu = menu{open: len(items) > 0 || status != "", items: items, selector: selector, status: status}
}

func (e *Editor) selectorStyles() styles.Selector {
	return styles.Selector{
		Item: e.styles.MenuItem, Selected: e.styles.MenuSelected,
		Detail: e.styles.MenuItem, SelectedDetail: e.styles.MenuSelected,
		Marker: "  ", SelectedMarker: "› ", ColumnGap: 2,
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

	lines := strings.Split(e.ta.Value(), "\n")
	row := e.ta.Line()
	if row < 0 || row >= len(lines) {
		e.closeMenu()
		return
	}
	runes := []rune(lines[row])
	col := min(max(e.ta.Column(), 0), len(runes))
	start, end := wordBounds(runes, col)
	if candidate.Kind != CandidateCommand {
		if span, ok := e.activeEntitySpan(); ok {
			start, end = span.start, span.end
		}
	}

	repl := []rune(insert + " ")
	lines[row] = string(runes[:start]) + string(repl) + string(runes[end:])
	e.ta.SetValue(strings.Join(lines, "\n"))
	e.viewCached = false
	if row == len(lines)-1 {
		e.ta.SetCursorColumn(start + len(repl))
	}
	e.dismissedMentions = append(e.dismissedMentions, dismissedMention{text: insert})
	if candidate.Attachment != nil {
		e.addAttachment(*candidate.Attachment, insert)
	}
	e.closeMenu()
}

func (e *Editor) closeMenu() { e.menu = menu{} }

func (e *Editor) addAttachment(attachment Attachment, mention string) {
	for _, existing := range e.attachments {
		if existing.Type == attachment.Type && existing.ID == attachment.ID {
			return
		}
	}
	e.attachments = append(e.attachments, attachment)
	e.attachmentMentions = append(e.attachmentMentions, mention)
	e.viewCached = false
}

func (e *Editor) reconcileAttachments() {
	value := e.ta.Value()
	attachments := e.attachments[:0]
	mentions := e.attachmentMentions[:0]
	for i, attachment := range e.attachments {
		if i >= len(e.attachmentMentions) || !strings.Contains(value, e.attachmentMentions[i]) {
			continue
		}
		attachments = append(attachments, attachment)
		mentions = append(mentions, e.attachmentMentions[i])
	}
	e.attachments = attachments
	e.attachmentMentions = mentions
}

type entitySpan struct {
	start int
	end   int
	query string
}

type dismissedMention struct {
	text string
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
	for i := col - 1; i >= 0; i-- {
		if runes[i] != '@' {
			continue
		}
		if i > 0 && (unicode.IsLetter(runes[i-1]) || unicode.IsDigit(runes[i-1]) || runes[i-1] == '_') {
			continue
		}
		dismissed := false
		for _, location := range e.dismissedMentions {
			end := i + len([]rune(location.text))
			if end <= len(runes) && string(runes[i:end]) == location.text {
				dismissed = true
				break
			}
		}
		if dismissed {
			continue
		}
		return entitySpan{start: i, end: col, query: string(runes[i+1 : col])}, true
	}
	return entitySpan{}, false
}

func (e *Editor) dismissActiveMention() {
	if _, ok := e.activeEntitySpan(); ok {
		e.dismissedValue = e.ta.Value()
	}
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
