package components

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

const (
	textPromptMinWidth = 24
	textPromptHeight   = 5 // title, rule, input, gap, and help row
)

// TextPrompt asks for one line of text. Enter submits a value that is not
// blank; esc dismisses the prompt.
type TextPrompt struct {
	title     string
	input     textinput.Model
	styles    styles.Theme
	value     string
	dismissed bool
	complete  bool
}

// NewTextPrompt creates a focused prompt holding value. A positive limit caps
// the input at that many characters.
func NewTextPrompt(title, value string, limit int) *TextPrompt {
	input := textinput.New()
	input.Prompt = ""
	input.SetVirtualCursor(true)
	input.CharLimit = max(0, limit)
	input.SetValue(value)
	input.CursorEnd()
	input.Focus()
	return &TextPrompt{title: title, input: input}
}

// SetPlaceholder sets the text shown while the input is empty.
func (p *TextPrompt) SetPlaceholder(placeholder string) { p.input.Placeholder = placeholder }

// SetStyles replaces the prompt's theme-derived styles.
func (p *TextPrompt) SetStyles(theme styles.Theme) { p.styles = theme }

// Placement puts the prompt in place of the composer.
func (p *TextPrompt) Placement() Placement { return ReplacesInput }

// Result reports the trimmed value once the user submits or dismisses.
func (p *TextPrompt) Result() (value string, dismissed, done bool) {
	return p.value, p.dismissed, p.complete
}

// Update applies input and reports whether the prompt used it. Pointer events
// are left to the host.
func (p *TextPrompt) Update(msg tea.Msg) (tea.Cmd, bool) {
	if p.complete {
		return nil, true
	}
	switch msg := msg.(type) {
	case tea.MouseMsg:
		return nil, false
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc":
			p.dismissed, p.complete = true, true
			return nil, true
		case "enter":
			if value := strings.TrimSpace(p.input.Value()); value != "" {
				p.value, p.complete = value, true
			}
			return nil, true
		}
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd, true
}

// Layout renders the prompt across the slot's width. It is answerable when
// the slot fits the input and its controls.
func (p *TextPrompt) Layout(slot Slot) (string, bool) {
	width := max(1, slot.Width-4)
	// textinput's width excludes its cursor cell.
	if inputWidth := max(1, width-1); p.input.Width() != inputWidth {
		position := p.input.Position()
		p.input.SetWidth(inputWidth)
		p.input.CursorEnd()
		p.input.SetCursor(position)
	}
	p.input.SetStyles(p.styles.TextInput)

	help := "enter save · esc cancel"
	lines := []string{
		p.styles.Text.Primary.Bold(true).Render(ansi.Truncate(p.title, width, "…")),
		p.styles.Text.Tertiary.Render(strings.Repeat("─", width)),
		p.input.View(),
		"",
		p.styles.Text.Tertiary.Render(ansi.Truncate(help, width, "…")),
	}
	view := lipgloss.NewStyle().Padding(0, 2).Render(strings.Join(lines, "\n"))
	return view, slot.Width >= textPromptMinWidth && slot.Height >= textPromptHeight
}
