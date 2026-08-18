package render

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// renderText renders a user or assistant text fragment. Markdown is shown as
// plain wrapped text for now; rich markdown rendering is deferred.
func renderText(it chat.Item, width int, sty Styles) string {
	if it.Role == assistant.RoleUser {
		return renderUser(it.Text, width, sty)
	}
	return sty.AssistantText.Render(wrap(it.Text, width))
}

// renderUser prefixes the first line with a marker and hangs the continuation
// lines under it so the message stays visually aligned.
func renderUser(text string, width int, sty Styles) string {
	const marker = "› "
	w := ansi.StringWidth(marker)

	lines := strings.Split(wrap(text, max(1, width-w)), "\n")
	for i, ln := range lines {
		if i == 0 {
			lines[i] = sty.UserMarker.Render(marker) + sty.UserText.Render(ln)
			continue
		}
		lines[i] = strings.Repeat(" ", w) + sty.UserText.Render(ln)
	}
	return strings.Join(lines, "\n")
}
