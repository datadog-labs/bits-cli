package chat

import (
	"image/color"

	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
)

// UI color tokens, mirroring the design system's CSS custom properties. Tokens
// that differ by terminal background carry Dark/Light variants; the renderers
// pick the right one via lipgloss.LightDark.
const (
	uiAIPrimary = "#5e6dd6" // --ui-ai-primary (same in both modes)

	uiAISecondaryDark  = "#3f4ca5" // --ui-ai-secondary (dark mode)
	uiAISecondaryLight = "#1d2140" // --ui-ai-secondary (light mode)

	uiLinkDark  = "#3d8bd0" // link color (dark mode) — rgb(61, 139, 208)
	uiLinkLight = "#006bc2" // link color (light mode) — rgb(0, 107, 194)

	uiCodeBgDark    = "#343336" // inline code background (dark mode)
	uiCodeBgLight   = "#EEEFF0" // inline code background (light mode)
	uiCodeTextDark  = "#CECECE" // inline code text (dark mode)
	uiCodeTextLight = "#1C2E38" // inline code text (light mode)

	uiStatusSuccessDark    = "#349C50" // tool success (dark mode)
	uiStatusSuccessLight   = "#41C464" // tool success (light mode)
	uiStatusSuccessBgDark  = "#0D2714" // tool success background (dark mode)
	uiStatusSuccessBgLight = "#ECF9EF" // tool success background (light mode)
	uiStatusErrorDark      = "#D33043" // tool error (dark mode)
	uiStatusErrorLight     = "#EB364B" // tool error (light mode)
	uiStatusErrorBgDark    = "#2F0A0F" // tool error background (dark mode)
	uiStatusErrorBgLight   = "#FDEBED" // tool error background (light mode)
)

// Styles holds the lipgloss styles the renderers use. Construct one with
// DefaultStyles(isDark) and pass it into Item.Render.
type Styles struct {
	MarkdownStyle string // glamour style name for assistant text ("dark"/"light")

	UserMarker    lipgloss.Style
	UserText      lipgloss.Style
	AssistantText lipgloss.Style
	Reasoning     lipgloss.Style
	ToolName      lipgloss.Style
	ToolDetail    lipgloss.Style
	StatusRunning lipgloss.Style
	StatusSuccess lipgloss.Style
	StatusError   lipgloss.Style
	Meta          lipgloss.Style // fallback labels, separators
	NoticeInfo    lipgloss.Style // solid notification bar: informational
	NoticeWarn    lipgloss.Style // solid notification bar: warning
	NoticeError   lipgloss.Style // solid notification bar: error

	// Markdown token colors mirror the values datadogStyleConfig feeds to
	// glamour. The renderer does not read these fields (markdown is styled via
	// datadogStyleConfig); they exist so the glamour-side tokens are inspectable
	// alongside the lipgloss ones, e.g. in the style catalog.
	MarkdownHeading color.Color // heading secondary color (--ui-ai-secondary)
	MarkdownLink    color.Color // link color
	MarkdownCodeFg  color.Color // inline code foreground
	MarkdownCodeBg  color.Color // inline code background
}

// Notice returns the style for a transient notice of the given level.
func (s Styles) Notice(level NoticeLevel) lipgloss.Style {
	switch level {
	case NoticeError:
		return s.NoticeError
	case NoticeWarn:
		return s.NoticeWarn
	default:
		return s.NoticeInfo
	}
}

// DefaultStyles returns the palette for the given terminal background.
func DefaultStyles(isDark bool) Styles {
	c := lipgloss.LightDark(isDark)
	aiPrimary := lipgloss.Color(uiAIPrimary)

	// Select the markdown token hex per mode, mirroring datadogStyleConfig so the
	// exposed colors stay in lockstep with what glamour actually renders.
	mdSecondary, mdLink, mdCodeText, mdCodeBg := uiAISecondaryLight, uiLinkLight, uiCodeTextLight, uiCodeBgLight
	if isDark {
		mdSecondary, mdLink, mdCodeText, mdCodeBg = uiAISecondaryDark, uiLinkDark, uiCodeTextDark, uiCodeBgDark
	}

	return Styles{
		MarkdownStyle: markdownStyleName(isDark),

		UserMarker:    lipgloss.NewStyle().Bold(true).Foreground(aiPrimary),
		UserText:      lipgloss.NewStyle(),
		AssistantText: lipgloss.NewStyle(),
		Reasoning:     lipgloss.NewStyle().Faint(true).Italic(true),
		ToolName:      lipgloss.NewStyle().Bold(true).Foreground(aiPrimary),
		ToolDetail:    lipgloss.NewStyle().Foreground(lipgloss.Color("#666666")).Faint(true),
		StatusRunning: lipgloss.NewStyle().Foreground(c(lipgloss.Color("3"), lipgloss.Color("11"))),
		StatusSuccess: lipgloss.NewStyle().Foreground(c(lipgloss.Color(uiStatusSuccessLight), lipgloss.Color(uiStatusSuccessDark))).Background(c(lipgloss.Color(uiStatusSuccessBgLight), lipgloss.Color(uiStatusSuccessBgDark))),
		StatusError:   lipgloss.NewStyle().Foreground(c(lipgloss.Color(uiStatusErrorLight), lipgloss.Color(uiStatusErrorDark))).Background(c(lipgloss.Color(uiStatusErrorBgLight), lipgloss.Color(uiStatusErrorBgDark))),
		Meta:          lipgloss.NewStyle().Faint(true),
		NoticeInfo:    lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#632CA6")),
		NoticeWarn:    lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color("#1A1A1A")).Background(lipgloss.Color("#F5A623")),
		NoticeError:   lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#C4314B")),

		MarkdownHeading: lipgloss.Color(mdSecondary),
		MarkdownLink:    lipgloss.Color(mdLink),
		MarkdownCodeFg:  lipgloss.Color(mdCodeText),
		MarkdownCodeBg:  lipgloss.Color(mdCodeBg),
	}
}

func markdownStyleName(isDark bool) string {
	if isDark {
		return styles.DarkStyle
	}
	return styles.LightStyle
}

// datadogStyleConfig derives a glamour style from the built-in dark/light base,
// recoloring headings, links, and inline code with the UI tokens. It copies the
// base by value and only reassigns pointer fields, so the shared built-in config
// is never mutated.
func datadogStyleConfig(isDark bool) ansi.StyleConfig {
	cfg := styles.LightStyleConfig
	secondary := uiAISecondaryLight
	link := uiLinkLight
	codeBg := uiCodeBgLight
	codeText := uiCodeTextLight
	if isDark {
		cfg = styles.DarkStyleConfig
		secondary = uiAISecondaryDark
		link = uiLinkDark
		codeBg = uiCodeBgDark
		codeText = uiCodeTextDark
	}

	// Headings use the secondary color. H2–H5 inherit Heading via cascade; H6
	// carries its own color in the dark base, so it needs an explicit override.
	cfg.Heading.Color = new(secondary)
	cfg.H6.Color = new(secondary)
	cfg.H1.Color = new("#FFFFFF")
	cfg.H1.BackgroundColor = new(uiAIPrimary)
	// Color both the URL (Link) and the visible label (LinkText); glamour uses
	// separate fields, so setting only Link leaves the label at the base color.
	cfg.Link.Color = new(link)
	cfg.LinkText.Color = new(link)
	cfg.Code.Color = new(codeText)
	cfg.Code.BackgroundColor = new(codeBg)

	return cfg
}
