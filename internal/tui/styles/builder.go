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
		Cursor:     lipgloss.Color(p.textPrimary),
		Block: lipgloss.NewStyle().Background(lipgloss.Color(p.surface)).
			Border(inputRule, true, false, true, false).
			BorderForeground(lipgloss.Color(p.inputRule)).
			BorderBackground(lipgloss.Color(p.surface)),
		Marker: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.interactive)).Background(lipgloss.Color(p.surface)),
		// Foreground is explicit: the textarea's default Text is empty, which
		// would leave typed input on the terminal's foreground over our painted
		// surface.
		Text: lipgloss.NewStyle().
			Background(lipgloss.Color(p.surface)).
			Foreground(lipgloss.Color(p.textPrimary)),
		// Set explicitly: the textarea hardcodes ANSI 240 for the placeholder,
		// ignoring theme colors. Secondary rather than tertiary since it's the
		// only instruction the empty composer gives.
		Placeholder: lipgloss.NewStyle().
			Background(lipgloss.Color(p.surface)).
			Foreground(lipgloss.Color(p.textSecondary)),
		SweepDim:    lipgloss.Color(p.inputRule),
		SweepHot:    lipgloss.Color(p.sweepHot),
		SweepMotion: true,
	}

	text := Text{
		Primary:   lipgloss.NewStyle().Foreground(lipgloss.Color(p.textPrimary)),
		Secondary: lipgloss.NewStyle().Foreground(lipgloss.Color(p.textSecondary)),
		Tertiary:  lipgloss.NewStyle().Foreground(lipgloss.Color(p.textTertiary)),
	}
	feedback := Feedback{
		Progress: lipgloss.NewStyle().Foreground(lipgloss.Color(p.interactive)),
		Error:    lipgloss.NewStyle().Foreground(lipgloss.Color(p.feedbackError)),
		Success:  lipgloss.NewStyle().Foreground(lipgloss.Color(p.feedbackSuccess)),
	}
	panel := Panel{
		Frame: lipgloss.NewStyle().
			Foreground(lipgloss.Color(p.textPrimary)).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(p.borderSubtle)).
			Padding(1, 2),
		Title:   text.Primary,
		Dismiss: text.Primary,
		Compact: text.Primary,
		// Help renders the footer hints ("↑/↓ navigate", "enter to continue"):
		// present but not something the reader has to read, so it takes the
		// tertiary level rather than secondary.
		Help:             text.Tertiary,
		MaxWidth:         112,
		HorizontalMargin: 2,
		CompactMaxWidth:  34,
		SectionGap:       1,
		FooterSeparator:  "   ",
	}
	approvalSurface := lipgloss.Color(p.approvalSurface)
	approvalPanel := panel
	approvalPanel.Title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.interactive))
	approvalPanel.Dismiss = text.Tertiary
	approval := Approval{
		Panel:  approvalPanel,
		Text:   text.Secondary,
		Detail: text.Tertiary,
		Action: lipgloss.NewStyle().
			Foreground(lipgloss.Color(p.textSecondary)).
			Background(approvalSurface).
			Padding(0, 1),
		Selected: lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color(p.onAccent)).
			Background(lipgloss.Color(p.interactive)).
			Padding(0, 1),
	}
	selector := Selector{
		Item:           text.Primary,
		Selected:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.interactive)),
		Detail:         text.Secondary,
		SelectedDetail: text.Primary,
		Marker:         "  ",
		SelectedMarker: "› ",
		ColumnGap:      2,
	}
	// The dev style catalog still illustrates the earlier label-sweep options,
	// so it receives the palette without affecting compact tool rendering.
	busy := shimmerPalette{
		fg:  lipgloss.Color(p.busy),
		bg:  lipgloss.Color(p.busySurface),
		dim: lipgloss.Color(p.busyDim),
		hot: lipgloss.Color(p.busyHot),
	}

	textInput := textinput.DefaultStyles(isDark)
	textInput.Focused.Text = text.Primary
	textInput.Focused.Placeholder = text.Secondary
	textInput.Focused.Suggestion = text.Secondary
	textInput.Focused.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color(p.interactive))
	textInput.Blurred.Text = text.Primary
	textInput.Blurred.Placeholder = text.Secondary
	textInput.Blurred.Suggestion = text.Secondary
	textInput.Blurred.Prompt = text.Secondary
	textInput.Cursor.Color = lipgloss.Color(p.interactive)

	return Theme{
		IsDark:     isDark,
		Background: lipgloss.Color(p.background),

		Input:     input,
		Text:      text,
		Feedback:  feedback,
		Panel:     panel,
		Approval:  approval,
		Selector:  selector,
		TextInput: textInput,
		Chat: Chat{
			Markdown:        markdown(isDark, p),
			MarkdownHeading: lipgloss.Color(p.secondary),
			MarkdownLink:    lipgloss.Color(p.link),
			MarkdownCodeFg:  lipgloss.Color(p.codeText),
			MarkdownCodeBg:  lipgloss.Color(p.codeSurface),
			AssistantText:   lipgloss.NewStyle().Foreground(lipgloss.Color(p.textSecondary)),
			Reasoning:       lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color(p.textSecondary)),
			ToolName:        lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(p.interactive)),
			ToolArgument:    text.Primary,
			ToolDetail:      lipgloss.NewStyle().Foreground(lipgloss.Color(p.textTertiary)),
			// The status glyph carries the failure signal; diagnostic text is
			// readable secondary copy rather than a wall of red.
			ToolError: text.Secondary,
			Diff: Diff{
				Add:        lipgloss.NewStyle().Foreground(lipgloss.Color(p.textPrimary)).Background(lipgloss.Color(p.successSurface)),
				Del:        lipgloss.NewStyle().Foreground(lipgloss.Color(p.textPrimary)).Background(lipgloss.Color(p.errorSurface)),
				Context:    lipgloss.NewStyle().Foreground(lipgloss.Color(p.textPrimary)),
				Gutter:     lipgloss.NewStyle().Foreground(lipgloss.Color(p.textTertiary)),
				Meta:       lipgloss.NewStyle().Foreground(lipgloss.Color(p.textTertiary)),
				SyntaxDark: isDark,
			},
			// The busy surface is what makes pill() draw caps for an in-flight
			// chip; without a background it skips them and renders bare text.
			StatusRunning: lipgloss.NewStyle().Foreground(lipgloss.Color(p.busy)).Background(lipgloss.Color(p.busySurface)),
			// A succeeded tool renders as a bare glyph, so this style needs no
			// surface; giving it one would leave a chip nothing ever draws.
			StatusSuccess:       lipgloss.NewStyle().Foreground(lipgloss.Color(p.success)),
			StatusError:         lipgloss.NewStyle().Foreground(lipgloss.Color(p.error)).Background(lipgloss.Color(p.errorSurface)),
			StatusRunningLabel:  busy.shimmer("running"),
			StatusAwaitingLabel: busy.shimmer("awaiting approval"),
			StatusSweepDim:      busy.dim,
			StatusSweepHot:      busy.hot,
			StatusSpinner:       Spinner{frames: brailleFrames, stepsPerFrame: spinnerStepsPerFrame},
			// Transcript labels, progress lines and the idle dot: present so nothing
			// is silently dropped, but not competing with the conversation.
			Meta:        lipgloss.NewStyle().Foreground(lipgloss.Color(p.textTertiary)),
			NoticeInfo:  lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color(p.onAccent)).Background(lipgloss.Color(p.info)),
			NoticeWarn:  lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color(p.onWarning)).Background(lipgloss.Color(p.warning)),
			NoticeError: lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color(p.onAccent)).Background(lipgloss.Color(p.critical)),
		},
		Editor: Editor{
			MenuFrame: lipgloss.NewStyle().
				Background(lipgloss.Color(p.background)).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color(p.borderSubtle)).
				BorderBackground(lipgloss.Color(p.background)),
			MenuItem: lipgloss.NewStyle().
				Foreground(lipgloss.Color(p.textSecondary)).
				Background(lipgloss.Color(p.background)),
			MenuDetail: lipgloss.NewStyle().
				Foreground(lipgloss.Color(p.textTertiary)).
				Background(lipgloss.Color(p.background)),
			MenuSelected: lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color(p.interactive)).
				Background(lipgloss.Color(p.background)),
			MenuSelectedDetail: lipgloss.NewStyle().Bold(true).
				Foreground(lipgloss.Color(p.interactive)).
				Background(lipgloss.Color(p.background)),
			MenuHelp: lipgloss.NewStyle().
				Foreground(lipgloss.Color(p.textPrimary)).
				Background(lipgloss.Color(p.background)),
		},
	}
}

// markdown adapts glamour's stock config to the palette.
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
