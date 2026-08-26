// Package tui is the Bubble Tea front end: it owns UI state, runs the turn-event
// pump, and maps agent events onto the transcript store and renderer.
package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/editor"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// Mode is the top-level screen the model shows.
type Mode int

const (
	ModeTermInit Mode = iota
	ModeChat
)

// Model is the root Bubble Tea model. All state lives here and is mutated only
// on the tea thread; the sole async source is the engine's event channel.
type Model struct {
	// Collaborators the model drives.
	engine *agent.Engine
	editor *editor.Editor

	// blocks is the latest snapshot of the engine's aggregated transcript
	blocks []agent.Block

	// Active turn: turnEvents is the running turn's event channel (nil when
	// idle); cancelTurn interrupts it.
	turnEvents <-chan agent.Event
	cancelTurn context.CancelFunc

	// Top-level screen; transitions go through setMode.
	mode Mode

	// Turn status, surfaced in the status line.
	chatPhase chat.Phase
	convID    string
	usage     *assistant.Usage

	// notice is the transient status message (error/warn/info) shown in the
	// status line.
	notice    chat.Notice
	noticeSeq int

	// Transcript rendering. list owns the transcript's scroll position and
	// per-item render cache; chatStyles is also used by the notice bar.
	list       *chat.List
	chatStyles chat.Styles
	styles     styles.Theme // terminal styles; dark until detected

	// Terminal height (the full window height from WindowSizeMsg; the transcript
	// height is derived from it). Width isn't stored here — it lives on list.
	// The model leaves ModeTermInit once the first WindowSizeMsg arrives.
	height int
}

// New builds the root model for the given engine. When the engine is bound to a
// conversation, the model shows its id immediately and restores its history on
// Init.
func New(engine *agent.Engine) *Model {
	m := &Model{
		engine: engine,
		editor: editor.New(),
		list:   chat.NewList(),
		styles: styles.Default(true),
		convID: engine.ConversationID(),
	}
	m.applyStyles(m.styles)
	return m
}

// ConversationID returns the active conversation id, or "" when none has been
// established yet. main reads it after the program exits to print a resume hint.
func (m *Model) ConversationID() string { return m.convID }

// applyStyles propagates one complete theme to every component that copies
// style values. Keep this as the single fan-out point for theme changes.
func (m *Model) applyStyles(theme styles.Theme) {
	m.styles = theme
	m.chatStyles = chat.StylesFor(theme)
	m.list.SetStyles(m.chatStyles)
	m.editor.SetInputStyles(theme.Input)
	m.editor.SetStyles(theme.Editor)
}

// setMode switches the top-level screen. It is the single entry point for mode
// changes so any layout/refresh side effects stay centralized (the tui analog
// of a state funnel). Today it only drives a viewport refresh; a future
// conversation picker plugs its layout in here.
func (m *Model) setMode(mode Mode) {
	m.mode = mode
	m.refreshViewport()
}

// Init focuses the editor and, when restoring a conversation, starts the history
// restore as a run: it streams through the same turn-event pump, so its blocks
// arrive as ordinary events and the engine transcript stays single-writer.
func (m *Model) Init() tea.Cmd {
	requestBG := func() tea.Msg { return tea.RequestBackgroundColor() }
	if m.engine.ConversationID() == "" {
		return m.editor.Focus()
	}
	m.chatPhase = chat.PhaseLoading
	ctx, cancel := context.WithTimeout(context.Background(), historyLoadTimeout)
	m.cancelTurn = cancel
	m.turnEvents = m.engine.Restore(ctx)
	return tea.Batch(m.editor.Focus(), waitEvent(m.turnEvents), requestBG)
}
