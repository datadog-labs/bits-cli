package render

import "github.com/DataDog/bits-cli/internal/tui/chat"

// renderReasoning renders model thinking as dimmed, wrapped text. Collapsing
// (Ctrl+O) is deferred; the whole block is shown for now.
func renderReasoning(it chat.Item, width int, sty Styles) string {
	return sty.Reasoning.Render(wrap(it.Text, width))
}
