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

// renderCacheEntry caches one item's rendered output, keyed by its version and
// width.
type renderCacheEntry struct {
	version int
	width   int
	out     string
}

// Model is the root Bubble Tea model. All state lives here and is mutated only
// on the tea thread; the sole async source is the engine's event channel.
type Model struct {
	// Collaborators the model drives.
	engine     *agent.Engine
	transcript *chat.Transcript
	editor     *editor.Editor

	// Active turn: turnEvents is the running turn's event channel (nil when
	// idle); cancelTurn interrupts it.
	turnEvents <-chan agent.Event
	cancelTurn context.CancelFunc

	// Turn status, surfaced in the status line.
	chatPhase chat.Phase
	convID    string
	usage     *assistant.Usage
	errMsg    string

	// Transcript rendering.
	viewport    viewport.Model
	chatStyles  chat.Styles
	renderCache map[chat.ItemID]renderCacheEntry
	hasDarkBG   bool // terminal background; assumed dark until detected

	// Terminal height (the full window height from WindowSizeMsg; the transcript
	// viewport height is derived from it). Width isn't stored — it equals
	// viewport.Width(). ready is set once the first WindowSizeMsg arrives.
	height int
	ready  bool
}

// New builds the root model for the given engine.
func New(engine *agent.Engine) *Model {
	return &Model{
		engine:      engine,
		transcript:  chat.NewTranscript(),
		editor:      editor.New(),
		chatStyles:  chat.DefaultStyles(true),
		renderCache: map[chat.ItemID]renderCacheEntry{},
		chatPhase:   chat.PhaseIdle,
		hasDarkBG:   true,
	}
}

// Init focuses the editor, starts its cursor blink, and asks the terminal for
// its background color so styles can adapt to a light or dark theme.
func (m *Model) Init() tea.Cmd {
	requestBG := func() tea.Msg { return tea.RequestBackgroundColor() }
	return tea.Batch(m.editor.Focus(), requestBG)
}
