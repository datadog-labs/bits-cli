package chat

import (
	"charm.land/lipgloss/v2"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// Styles holds the chat-specific styles the renderers use.
type Styles struct {
	Input     styles.Input
	Accordion styles.Accordion
	styles.Chat
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

// DefaultStyles selects the chat portion of the default theme for a terminal
// background mode. It remains useful to callers that only know the mode.
func DefaultStyles(isDark bool) Styles {
	return StylesFor(styles.Default(isDark))
}

// StylesFor selects the chat portion of an already-built shared theme.
func StylesFor(theme styles.Theme) Styles {
	return Styles{Input: theme.Input, Accordion: theme.Accordion, Chat: theme.Chat}
}
