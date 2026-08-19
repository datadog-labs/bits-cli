package chat

import (
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
)

// renderMarkdown renders markdown source to ANSI using the Datadog-accented
// glamour style for the given background, wrapped to width, with glamour's
// surrounding blank lines trimmed so blocks join cleanly. An empty styleName is
// treated as dark.
func renderMarkdown(src string, width int, styleName string) string {
	if width < 1 {
		width = 1
	}
	isDark := styleName != styles.LightStyle
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(datadogStyleConfig(isDark)),
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
