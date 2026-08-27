package styles

import (
	"charm.land/bubbles/v2/textinput"
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
		Marker: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.primary)).Background(lipgloss.Color(p.surface)),
		Text:   lipgloss.NewStyle().Background(lipgloss.Color(p.surface)),
	}

	text := Text{
		Body:  lipgloss.NewStyle().Foreground(lipgloss.Color(p.text)),
		Muted: lipgloss.NewStyle().Foreground(lipgloss.Color(p.muted)),
		Help:  lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color(p.muted)),
	}
	feedback := Feedback{
		Progress: lipgloss.NewStyle().Foreground(lipgloss.Color(p.primary)),
		Error:    lipgloss.NewStyle().Foreground(lipgloss.Color(p.error)),
		Success:  lipgloss.NewStyle().Foreground(lipgloss.Color(p.success)),
	}
	panel := Panel{
		Frame: lipgloss.NewStyle().
			Foreground(lipgloss.Color(p.text)).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(p.borderSubtle)).
			Padding(1, 2),
		Title:            text.Body,
		Dismiss:          text.Help,
		Compact:          text.Body,
		Help:             text.Help,
		MaxWidth:         112,
		HorizontalMargin: 2,
		CompactMaxWidth:  34,
		SectionGap:       1,
		FooterSeparator:  "   ",
	}
	selector := Selector{
		Item:           text.Body,
		Selected:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.primary)),
		Detail:         text.Help,
		SelectedDetail: text.Body,
		Marker:         "  ",
		SelectedMarker: "› ",
		ColumnGap:      2,
	}
	textInput := textinput.DefaultStyles(isDark)
	textInput.Focused.Text = text.Body
	textInput.Focused.Placeholder = text.Help
	textInput.Focused.Suggestion = text.Help
	textInput.Focused.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color(p.primary))
	textInput.Blurred.Text = text.Body
	textInput.Blurred.Placeholder = text.Help
	textInput.Blurred.Suggestion = text.Help
	textInput.Blurred.Prompt = text.Muted
	textInput.Cursor.Color = lipgloss.Color(p.primary)

	return Theme{
		IsDark:    isDark,
		Input:     input,
		Text:      text,
		Feedback:  feedback,
		Panel:     panel,
		Selector:  selector,
		TextInput: textInput,
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
