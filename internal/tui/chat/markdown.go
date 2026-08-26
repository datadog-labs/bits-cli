package chat

import (
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// RenderMarkdown renders markdown source to ANSI using the Datadog-accented
// glamour style for the given terminal background, wrapped to width. It is the
// exported entry point for dev tooling (the style catalog) that needs the same
// markdown rendering the transcript uses.
func RenderMarkdown(src string, width int, isDark bool) string {
	return renderMarkdown(src, width, markdownStyleConfig(isDark))
}

func markdownStyleConfig(isDark bool) ansi.StyleConfig {
	return styles.Default(isDark).Chat.Markdown
}

// renderMarkdown renders markdown source to ANSI using the Datadog-accented
// glamour style for the given palette, wrapped to width, with glamour's
// surrounding blank lines trimmed so blocks join cleanly.
func renderMarkdown(src string, width int, style ansi.StyleConfig) string {
	var r markdownRenderer
	return r.Render(src, width, style)
}

type markdownRenderer struct {
	term  *glamour.TermRenderer
	width int
	style ansi.StyleConfig
	ready bool
}

// Render reuses Glamour's parsed style and Goldmark pipeline until wrapping or
// the terminal theme changes. Failed setup is cached too, avoiding repeated
// setup attempts while the same fallback configuration remains active.
func (r *markdownRenderer) Render(src string, width int, style ansi.StyleConfig) string {
	if width < 1 {
		width = 1
	}
	if !r.ready || width != r.width || style != r.style {
		r.term, _ = glamour.NewTermRenderer(
			glamour.WithStyles(style),
			glamour.WithWordWrap(width),
		)
		r.width = width
		r.style = style
		r.ready = true
	}
	if r.term == nil {
		return wrap(src, width)
	}
	out, err := r.term.Render(src)
	if err != nil {
		r.term = nil
		r.ready = false
		return wrap(src, width)
	}
	return strings.Trim(out, "\n")
}
