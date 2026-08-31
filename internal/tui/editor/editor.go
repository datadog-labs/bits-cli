// Package editor is the chat input: a multiline textarea plus an @/ completion
// menu backed by a Completer. It owns key handling for menu navigation; the
// parent tui routes keys to it and reads Value on submit. No agent/chat
// dependency — it is pure input.
package editor

import (
	"strings"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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
)

// Editor is the chat input. The completion menu opens automatically when the
// token before the cursor begins with "@" or "/".
type Editor struct {
	ta       textarea.Model
	complete Completer
	styles   styles.Editor
	menu     menu

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
	selected int
}

// New returns a chat editor using the built-in fake completer. Call Focus to
// start the cursor and receive its blink command.
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
		complete:   Dispatch,
		styles:     defaultStyles.Editor,
		inputStyle: defaultStyles.Input,
	}
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
func (e *Editor) SetStyles(menuStyles styles.Editor) { e.styles = menuStyles }

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
	e.closeMenu()
	e.viewCached = false
}

// MenuOpen reports whether the completion menu is showing. The parent uses this
// to decide whether Enter/Esc drive the menu or submit/cancel.
func (e *Editor) MenuOpen() bool { return e.menu.open }

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
	if k, ok := msg.(tea.KeyPressMsg); ok && e.menu.open {
		switch k.String() {
		case "up", "ctrl+p":
			e.move(-1)
			return nil
		case "down", "ctrl+n":
			e.move(1)
			return nil
		case "tab", "enter":
			e.accept()
			return nil
		case "esc":
			e.closeMenu()
			return nil
		}
	}
	e.viewCached = false
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(msg)
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
	lines := make([]string, len(e.menu.items))
	for i, it := range e.menu.items {
		marker, st := "  ", e.styles.MenuItem
		if i == e.menu.selected {
			marker, st = "› ", e.styles.MenuSelected
		}
		lines[i] = st.Width(w).Render(ansi.Truncate(marker+it.Label, w, "…"))
	}
	return strings.Join(lines, "\n")
}

func (e *Editor) menuWidth() int {
	w := 0
	for _, it := range e.menu.items {
		w = max(w, ansi.StringWidth(it.Label)+2)
	}
	return min(max(w, 12), menuMaxWidth)
}

// recompute refreshes the menu from the word at the cursor. An empty candidate
// set (no "@"/"/" trigger, or nothing matched) closes the menu; the selection is
// kept when it still points at a valid item.
func (e *Editor) recompute() {
	items := e.complete(e.ta.Word())
	if len(items) == 0 {
		e.closeMenu()
		return
	}
	sel := 0
	if e.menu.open && e.menu.selected < len(items) {
		sel = e.menu.selected
	}
	e.menu = menu{open: true, items: items, selected: sel}
}

func (e *Editor) move(delta int) {
	n := len(e.menu.items)
	if n == 0 {
		return
	}
	e.menu.selected = (e.menu.selected + delta + n) % n
}

// accept replaces the word at the cursor with the selected candidate's insert
// text plus a trailing space, then closes the menu. The cursor lands after the
// inserted space when the edit is on the final line (the common single-line
// case); otherwise it falls back to the buffer end.
func (e *Editor) accept() {
	if !e.menu.open || len(e.menu.items) == 0 {
		return
	}
	insert := e.menu.items[e.menu.selected].Insert

	lines := strings.Split(e.ta.Value(), "\n")
	row := e.ta.Line()
	if row < 0 || row >= len(lines) {
		e.closeMenu()
		return
	}
	runes := []rune(lines[row])
	col := min(max(e.ta.Column(), 0), len(runes))
	start, end := wordBounds(runes, col)

	repl := []rune(insert + " ")
	lines[row] = string(runes[:start]) + string(repl) + string(runes[end:])
	e.ta.SetValue(strings.Join(lines, "\n"))
	e.viewCached = false
	if row == len(lines)-1 {
		e.ta.SetCursorColumn(start + len(repl))
	}
	e.closeMenu()
}

func (e *Editor) closeMenu() { e.menu = menu{} }

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
