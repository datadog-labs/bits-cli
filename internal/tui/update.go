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

// historyLoadTimeout bounds the conversation-history fetch on startup.
const historyLoadTimeout = 30 * time.Second

// mouseWheelDelta is how many transcript lines one wheel notch scrolls,
const mouseWheelDelta = 3

// defaultNoticeTTL is how long a transient status notice stays before it clears.
const defaultNoticeTTL = 10 * time.Second

// turnEventMsg carries one engine event into Update; turnClosedMsg signals the
// turn's channel was closed (turn finished or cancelled). A history restore runs
// through the same pump, so its events flow here too.
type (
	turnEventMsg  struct{ ev agent.Event }
	turnClosedMsg struct{}
)

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
		if m.cancelTurn != nil {
			m.cancelTurn() // release the turn/restore context
			m.cancelTurn = nil
		}
		return m, nil

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

// submit starts a turn for the current input, unless it is empty, a turn is
// already running, or history is still loading. The user block is added by the
// engine (it owns the transcript), so it arrives as the turn's first event.
func (m *Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.editor.Value())
	if text == "" || m.turnEvents != nil || m.chatPhase == chat.PhaseLoading {
		return m, nil
	}
	m.editor.Reset()

	// Slash commands are a native control plane: they never reach the model.
	if name, ok := parseCommand(text); ok {
		return m.dispatchCommand(name)
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	m.turnEvents = m.engine.StartTurn(ctx, text)
	m.chatPhase = chat.PhaseWaiting
	m.clearNotice()
	m.refreshViewport()
	// Submitting always jumps to the tail and re-engages auto-follow, so the
	// user sees their message and the incoming reply even if they had scrolled up.
	m.list.ScrollToBottom()
	return m, waitEvent(m.turnEvents)
}

// applyEvent folds one engine event into the block snapshot / status. The switch
// is exhaustive over agent.EventKind. It returns a command for side effects (an
// error posts a transient notice); nil otherwise.
func (m *Model) applyEvent(ev agent.Event) tea.Cmd {
	switch ev.Kind {
	case agent.EventBlock:
		m.blocks = ev.Update.Blocks
		// A still-open text/reasoning block means tokens are arriving. Restored
		// (Complete) blocks and the bulk restore snapshot (zero Changed) don't
		// flip the phase, so restore stays in PhaseLoading until its channel closes.
		if k := ev.Update.Changed.Kind; !ev.Update.Changed.Complete &&
			(k == assistant.KindText || k == assistant.KindReasoning) {
			m.chatPhase = chat.PhaseStreaming
		}
	case agent.EventUsage:
		m.usage = ev.Usage
	case agent.EventConversation:
		m.convID = ev.ConvID
	case agent.EventTurnDone:
		m.chatPhase = chat.PhaseIdle
	case agent.EventError:
		// A failure during restore is benign: drop to idle with a notice so the
		// user can still type. A failure mid-turn is the turn's error state.
		if m.chatPhase == chat.PhaseLoading {
			m.chatPhase = chat.PhaseIdle
			if ev.Err != nil {
				return m.showNotice(noticeForError("restore failed", ev.Err), 0)
			}
		} else {
			m.chatPhase = chat.PhaseError
			if ev.Err != nil {
				return m.showNotice(noticeForError("", ev.Err), 0)
			}
		}
	}
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
	m.list.SetItems(m.blocks)
}
