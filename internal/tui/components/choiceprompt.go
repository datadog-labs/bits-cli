package components

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/tui/styles"
	"github.com/datadog-labs/bits-cli/internal/tui/textwrap"
)

// choicePromptMinWidth leaves the descriptions a few words per row.
const choicePromptMinWidth = 36

// choiceDetailMinWidth is the narrowest a description may wrap beside its
// label; narrower, descriptions go under their labels.
const choiceDetailMinWidth = 24

// currentSuffix marks the option in effect.
const currentSuffix = " (current)"

// ChoicePrompt asks the user to pick one of a few options, each described in
// full rather than truncated, below an optional intro. Enter picks the
// highlighted option; esc dismisses the prompt.
type ChoicePrompt struct {
	title     string
	intro     string
	options   []Option
	current   int // the option in effect, or -1
	cursor    int
	styles    styles.Theme
	dismissed bool
	complete  bool
}

// NewChoicePrompt creates a prompt highlighting options[selected].
func NewChoicePrompt(title, intro string, options []Option, selected int) *ChoicePrompt {
	return &ChoicePrompt{title: title, intro: intro, options: options, current: -1, cursor: max(0, min(selected, len(options)-1))}
}

// SetCurrent marks options[index] as the one in effect. The label column
// keeps room for the mark on every option, so moving it shifts nothing.
func (p *ChoicePrompt) SetCurrent(index int) { p.current = index }

// SetStyles replaces the prompt's theme-derived styles.
func (p *ChoicePrompt) SetStyles(theme styles.Theme) { p.styles = theme }

// Placement puts the prompt in place of the composer.
func (p *ChoicePrompt) Placement() Placement { return ReplacesInput }

// Result reports the picked option's index once the user picks or dismisses.
func (p *ChoicePrompt) Result() (index int, dismissed, done bool) {
	if !p.complete || p.dismissed {
		return 0, p.dismissed, p.complete
	}
	return p.cursor, false, true
}

// Update applies keys and reports whether the prompt used them. Pointer
// events are left to the host.
func (p *ChoicePrompt) Update(msg tea.Msg) (tea.Cmd, bool) {
	if p.complete {
		return nil, true
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	switch key.String() {
	case "esc":
		p.dismissed, p.complete = true, true
	case "enter":
		p.complete = true
	case "up", "k", "shift+tab":
		p.cursor = (p.cursor - 1 + len(p.options)) % len(p.options)
	case "down", "j", "tab":
		p.cursor = (p.cursor + 1) % len(p.options)
	}
	return nil, true
}

// Layout renders the prompt across the slot's width. It is answerable when
// the slot fits every option and the controls.
func (p *ChoicePrompt) Layout(slot Slot) (string, bool) {
	width := max(1, slot.Width-4)
	sty := p.styles.Selector
	marker := max(ansi.StringWidth(sty.Marker), ansi.StringWidth(sty.SelectedMarker))
	label := 0
	for _, option := range p.options {
		label = max(label, ansi.StringWidth(option.Label))
	}
	if p.current >= 0 {
		label += ansi.StringWidth(currentSuffix)
	}
	column := marker + label + max(1, sty.ColumnGap)
	stacked := width-column < choiceDetailMinWidth

	lines := []string{
		p.styles.Text.Primary.Bold(true).Render(ansi.Truncate(p.title, width, "…")),
		p.styles.Text.Tertiary.Render(strings.Repeat("─", width)),
	}
	if p.intro != "" {
		lines = append(append(lines, textwrap.WrapProse(width, p.intro, p.styles.Text.Secondary)...), "")
	}
	for i, option := range p.options {
		labelStyle, detailStyle, mark := sty.Item, sty.Detail, sty.Marker
		if i == p.cursor {
			labelStyle, detailStyle, mark = sty.Selected, sty.SelectedDetail, sty.SelectedMarker
		}
		name := option.Label
		if i == p.current {
			name += currentSuffix
		}
		if i > 0 {
			lines = append(lines, "")
		}
		wrap := textwrap.Options{
			Width:       width,
			FirstPrefix: labelStyle.Width(column).Render(mark + name),
			RestPrefix:  strings.Repeat(" ", column),
			Breakpoints: textwrap.Prose,
			TrimSpace:   true,
		}
		if stacked {
			lines = append(lines, labelStyle.Render(mark+name))
			if option.Description == "" {
				continue
			}
			wrap.FirstPrefix, wrap.RestPrefix = strings.Repeat(" ", marker), strings.Repeat(" ", marker)
		}
		lines = append(lines, textwrap.Wrap(wrap, textwrap.Styled(option.Description, detailStyle))...)
	}
	help := "↑↓ choose · enter confirm · esc cancel"
	lines = append(lines, "", p.styles.Text.Tertiary.Render(ansi.Truncate(help, width, "…")))
	view := lipgloss.NewStyle().Padding(0, 2).Render(strings.Join(lines, "\n"))
	return view, slot.Width >= choicePromptMinWidth && slot.Height >= len(lines)
}
