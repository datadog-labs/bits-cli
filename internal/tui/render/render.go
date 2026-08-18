// Package render turns transcript items into styled, width-wrapped terminal
// strings. Rendering is a single exhaustive switch over assistant.ContentKind
// so a new server content type fails the build until it is handled here.
package render

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// Item renders one transcript item to a styled block with no trailing newline
// (the caller joins items). width is the target display width in cells.
//
//exhaustive:enforce
func Item(it chat.Item, width int, sty Styles) string {
	switch it.Kind {
	case assistant.KindText:
		return renderText(it, width, sty)
	case assistant.KindReasoning:
		return renderReasoning(it, width, sty)
	case assistant.KindToolCall, assistant.KindToolResult:
		return renderTool(it, width, sty)
	case assistant.KindWidget, assistant.KindDashboard,
		assistant.KindProgress, assistant.KindTurnMarker,
		assistant.KindStop, assistant.KindInternal, assistant.KindUnknown:
		return fallback(it, width, sty)
	}
	return fallback(it, width, sty) // unreachable; the compiler needs a return
}

// fallback renders kinds without a dedicated renderer as a dim, bracketed label
// so nothing is silently dropped.
func fallback(it chat.Item, width int, sty Styles) string {
	return sty.Meta.Render(wrap("["+it.Kind.String()+"]", width))
}

// wrap word-wraps s to width (min 1) without padding.
func wrap(s string, width int) string {
	if width < 1 {
		width = 1
	}
	return ansi.Wordwrap(s, width, "-")
}

// indent prefixes every line of s with pad spaces.
func indent(s string, pad int) string {
	if pad <= 0 || s == "" {
		return s
	}
	prefix := strings.Repeat(" ", pad)
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}
