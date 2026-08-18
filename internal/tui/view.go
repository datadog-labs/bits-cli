package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// View lays out the transcript viewport, a status line, and the input.
func (m *Model) View() string {
	if !m.ready {
		return "loading…"
	}
	return strings.Join([]string{
		m.viewport.View(),
		m.statusLine(),
		m.input.View(),
	}, "\n")
}

func (m *Model) statusLine() string {
	var b strings.Builder
	b.WriteString(phaseLabel(m.phase))
	if m.usage != nil && m.usage.TokensUsed > 0 {
		fmt.Fprintf(&b, " · %d tokens", m.usage.TokensUsed)
	}
	if m.convID != "" {
		fmt.Fprintf(&b, " · %s", shortID(m.convID))
	}
	if m.phase == chat.PhaseError && m.errMsg != "" {
		fmt.Fprintf(&b, " · %s", m.errMsg)
	}
	return m.styles.Meta.Render(ansi.Truncate(b.String(), max(1, m.width), "…"))
}

// phaseLabel is exhaustive over chat.Phase.
//
//exhaustive:enforce
func phaseLabel(p chat.Phase) string {
	switch p {
	case chat.PhaseIdle:
		return "ready"
	case chat.PhaseWaiting:
		return "waiting…"
	case chat.PhaseStreaming:
		return "streaming…"
	case chat.PhaseError:
		return "error"
	}
	return ""
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
