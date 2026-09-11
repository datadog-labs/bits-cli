package tui

import (
	"context"
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/agent/fake"
	"github.com/DataDog/bits-cli/internal/assistant"
	loginui "github.com/DataDog/bits-cli/internal/tui/login"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// TestViewsPaintTheThemeBackground covers both root views and both modes: an
// unset background would silently fall back to the terminal's own.
func TestViewsPaintTheThemeBackground(t *testing.T) {
	for _, test := range []struct {
		name     string
		terminal string
		want     color.Color
	}{
		{name: "dark", terminal: "#000000", want: styles.Default(true).Background},
		{name: "light", terminal: "#FFFFFF", want: styles.Default(false).Background},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := NewWithLogin(context.Background(), loginui.New(context.Background(), nil),
				func(context.Context) (*agent.Engine, error) {
					return agent.New(fake.New(), assistant.SendOptions{}), nil
				})

			_, _ = root.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			_, _ = root.Update(tea.BackgroundColorMsg{Color: lipgloss.Color(test.terminal)})

			if got := root.View().BackgroundColor; got != test.want {
				t.Errorf("login view background = %v, want %v", got, test.want)
			}

			_, cmd := root.Update(loginui.CompletedMsg{})
			if cmd == nil {
				t.Fatal("login completion produced no command")
			}
			if _, initCmd := root.Update(cmd()); initCmd == nil {
				t.Fatal("engine result did not initialize chat")
			}
			if root.mode != ModeChat {
				t.Fatalf("mode = %v, want ModeChat", root.mode)
			}

			if got := root.View().BackgroundColor; got != test.want {
				t.Errorf("chat view background = %v, want %v", got, test.want)
			}
		})
	}
}

// TestBackgroundFollowsTerminalModeChange guards a late-arriving terminal
// reply: the background must rebuild with the rest of the theme, not stay on
// the dark default.
func TestBackgroundFollowsTerminalModeChange(t *testing.T) {
	root := NewWithLogin(context.Background(), loginui.New(context.Background(), nil),
		func(context.Context) (*agent.Engine, error) {
			return agent.New(fake.New(), assistant.SendOptions{}), nil
		})
	_, _ = root.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	_, _ = root.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#000000")})
	dark := root.View().BackgroundColor

	_, _ = root.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#FFFFFF")})
	light := root.View().BackgroundColor

	if dark == nil || light == nil {
		t.Fatalf("background dark=%v light=%v, want both set", dark, light)
	}
	if dark == light {
		t.Errorf("background stayed %v across a terminal mode change", dark)
	}
}
