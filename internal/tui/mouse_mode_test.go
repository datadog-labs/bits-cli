package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChatMouseModeDegradesUnderAMultiplexer(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want tea.MouseMode
	}{
		{"no multiplexer", nil, tea.MouseModeAllMotion},
		{"TMUX set", map[string]string{"TMUX": "/tmp/tmux-1000/default,1234,0"}, tea.MouseModeCellMotion},
		{"ZELLIJ set", map[string]string{"ZELLIJ": "0"}, tea.MouseModeCellMotion},
		{"STY set", map[string]string{"STY": "1234.pts-0.host"}, tea.MouseModeCellMotion},
		{"TERM=screen", map[string]string{"TERM": "screen-256color"}, tea.MouseModeCellMotion},
		{"TERM=tmux", map[string]string{"TERM": "tmux-256color"}, tea.MouseModeCellMotion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMUX", "")
			t.Setenv("ZELLIJ", "")
			t.Setenv("STY", "")
			t.Setenv("TERM", "xterm-256color")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := chatMouseMode(); got != tc.want {
				t.Fatalf("chatMouseMode() = %v, want %v", got, tc.want)
			}
		})
	}
}
