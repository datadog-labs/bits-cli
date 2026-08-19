package tui

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// historyLoadTimeout bounds the one-shot conversation-history fetch on startup.
const historyLoadTimeout = 30 * time.Second

// turnEventMsg carries one engine event into Update; turnClosedMsg signals the
// turn's channel was closed (turn finished or cancelled).
type (
	turnEventMsg  struct{ ev agent.Event }
	turnClosedMsg struct{}
)

// historyLoadedMsg carries the result of the startup history restore: the
// persisted messages to replay, or an error to surface.
type historyLoadedMsg struct {
	msgs []assistant.Message
	err  error
}

// loadHistory fetches the engine's conversation history off the tea thread and
// delivers it as a historyLoadedMsg. It is a one-shot command, not a turn: it
// does not touch the turn-event pump.
func loadHistory(engine *agent.Engine) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), historyLoadTimeout)
		defer cancel()
		msgs, err := engine.LoadHistory(ctx)
		return historyLoadedMsg{msgs: msgs, err: err}
	}
}

// waitEvent reads one event from the turn channel and re-arms after each event
// in Update — the turn-scoped pump. Reading a closed channel yields
// turnClosedMsg.
func waitEvent(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return turnClosedMsg{}
		}
		return turnEventMsg{ev: ev}
	}
}

// Update is the single message handler. Only this thread touches Model state.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil

	case tea.BackgroundColorMsg:
		m.setDarkBackground(msg.IsDark())
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case turnEventMsg:
		m.applyEvent(msg.ev)
		m.refreshViewport()
		return m, waitEvent(m.turnEvents)

	case turnClosedMsg:
		if m.chatPhase != chat.PhaseError {
			m.chatPhase = chat.PhaseIdle
		}
		m.turnEvents = nil
		m.cancelTurn = nil
		return m, nil

	case historyLoadedMsg:
		m.applyHistory(msg)
		m.refreshViewport()
		return m, nil
	}

	// Cursor blink, paste, and other input messages go to the editor; a paste
	// can change its height, so relayout.
	cmd := m.editor.Update(msg)
	m.refreshViewport()
	return m, cmd
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		if m.cancelTurn != nil {
			m.cancelTurn()
		}
		return m, tea.Quit
	}

	// While the completion menu is open it owns navigation keys (arrows, tab,
	// enter to accept, esc to close); route everything to the editor.
	if m.editor.MenuOpen() {
		cmd := m.editor.Update(msg)
		m.refreshViewport()
		return m, cmd
	}

	switch msg.String() {
	case "esc":
		if m.cancelTurn != nil {
			m.cancelTurn() // interrupt the running turn
		}
		return m, nil
	case "enter":
		return m.submit()
	case "pgup", "pgdown":
		// ctrl+u / ctrl+d are intentionally NOT scroll keys: the editor is always
		// focused and owns them for line editing (ctrl+u = delete to line start,
		// which is what Ghostty sends for cmd+backspace). Transcript scrolling is
		// pgup/pgdown and the mouse wheel.
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	cmd := m.editor.Update(msg)
	m.refreshViewport()
	return m, cmd
}

// submit starts a turn for the current input, unless it is empty or a turn is
// already running.
func (m *Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.editor.Value())
	if text == "" || m.turnEvents != nil {
		return m, nil
	}
	m.editor.Reset()
	m.transcript.AppendUser(text)

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	m.turnEvents = m.engine.Start(ctx, text)
	m.chatPhase = chat.PhaseWaiting
	m.errMsg = ""
	m.refreshViewport()
	return m, waitEvent(m.turnEvents)
}

// applyEvent folds one engine event into the transcript / status. The switch is
// exhaustive over agent.EventKind.
func (m *Model) applyEvent(ev agent.Event) {
	switch ev.Kind {
	case agent.EventMessage:
		m.applyMessage(ev.Msg)
	case agent.EventConversation:
		m.convID = ev.ConvID
	case agent.EventTurnDone:
		m.transcript.FinalizeAll()
		m.chatPhase = chat.PhaseIdle
	case agent.EventError:
		m.transcript.FinalizeAll()
		m.chatPhase = chat.PhaseError
		if ev.Err != nil {
			m.errMsg = ev.Err.Error()
		}
	}
}

// applyMessage folds one streamed message into the transcript and advances the
// live-turn status. The transcript mapping is shared with history replay via
// Transcript.Fold; only the phase/usage side effects are live-turn specific.
func (m *Model) applyMessage(msg assistant.Message) {
	if msg.Results != nil && msg.Results.Usage != nil {
		m.usage = msg.Results.Usage
	}
	if k := msg.Content.Kind(); k == assistant.KindText || k == assistant.KindReasoning {
		m.chatPhase = chat.PhaseStreaming
	}
	m.transcript.Fold(msg)
}

// applyHistory replays a restored conversation into the transcript. It reuses
// the same folding as the live path, banks the final usage, then finalizes so
// nothing shows a streaming indicator and returns to idle ready for input. A
// load error surfaces on the status line without a transcript.
func (m *Model) applyHistory(res historyLoadedMsg) {
	if res.err != nil {
		m.chatPhase = chat.PhaseError
		m.errMsg = res.err.Error()
		return
	}
	for _, msg := range res.msgs {
		if msg.Results != nil && msg.Results.Usage != nil {
			m.usage = msg.Results.Usage
		}
		m.transcript.Fold(msg)
	}
	m.transcript.FinalizeAll()
	m.chatPhase = chat.PhaseIdle
}

// setDarkBackground adapts styles to the detected terminal background. It drops
// the render cache because those entries were produced under the old palette —
// the cache key is version+width, not style — so stale colors would otherwise
// persist until each item next changes.
func (m *Model) setDarkBackground(isDark bool) {
	if isDark == m.hasDarkBG {
		return
	}
	m.hasDarkBG = isDark
	m.chatStyles = chat.DefaultStyles(isDark)
	m.renderCache = map[chat.ItemID]renderCacheEntry{}
	m.refreshViewport()
}

func (m *Model) resize(w, h int) {
	m.height = h
	if !m.ready {
		m.viewport = viewport.New(viewport.WithWidth(w), viewport.WithHeight(1))
		m.ready = true
	} else {
		m.viewport.SetWidth(w)
	}
	m.editor.SetWidth(w)
	m.refreshViewport()
}

// refreshViewport re-renders the transcript into the viewport and sizes it to
// the space left by the status line and the (possibly multi-row) editor. The
// view stays pinned to the bottom while it was already there (auto-follow).
func (m *Model) refreshViewport() {
	if !m.ready {
		return
	}
	vpHeight := max(1, m.height-1-m.editor.Height())
	pinned := m.viewport.AtBottom()
	m.viewport.SetHeight(vpHeight)
	m.viewport.SetContent(m.renderTranscript())
	if pinned {
		m.viewport.GotoBottom()
	}
}

func (m *Model) renderTranscript() string {
	items := m.transcript.Items()
	blocks := make([]string, len(items))
	for i := range items {
		blocks[i] = m.renderCached(items[i])
	}
	return strings.Join(blocks, "\n\n")
}

// renderCached memoizes each item's rendered output by version + width so only
// the changed (streaming) item re-renders.
func (m *Model) renderCached(it chat.Item) string {
	w := m.viewport.Width()
	if e, ok := m.renderCache[it.ID]; ok && e.version == it.Version && e.width == w {
		return e.out
	}
	out := it.Render(w, m.chatStyles)
	m.renderCache[it.ID] = renderCacheEntry{version: it.Version, width: w, out: out}
	return out
}
