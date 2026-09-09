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
	Block      lipgloss.Style
	Marker     lipgloss.Style

	// Text styles what the user wrote, at both moments it is on screen: the live
	// line in the editor and the submitted block in the transcript. One style
	// serves both so a message does not change color on its way past Enter.
	Text lipgloss.Style
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
	// style catalog. Compact tool rendering uses StatusSpinner and group dots;
	// an approval wait is static.
	StatusRunningLabel  Shimmer
	StatusAwaitingLabel Shimmer

	// StatusSweepDim and StatusSweepHot are catalog-only sweep colors.
	StatusSweepDim color.Color
	StatusSweepHot color.Color

	// StatusSpinner is the glyph cycle shown in place of the static dot while a
	// tool is running. It reports work in progress, so it is deliberately not
	// used for a tool awaiting approval, which is blocked rather than busy.
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
	MenuItem     lipgloss.Style
	MenuSelected lipgloss.Style
}

// Text contains shared semantic text roles used outside the transcript.
type Text struct {
	Body  lipgloss.Style
	Muted lipgloss.Style
	Help  lipgloss.Style
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
	FooterSeparator  string
}

// Approval styles the docked tool-approval block. It mirrors the input block's
// shape (top and bottom rules, a filled background) on a distinct surface so it
// reads as an attention-seeking sibling of the editor that pushes the
// transcript up, rather than an overlay that hides it. Every text role carries
// the block background so inner spans blend into the fill.
type Approval struct {
	Block    lipgloss.Style // outer surface + rules; width applied at render
	Marker   lipgloss.Style // accent prompt glyph
	Title    lipgloss.Style // emphasized header ("Approval required")
	Text     lipgloss.Style // normal body (the tool prompt)
	Detail   lipgloss.Style // muted secondary (detail, help)
	Action   lipgloss.Style // unselected choice
	Selected lipgloss.Style // focused choice
	Prompt   string
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

// Theme is the complete set of styles for one terminal background mode.
type Theme struct {
	IsDark bool

	// Background is painted across the whole alt-screen by the root views, so
	// the UI composes against a known color instead of the user's terminal
	// theme. Bubble Tea emits the escape sequence itself when a view sets it and
	// restores the terminal's own background on teardown.
	//
	// This is unconditional today. Crush, which this follows, lets users opt out
	// via an `options.tui.transparent` config field that leaves the view's
	// background unset; bits-cli has no user/client settings layer yet, so there
	// is nowhere to hang that switch. When one lands, expose the same choice
	// rather than assuming everyone wants a painted background — a terminal with
	// a deliberate theme or a transparent window has a real claim to show it.
	Background color.Color

	Input     Input
	Chat      Chat
	Editor    Editor
	Text      Text
	Feedback  Feedback
	Panel     Panel
	Approval  Approval
	Selector  Selector
	TextInput textinput.Styles
}

// Default returns the complete UI theme for a terminal background mode.
func Default(isDark bool) Theme {
	if isDark {
		return build(true, darkPalette())
	}
	return build(false, lightPalette())
}
