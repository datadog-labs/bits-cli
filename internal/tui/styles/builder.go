package styles

import (
	glamouransi "charm.land/glamour/v2/ansi"
	glamourstyles "charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
)

// inputRule draws the input block's top and bottom edges with one-eighth block
// glyphs. Box-drawing "─" sits mid-cell, so it reads as a separate rule row
// floating on the transcript background; these glyphs ink only the outer eighth
// of the row and leave the rest to the border background (the input surface).
// The row is therefore both the block's edge and its vertical breathing room —
// it replaces the padding it used to have instead of adding height.
var inputRule = lipgloss.Border{Top: "▔", Bottom: "▁"}

// build turns semantic palette roles into the concrete styles components use.
func build(isDark bool, p palette) Theme {
	input := Input{
		Prompt:     prompt,
		Background: lipgloss.Color(p.surface),
		Block: lipgloss.NewStyle().Background(lipgloss.Color(p.surface)).
			Border(inputRule, true, false, true, false).
			BorderForeground(lipgloss.Color(p.inputRule)).
			BorderBackground(lipgloss.Color(p.surface)),
		Marker:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.primary)).Background(lipgloss.Color(p.surface)),
		Text:       lipgloss.NewStyle().Background(lipgloss.Color(p.surface)),
	}

	return Theme{
		IsDark: isDark,
		Input:  input,
		Chat: Chat{
			Markdown:        markdown(isDark, p),
			MarkdownHeading: lipgloss.Color(p.secondary),
			MarkdownLink:    lipgloss.Color(p.link),
			MarkdownCodeFg:  lipgloss.Color(p.codeText),
			MarkdownCodeBg:  lipgloss.Color(p.codeSurface),
			AssistantText:   lipgloss.NewStyle(),
			Reasoning:       lipgloss.NewStyle().Faint(true).Italic(true),
			ToolName:        lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.primary)),
			ToolDetail:      lipgloss.NewStyle().Foreground(lipgloss.Color(p.muted)).Faint(true),
			StatusRunning:   lipgloss.NewStyle().Foreground(lipgloss.Color(p.busy)),
			StatusSuccess:   lipgloss.NewStyle().Foreground(lipgloss.Color(p.success)).Background(lipgloss.Color(p.successSurface)),
			StatusError:     lipgloss.NewStyle().Foreground(lipgloss.Color(p.error)).Background(lipgloss.Color(p.errorSurface)),
			Meta:            lipgloss.NewStyle().Faint(true),
			NoticeInfo:      lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color(p.onAccent)).Background(lipgloss.Color(p.info)),
			NoticeWarn:      lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color(p.onWarning)).Background(lipgloss.Color(p.warning)),
			NoticeError:     lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color(p.onAccent)).Background(lipgloss.Color(p.critical)),
		},
		Editor: Editor{
			MenuItem:     lipgloss.NewStyle().Foreground(lipgloss.Color(p.text)).Background(lipgloss.Color(p.surfaceRaised)),
			MenuSelected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.onAccent)).Background(lipgloss.Color(p.primary)),
		},
	}
}

func markdown(isDark bool, p palette) glamouransi.StyleConfig {
	cfg := glamourstyles.LightStyleConfig
	if isDark {
		cfg = glamourstyles.DarkStyleConfig
	}
	cfg.Heading.Color = new(p.secondary)
	cfg.H6.Color = new(p.secondary)
	cfg.H1.Color = new(p.onAccent)
	cfg.H1.BackgroundColor = new(p.primary)
	cfg.Link.Color = new(p.link)
	cfg.LinkText.Color = new(p.link)
	cfg.Code.Color = new(p.codeText)
	cfg.Code.BackgroundColor = new(p.codeSurface)
	return cfg
}
