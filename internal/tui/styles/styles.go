// Package styles owns the terminal UI's visual language.
package styles

import (
	"image/color"

	glamouransi "charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const prompt = "› "

// Input is the common visual contract for the active editor and submitted user
// messages.
type Input struct {
	Prompt     string
	Background color.Color
	Block      lipgloss.Style
	Marker     lipgloss.Style
	Text       lipgloss.Style
}

// PromptWidth returns the number of cells occupied by the input prompt.
func (s Input) PromptWidth() int { return ansi.StringWidth(s.Prompt) }

// ContentOffset returns the number of cells from the block's left edge to the
// first text cell. Overlay content uses it to align with typed input.
func (s Input) ContentOffset() int {
	return s.Block.GetPaddingLeft() + s.PromptWidth()
}

// Chat contains chat-specific styles derived from a Theme.
type Chat struct {
	Markdown        glamouransi.StyleConfig
	MarkdownHeading color.Color
	MarkdownLink    color.Color
	MarkdownCodeFg  color.Color
	MarkdownCodeBg  color.Color
	AssistantText   lipgloss.Style
	Reasoning       lipgloss.Style
	ToolName        lipgloss.Style
	ToolDetail      lipgloss.Style
	StatusRunning   lipgloss.Style
	StatusSuccess   lipgloss.Style
	StatusError     lipgloss.Style
	Meta            lipgloss.Style
	NoticeInfo      lipgloss.Style
	NoticeWarn      lipgloss.Style
	NoticeError     lipgloss.Style
}

// Editor contains editor-specific styles derived from a Theme.
type Editor struct {
	MenuItem     lipgloss.Style
	MenuSelected lipgloss.Style
}

// Theme is the complete set of styles for one terminal background mode.
type Theme struct {
	IsDark bool
	Input  Input
	Chat   Chat
	Editor Editor
}

// Default returns the complete UI theme for a terminal background mode.
func Default(isDark bool) Theme {
	if isDark {
		return build(true, darkPalette())
	}
	return build(false, lightPalette())
}
