// Package styles owns the terminal UI's visual language.
package styles

import (
	"image/color"

	"charm.land/bubbles/v2/textinput"
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
	Cursor     color.Color
	Block      lipgloss.Style
	Marker     lipgloss.Style
	// Text styles what the user wrote, shared by the live editor line and the
	// submitted transcript block so it doesn't change color past Enter.
	Text lipgloss.Style

	// Placeholder styles the hint shown while the composer is empty. It is a
	// theme role rather than the textarea's own default because that default is
	// a hardcoded ANSI index, identical in both modes.
	Placeholder lipgloss.Style
	// SweepDim and SweepHot are the border-sweep animation's resting and peak
	// colors, used by editor.Editor while Bits is generating a response.
	// SweepMotion lets reduced-motion themes keep the ordinary static border.
	SweepDim    color.Color
	SweepHot    color.Color
	SweepMotion bool
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
	ToolArgument    lipgloss.Style
	ToolDetail      lipgloss.Style
	ToolError       lipgloss.Style
	Diff            Diff
	StatusRunning   lipgloss.Style
	StatusSuccess   lipgloss.Style
	StatusError     lipgloss.Style

	// StatusRunningLabel and StatusAwaitingLabel are retained for the dev-only
	// style catalog. Compact tool rendering uses StatusSpinner;
	// an approval wait is static.
	StatusRunningLabel  Shimmer
	StatusAwaitingLabel Shimmer

	// StatusSweepDim and StatusSweepHot are catalog-only sweep colors.
	StatusSweepDim color.Color
	StatusSweepHot color.Color

	// StatusSpinner is the shared glyph cycle shown in place of the static dot
	// while work is progressing (tools, thinking, or inspection groups). It is
	// deliberately not used for a tool awaiting approval, which is blocked
	// rather than busy.
	StatusSpinner Spinner

	Meta        lipgloss.Style
	NoticeInfo  lipgloss.Style
	NoticeWarn  lipgloss.Style
	NoticeError lipgloss.Style
}

type Diff struct {
	Add        lipgloss.Style
	Del        lipgloss.Style
	Context    lipgloss.Style
	Gutter     lipgloss.Style
	Meta       lipgloss.Style
	SyntaxDark bool
}

// Editor contains editor-specific styles derived from a Theme.
type Editor struct {
	MenuFrame          lipgloss.Style
	MenuItem           lipgloss.Style
	MenuDetail         lipgloss.Style
	MenuSelected       lipgloss.Style
	MenuSelectedDetail lipgloss.Style
	MenuHelp           lipgloss.Style
}

// Text exposes the palette's three foreground levels to components outside the
// transcript. The levels are ordered by how much attention the text should
// draw; see the palette for what each one is for.
type Text struct {
	Primary   lipgloss.Style
	Secondary lipgloss.Style
	Tertiary  lipgloss.Style
}

// Feedback contains shared progress and outcome roles.
type Feedback struct {
	Progress lipgloss.Style
	Error    lipgloss.Style
	Success  lipgloss.Style
}

// Panel styles the reusable understated bordered surface.
type Panel struct {
	Frame   lipgloss.Style
	Title   lipgloss.Style
	Dismiss lipgloss.Style
	Compact lipgloss.Style
	Help    lipgloss.Style

	MaxWidth         int
	HorizontalMargin int
	CompactMaxWidth  int
	SectionGap       int
}

// Approval styles the docked tool-approval panel and its horizontal actions.
type Approval struct {
	Panel    Panel
	Text     lipgloss.Style
	Detail   lipgloss.Style
	Action   lipgloss.Style
	Selected lipgloss.Style
}

// Accordion styles the disclosure control that expands and collapses a
// transcript block's detail rows.
type Accordion struct {
	// Expanded and Collapsed are the disclosure glyphs. Both are one cell wide
	// under the grapheme width model the transcript measures with, so the
	// control keeps a fixed width across states and never reflows the header.
	Expanded  string
	Collapsed string

	// Resting draws the control: a fixed background fill whose padding gives
	// the chevron its box.
	Resting lipgloss.Style

	// HoverBackground is painted across the whole header row, not just the
	// control's own cells, when that row is hovered. The caller (chat.List)
	// repaints an already-rendered line's background cell-by-cell rather than
	// wrapping it in a lipgloss style, since the line's own foreground colors
	// must survive the repaint.
	HoverBackground color.Color
}

// Selector styles the reusable two-column keyboard selector.
type Selector struct {
	Item           lipgloss.Style
	Selected       lipgloss.Style
	Detail         lipgloss.Style
	SelectedDetail lipgloss.Style
	Marker         string
	SelectedMarker string
	ColumnGap      int
}

// Tabs styles compact navigation with a filled active item.
type Tabs struct {
	Item   lipgloss.Style
	Active lipgloss.Style
}

// Theme is the complete set of styles for one terminal background mode.
type Theme struct {
	IsDark bool

	// Background is painted across the whole alt-screen by the root views,
	// instead of inheriting the user's terminal theme. Unconditional for now;
	// revisit if bits-cli ever gets a settings layer to opt out of it.
	Background color.Color

	// Logo is the brand accent for the startup wordmark: decorative, with no
	// state or interaction behind it.
	Logo lipgloss.Style

	Input       Input
	Chat        Chat
	Editor      Editor
	Text        Text
	Feedback    Feedback
	Panel       Panel
	Approval    Approval
	Permissions Panel
	Accordion   Accordion
	Selector    Selector
	TextInput   textinput.Styles
	Tabs        Tabs
}

// Default returns the complete UI theme for a terminal background mode.
func Default(isDark bool) Theme {
	if isDark {
		return build(true, darkPalette())
	}
	return build(false, lightPalette())
}
