package tui

import (
	"context"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

// Clicking an app route opens it on the engine's web origin.
func TestMarkdownLinkClickOpensAppRoute(t *testing.T) {
	var opened []string
	m := New(agent.New(&spyBackend{t: t, webOrigin: "https://acme.ap1.datadoghq.com"}, agent.Config{}), Config{
		OpenURL: func(_ context.Context, u string) error { opened = append(opened, u); return nil },
	})
	defer m.Detach()
	m.mode = ModeChat
	m.state.Blocks = []agent.Block{{
		ID: agent.BlockID{Key: "answer"}, Kind: assistant.KindText, Role: assistant.RoleAssistant,
		Complete: true, Markdown: &assistant.MarkdownPayload{Content: "[tile](/dashboard/abc?tile_focus=w1#g)"},
	}}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m.syncTranscript()
	flattenMsgs(t, clickWidget(t, m, chat.WidgetNone))
	if want := []string{"https://acme.ap1.datadoghq.com/dashboard/abc?tile_focus=w1#g"}; !slices.Equal(opened, want) {
		t.Fatalf("opened %q, want %q", opened, want)
	}
}
