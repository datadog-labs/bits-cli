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

	// Chip styles the attachment chips shown above the prompt, both in the
	// composer and in submitted user blocks.
	Chip Chip
}

// Chip styles one attachment chip. Every part carries the chip background
// because each is rendered separately and its reset would clear an outer one.
type Chip struct {
	Frame  lipgloss.Style
	Type   lipgloss.Style
	Label  lipgloss.Style
	Remove lipgloss.Style
	// Active replaces Label and Remove on a hovered or focused chip.
	Active lipgloss.Style
	// Gap paints the cells between chips with the surrounding surface.
	Gap lipgloss.Style
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

	Meta lipgloss.Style

	// NoticeInfo, NoticeWarn and NoticeError style a notice's leading marker;
	// NoticeText styles the message beside it, which carries no level of its own.
	NoticeInfo  lipgloss.Style
	NoticeWarn  lipgloss.Style
	NoticeError lipgloss.Style
	// NoticeDone marks a finished response's timestamp line. Deliberately not
	// bold like the others — it should recede, not compete with tool headers.
	NoticeDone lipgloss.Style
	NoticeText lipgloss.Style

	// FollowBorder mirrors Theme.FollowBorder: the chat package renders the
	// done row's fork button with the same resting border as the jump-to-latest
	// control, but cannot reach Theme from here, so the role is duplicated onto
	// Chat rather than given a second palette entry.
	FollowBorder color.Color
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

// Text exposes the palette's foreground levels and link treatment to components
// outside the transcript. The levels are ordered by how much attention the
// text should draw; see the palette for what each one is for.
type Text struct {
	Primary   lipgloss.Style
	Secondary lipgloss.Style
	Tertiary  lipgloss.Style
	Link      lipgloss.Style
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
	// FullAccess is the focused fill for the "Switch to full access" choice,
	// distinct from Selected so it reads as a different kind of commitment.
	FullAccess lipgloss.Style
	// Disclaimer styles the one-row warning shown while full access is
	// focused.
	Disclaimer lipgloss.Style
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

	// DetailHoverBackground paints a block's rows below its header (compact
	// preview or full disclosure) while any row of it is hovered. Softer than HoverBackground so the header
	// stays distinct from its content.
	DetailHoverBackground color.Color
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

	// FollowBorder is the resting border of the jump-to-latest control, derived
	// from the accent the control takes on hover rather than from the panel edge.
	FollowBorder color.Color

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
	Selection   Selection
}

// Selection paints cells the pointer has dragged over. Both colors are stated
// so the selection renders from the theme rather than from terminal defaults.
type Selection struct {
	Foreground color.Color
	Background color.Color
}

// Default returns the complete UI theme for a terminal background mode.
func Default(isDark bool) Theme {
	if isDark {
		return build(true, darkPalette())
	}
	return build(false, lightPalette())
}

// FollowButtonStyle is the rounded-border control shared by the jump-to-
// latest overlay and the done row's fork button: marker foreground, border
// at rest, border→marker plus bold when active (hovered or pressed). Content
// padding is the caller's job, by including the blank cells in what it
// renders, so both callers pad identically and nothing here depends on
// lipgloss's own Padding. A free function rather than a Theme method alone
// because chat.Styles, which has no Theme, needs it too.
func FollowButtonStyle(marker lipgloss.Style, border color.Color, active bool) lipgloss.Style {
	style := lipgloss.NewStyle().
		Foreground(marker.GetForeground()).
		Border(lipgloss.RoundedBorder())
	borderColor := border
	if active {
		borderColor = marker.GetForeground()
		style = style.Bold(true)
	}
	return style.BorderForeground(borderColor)
}

// FollowButton is FollowButtonStyle applied to this theme's own marker and
// FollowBorder.
func (t Theme) FollowButton(active bool) lipgloss.Style {
	return FollowButtonStyle(t.Input.Marker, t.FollowBorder, active)
}
