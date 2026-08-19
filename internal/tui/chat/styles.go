package chat

import (
	"charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
)

// Work in progress: these colors (the Datadog violet accents and the status
// palette) are placeholders and should be refined.
const (
	brandPurple = "#632CA6"
	lightViolet = "#A78BFA"
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
	violet := c(lipgloss.Color(brandPurple), lipgloss.Color(lightViolet))

	return Styles{
		MarkdownStyle: markdownStyleName(isDark),

		UserMarker:    lipgloss.NewStyle().Bold(true).Foreground(violet),
		UserText:      lipgloss.NewStyle(),
		AssistantText: lipgloss.NewStyle(),
		Reasoning:     lipgloss.NewStyle().Faint(true).Italic(true),
		ToolName:      lipgloss.NewStyle().Bold(true).Foreground(violet),
		ToolDetail:    lipgloss.NewStyle().Faint(true),
		StatusRunning: lipgloss.NewStyle().Foreground(c(lipgloss.Color("3"), lipgloss.Color("11"))),
		StatusSuccess: lipgloss.NewStyle().Foreground(c(lipgloss.Color("2"), lipgloss.Color("10"))),
		StatusError:   lipgloss.NewStyle().Foreground(c(lipgloss.Color("1"), lipgloss.Color("9"))),
		Meta:          lipgloss.NewStyle().Faint(true),
		NoticeInfo:    lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#632CA6")),
		NoticeWarn:    lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color("#1A1A1A")).Background(lipgloss.Color("#F5A623")),
		NoticeError:   lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#C4314B")),
	}
}

func markdownStyleName(isDark bool) string {
	if isDark {
		return styles.DarkStyle
	}
	return styles.LightStyle
}

// datadogStyleConfig derives a glamour style from the built-in dark/light base,
// accenting headings and links. It copies the base by value and only reassigns
// pointer fields, so the shared built-in config is never mutated.
func datadogStyleConfig(isDark bool) ansi.StyleConfig {
	cfg := styles.LightStyleConfig
	accent := brandPurple
	if isDark {
		cfg = styles.DarkStyleConfig
		accent = lightViolet
	}

	cfg.Heading.Color = new(accent)
	cfg.H1.Color = new("#FFFFFF")
	cfg.H1.BackgroundColor = new(brandPurple)
	cfg.H6.Color = new(accent)
	cfg.Link.Color = new(accent)
	cfg.LinkText.Color = new(accent)

	return cfg
}
