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

// New builds the root model for the given engine. When the engine is bound to a
// conversation, the model shows its id immediately and restores its history on
// Init.
func New(engine *agent.Engine) *Model {
	return &Model{
		engine:      engine,
		transcript:  chat.NewTranscript(),
		editor:      editor.New(),
		chatStyles:  chat.DefaultStyles(true),
		renderCache: map[chat.ItemID]renderCacheEntry{},
		convID:      engine.ConversationID(),
		hasDarkBG:   true,
	}
}

// Init focuses the editor and, when restoring a conversation, kicks off the
// one-shot history load whose result arrives as a historyLoadedMsg.
func (m *Model) Init() tea.Cmd {
	requestBG := func() tea.Msg { return tea.RequestBackgroundColor() }
	if m.engine.ConversationID() == "" {
		return m.editor.Focus()
	}
	m.chatPhase = chat.PhaseLoading
	return tea.Batch(m.editor.Focus(), loadHistory(m.engine), requestBG)
}
