package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// mouseWheelDelta is how many transcript lines one wheel notch scrolls,
const mouseWheelDelta = 3

// historyLoadTimeout bounds the one-shot conversation-history fetch on startup.
const historyLoadTimeout = 30 * time.Second

// defaultNoticeTTL is how long a transient status notice stays before it clears.
const defaultNoticeTTL = 10 * time.Second

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

// noticeExpiredMsg clears a transient status notice when its TTL elapses. seq
// guards against a stale timer clearing a newer notice.
type noticeExpiredMsg struct{ seq int }

// showNotice sets the transient status notice and returns a command that clears
// it after ttl (defaultNoticeTTL when ttl <= 0). The seq stamps the timer so a
// later notice is not cleared by an earlier one's timer.
func (m *Model) showNotice(n chat.Notice, ttl time.Duration) tea.Cmd {
	m.noticeSeq++
	m.notice = n
	if ttl <= 0 {
		ttl = defaultNoticeTTL
	}
	seq := m.noticeSeq
	return tea.Tick(ttl, func(time.Time) tea.Msg { return noticeExpiredMsg{seq: seq} })
}

// clearNotice removes any notice immediately and invalidates a pending timer.
func (m *Model) clearNotice() {
	m.noticeSeq++
	m.notice = chat.Notice{}
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

	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.list.ScrollBy(-mouseWheelDelta)
		case tea.MouseWheelDown:
			m.list.ScrollBy(mouseWheelDelta)
		}
		return m, nil

	case turnEventMsg:
		cmd := m.applyEvent(msg.ev)
		m.refreshViewport()
		return m, tea.Batch(cmd, waitEvent(m.turnEvents))

	case turnClosedMsg:
		if m.chatPhase != chat.PhaseError {
			m.chatPhase = chat.PhaseIdle
		}
		m.turnEvents = nil
		m.cancelTurn = nil
		return m, nil

	case historyLoadedMsg:
		cmd := m.applyHistory(msg)
		m.refreshViewport()
		return m, cmd

	case noticeExpiredMsg:
		if msg.seq == m.noticeSeq {
			m.notice = chat.Notice{}
		}
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
		if msg.String() == "pgup" {
			m.list.PageUp()
		} else {
			m.list.PageDown()
		}
		return m, nil
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
	m.clearNotice()
	m.refreshViewport()
	m.list.ScrollToBottom()
	return m, waitEvent(m.turnEvents)
}

// applyEvent folds one engine event into the transcript / status. The switch is
// exhaustive over agent.EventKind. It returns a command for side effects (a turn
// error posts a transient notice); nil otherwise.
func (m *Model) applyEvent(ev agent.Event) tea.Cmd {
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
			return m.showNotice(noticeForError("", ev.Err), 0)
		}
	}
	return nil
}

// applyMessage folds one streamed message into the transcript and advances the
// live-turn status.
func (m *Model) applyMessage(msg assistant.Message) {
	if msg.Results != nil && msg.Results.Usage != nil {
		m.usage = msg.Results.Usage
	}
	if k := msg.Content.Kind(); k == assistant.KindText || k == assistant.KindReasoning {
		m.chatPhase = chat.PhaseStreaming
	}
	m.transcript.AppendMessage(msg)
}

// applyHistory replays a restored conversation into the transcript.
func (m *Model) applyHistory(res historyLoadedMsg) tea.Cmd {
	if res.err != nil {
		m.chatPhase = chat.PhaseIdle
		return m.showNotice(noticeForError("restore failed", res.err), 0)
	}
	for _, msg := range res.msgs {
		if msg.Results != nil && msg.Results.Usage != nil {
			m.usage = msg.Results.Usage
		}
		m.transcript.AppendMessage(msg)
	}
	m.transcript.FinalizeAll()
	m.chatPhase = chat.PhaseIdle
	return nil
}

// setDarkBackground adapts styles to the detected terminal background.
func (m *Model) setDarkBackground(isDark bool) {
	if isDark == m.hasDarkBG {
		return
	}
	m.hasDarkBG = isDark
	m.chatStyles = chat.DefaultStyles(isDark)
	m.list.SetStyles(m.chatStyles)
	m.refreshViewport()
}

func (m *Model) resize(w, h int) {
	m.height = h
	m.editor.SetWidth(w)
	m.list.SetWidth(w)
	if m.mode == ModeTermInit {
		m.setMode(ModeChat)
		return
	}
	m.refreshViewport()
}

// refreshViewport re-syncs the transcript list and sizes it to the space left by
// the status line and the (possibly multi-row) editor.
func (m *Model) refreshViewport() {
	if m.mode == ModeTermInit {
		return
	}
	m.list.SetHeight(max(1, m.height-1-m.editor.Height()))
	m.list.SetItems(m.transcript.Items())
}
