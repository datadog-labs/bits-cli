// Package tui is the Bubble Tea front end: it owns UI state, runs the turn-event
// pump, and maps agent events onto the transcript store and renderer.
package tui

import (
	"context"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/editor"
)

// memoEntry caches one item's rendered output, keyed by its version and width.
type memoEntry struct {
	version int
	width   int
	out     string
}

// Model is the root Bubble Tea model. All state lives here and is mutated only
// on the tea thread; the sole async source is the engine's event channel.
type Model struct {
	engine     *agent.Engine
	transcript *chat.Transcript
	styles     chat.Styles

	// turn is the active turn's event channel (nil when idle); cancel
	// interrupts it.
	turn   <-chan agent.Event
	cancel context.CancelFunc

	viewport viewport.Model
	editor   *editor.Editor
	memo     map[string]memoEntry

	phase  chat.Phase
	convID string
	usage  *assistant.Usage
	errMsg string

	width  int
	height int
	ready  bool
}

// New builds the root model for the given engine.
func New(engine *agent.Engine) *Model {
	return &Model{
		engine:     engine,
		transcript: chat.NewTranscript(),
		styles:     chat.DefaultStyles(),
		editor:     editor.New(),
		memo:       map[string]memoEntry{},
		phase:      chat.PhaseIdle,
	}
}

// Init focuses the editor and starts its cursor blink.
func (m *Model) Init() tea.Cmd {
	return m.editor.Focus()
}
