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
	if width < 1 {
		width = 1
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return wrap(src, width)
	}
	out, err := r.Render(src)
	if err != nil {
		// glamour tolerates partial markdown; this is belt-and-suspenders.
		return wrap(src, width)
	}
	return strings.Trim(out, "\n")
}
