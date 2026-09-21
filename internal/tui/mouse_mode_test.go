package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func clearMultiplexerEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TMUX", "")
	t.Setenv("ZELLIJ", "")
	t.Setenv("STY", "")
	t.Setenv("TERM", "xterm-256color")
}

func TestChatMouseModeEscalatesToAllMotionOutsideAMultiplexer(t *testing.T) {
	clearMultiplexerEnv(t)
	m := newShell()
	if got := m.chatMouseMode(); got != tea.MouseModeAllMotion {
		t.Fatalf("chatMouseMode() = %v, want AllMotion", got)
	}
}

func TestChatMouseModeDegradesUnderAMultiplexer(t *testing.T) {
	cases := []struct {
		name string
		set  func(t *testing.T)
	}{
		{"TMUX set", func(t *testing.T) { t.Setenv("TMUX", "/tmp/tmux-1000/default,1234,0") }},
		{"ZELLIJ set", func(t *testing.T) { t.Setenv("ZELLIJ", "0") }},
		{"STY set", func(t *testing.T) { t.Setenv("STY", "1234.pts-0.host") }},
		{"TERM=screen", func(t *testing.T) { t.Setenv("TERM", "screen-256color") }},
		{"TERM=tmux", func(t *testing.T) { t.Setenv("TERM", "tmux-256color") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearMultiplexerEnv(t)
			tc.set(t)
			m := newShell()
			if got := m.chatMouseMode(); got != tea.MouseModeCellMotion {
				t.Fatalf("chatMouseMode() = %v, want CellMotion", got)
			}
		})
	}
}

func TestViewUsesChatMouseModeInModeChat(t *testing.T) {
	clearMultiplexerEnv(t)
	m := newShell()
	m.mode = ModeChat
	m.resize(80, 24)
	if got := m.View().MouseMode; got != tea.MouseModeAllMotion {
		t.Fatalf("View().MouseMode = %v, want AllMotion", got)
	}
}
