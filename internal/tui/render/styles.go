package render

import "github.com/charmbracelet/lipgloss"

// Styles holds the lipgloss styles the renderers use. It is a plain value:
// construct one with DefaultStyles and pass it into Item. No theme system yet.
type Styles struct {
	UserMarker    lipgloss.Style // "› " prompt marker
	UserText      lipgloss.Style
	AssistantText lipgloss.Style
	Reasoning     lipgloss.Style // dimmed thinking
	ToolName      lipgloss.Style
	ToolDetail    lipgloss.Style // input summary + output body
	StatusRunning lipgloss.Style
	StatusSuccess lipgloss.Style
	StatusError   lipgloss.Style
	Meta          lipgloss.Style // fallback labels, separators
}

// DefaultStyles returns a reasonable default palette using ANSI-256 colors.
func DefaultStyles() Styles {
	return Styles{
		UserMarker:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")),
		UserText:      lipgloss.NewStyle(),
		AssistantText: lipgloss.NewStyle(),
		Reasoning:     lipgloss.NewStyle().Faint(true).Italic(true),
		ToolName:      lipgloss.NewStyle().Bold(true),
		ToolDetail:    lipgloss.NewStyle().Faint(true),
		StatusRunning: lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		StatusSuccess: lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		StatusError:   lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		Meta:          lipgloss.NewStyle().Faint(true),
	}
}
