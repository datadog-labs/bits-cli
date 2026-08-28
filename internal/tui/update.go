package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	conversationview "github.com/DataDog/bits-cli/internal/tui/conversations"
	loginui "github.com/DataDog/bits-cli/internal/tui/login"
	"github.com/DataDog/bits-cli/internal/tui/styles"
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
	turnEventMsg struct {
		generation uint64
		ev         agent.Event
	}
	turnClosedMsg  struct{ generation uint64 }
	engineReadyMsg struct {
		generation uint64
		engine     *agent.Engine
		err        error
	}
)

// noticeExpiredMsg clears a transient status notice when its TTL elapses. seq
// guards against a stale timer clearing a newer notice.
type noticeExpiredMsg struct{ seq int }

var approvalChoices = [...]agent.ApprovalDecision{
	agent.ApprovalDeny,
	agent.ApprovalAllowOnce,
	agent.ApprovalAllowSession,
}

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
func waitEvent(generation uint64, ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return turnClosedMsg{generation: generation}
		}
		return turnEventMsg{generation: generation, ev: ev}
	}
}

// Update is the single message handler. Only this thread touches Model state.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.mode == ModeLogin {
		return m.updateLogin(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil

	case tea.BackgroundColorMsg:
		m.setDarkBackground(msg.IsDark())
		return m, nil

	case tea.KeyPressMsg:
		// Global quit must win over mode-specific input routing. In particular,
		// /resume may own a live list/history request that must be cancelled
		// before the application exits.
		if msg.String() == "ctrl+c" && m.mode == ModeConversations {
			m.abandonConversationPicker()
			return m.handleKey(msg)
		}
		if m.mode == ModeConversations {
			return m, m.updateConversationPicker(msg)
		}
		return m.handleKey(msg)

	case tea.MouseWheelMsg:
		if m.mode == ModeConversations {
			return m, m.updateConversationPicker(msg)
		}
		switch msg.Button {
		case tea.MouseWheelUp:
			m.list.ScrollBy(-mouseWheelDelta)
		case tea.MouseWheelDown:
			m.list.ScrollBy(mouseWheelDelta)
		}
		return m, nil

	case turnEventMsg:
		if !m.acceptRemoteMessage(msg.generation) {
			return m, nil
		}
		cmd := m.applyEvent(msg.ev)
		m.refreshViewport()
		return m, tea.Batch(cmd, waitEvent(msg.generation, m.turnEvents))

	case turnClosedMsg:
		if !m.acceptRemoteMessage(msg.generation) {
			return m, nil
		}
		if m.chatPhase != chat.PhaseError {
			m.chatPhase = chat.PhaseIdle
		}
		m.turnEvents = nil
		m.pendingApprovals = nil
		m.approvalChoice = 0
		if m.cancelTurn != nil && !m.cancelRequested {
			m.cancelTurn() // release the turn/restore context
		}
		m.cancelTurn = nil
		m.cancelRequested = false
		if m.pendingNew {
			m.pendingNew = false
			return m, m.startNewConversation()
		}
		return m, nil

	case conversationListResultMsg:
		return m, m.applyConversationListResult(msg)

	case conversationSwitchResultMsg:
		return m, m.applyConversationSwitchResult(msg)

	case conversationview.SelectedMsg:
		return m, m.selectConversation(msg.Conversation)

	case conversationview.CancelledMsg:
		return m, m.closeConversationPicker()

	case conversationview.RetryMsg:
		return m, m.retryConversationOperation()

	case noticeExpiredMsg:
		if msg.seq == m.noticeSeq {
			m.notice = chat.Notice{}
		}
		return m, nil
	}
	if m.mode == ModeConversations {
		return m, m.updateConversationPicker(msg)
	}
	if len(m.pendingApprovals) > 0 {
		return m, nil
	}

	// Cursor blink, paste, and other input messages go to the editor; a paste
	// can change its height, so relayout.
	cmd := m.editor.Update(msg)
	m.refreshViewport()
	return m, cmd
}

func (m *Model) updateLogin(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		m.setDarkBackground(msg.IsDark())
	case loginui.CompletedMsg:
		if m.startupPending || m.startupCanceled || m.startupStopping {
			return m, nil
		}
		m.startupPending = true
		m.startupGeneration++
		generation := m.startupGeneration
		factory := m.engineFactory
		factoryCtx, cancel := context.WithCancel(m.startupCtx)
		m.startupCancel = cancel
		return m, func() tea.Msg {
			if factory == nil {
				return engineReadyMsg{generation: generation, err: errors.New("authenticated chat is unavailable")}
			}
			engine, err := factory(factoryCtx)
			return engineReadyMsg{generation: generation, engine: engine, err: err}
		}
	case engineReadyMsg:
		if msg.generation != m.startupGeneration || m.startupCanceled || m.startupStopping {
			return m, nil
		}
		m.startupPending = false
		if m.startupCancel != nil {
			m.startupCancel()
			m.startupCancel = nil
		}
		if msg.err != nil {
			m.startupErr = msg.err
			return m, tea.Quit
		}
		if msg.engine == nil {
			m.startupErr = errors.New("authenticated chat returned no engine")
			return m, tea.Quit
		}
		m.engine = msg.engine
		m.convID = msg.engine.ConversationID()
		m.loginModel = nil
		m.engineFactory = nil
		m.startupCtx = nil
		if m.width > 0 {
			m.editor.SetWidth(m.width)
			m.list.SetWidth(m.width)
		}
		m.setMode(ModeChat)
		return m, m.initChat()
	}

	if m.loginModel == nil {
		m.startupErr = errors.New("startup login is unavailable")
		return m, tea.Quit
	}
	next, cmd := m.loginModel.Update(msg)
	if loginModel, ok := next.(*loginui.Model); ok {
		m.loginModel = loginModel
	}
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+c" {
		m.stopStartup()
	}
	if m.loginModel.Canceled() {
		m.startupCanceled = true
		m.stopStartup()
	}
	return m, cmd
}

func (m *Model) stopStartup() {
	if m.startupStopping {
		return
	}
	m.startupStopping = true
	m.startupGeneration++
	m.startupPending = false
	if m.startupCancel != nil {
		m.startupCancel()
		m.startupCancel = nil
	}
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		if m.cancelTurn != nil {
			m.cancelTurn()
		}
		return m, tea.Quit
	}
	if len(m.pendingApprovals) > 0 {
		return m.handleApprovalKey(msg)
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
		m.cancelRemote() // interrupt the running turn
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

func (m *Model) handleApprovalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "shift+tab":
		m.approvalChoice = (m.approvalChoice + len(approvalChoices) - 1) % len(approvalChoices)
	case "right", "tab":
		m.approvalChoice = (m.approvalChoice + 1) % len(approvalChoices)
	case "d", "esc":
		m.respondToApproval(agent.ApprovalDeny)
	case "enter":
		m.respondToApproval(approvalChoices[m.approvalChoice])
	}
	return m, nil
}

func (m *Model) respondToApproval(decision agent.ApprovalDecision) {
	if len(m.pendingApprovals) == 0 {
		return
	}
	m.engine.Decide(m.pendingApprovals[0].ToolCallID(), decision)
}

// submit routes slash commands through their active-turn policy, or starts a
// turn for ordinary input unless a turn is already running or history is still
// loading. The user block is added by the engine (it owns the transcript), so
// it arrives as the turn's first event.
func (m *Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.editor.Value())
	if text == "" {
		return m, nil
	}

	// Slash commands are a native control plane: they never reach the model.
	if name, ok := parseCommand(text); ok {
		m.editor.Reset()
		return m.dispatchCommand(name)
	}

	if m.turnEvents != nil || m.chatPhase == chat.PhaseLoading {
		return m, nil
	}
	m.editor.Reset()

	ctx, cancel := context.WithCancel(context.Background())
	events := m.engine.StartTurn(ctx, agent.TurnInput{Message: text, Tools: m.tools})
	wait := m.beginRemote(events, cancel)
	m.chatPhase = chat.PhaseWaiting
	m.usage = nil
	m.clearNotice()
	m.refreshViewport()
	// Submitting always jumps to the tail and re-engages auto-follow, so the
	// user sees their message and the incoming reply even if they had scrolled up.
	m.list.ScrollToBottom()
	return m, wait
}

func (m *Model) beginRemote(events <-chan agent.Event, cancel context.CancelFunc) tea.Cmd {
	m.turnGen++
	m.turnEvents = events
	m.cancelTurn = cancel
	m.cancelRequested = false
	return waitEvent(m.turnGen, events)
}

func (m *Model) acceptRemoteMessage(generation uint64) bool {
	return m.turnEvents != nil && generation == m.turnGen
}

func (m *Model) cancelRemote() {
	if m.cancelTurn == nil || m.cancelRequested {
		return
	}
	m.cancelRequested = true
	m.cancelTurn()
}

// applyEvent folds one engine event into the block snapshot / status. The switch
// is exhaustive over agent.EventKind. It returns a command for side effects (an
// error posts a transient notice); nil otherwise.
func (m *Model) applyEvent(ev agent.Event) tea.Cmd {
	switch ev.Kind {
	case agent.EventBlock:
		m.blocks = ev.Update.Blocks
		m.updatePendingApprovals()
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

func (m *Model) updatePendingApprovals() {
	current := ""
	if len(m.pendingApprovals) > 0 {
		current = m.pendingApprovals[0].ToolCallID()
	}
	m.pendingApprovals = m.pendingApprovals[:0]
	for _, block := range m.blocks {
		if block.Tool != nil && block.Tool.Status == agent.ToolAwaitingApproval {
			m.pendingApprovals = append(m.pendingApprovals, block)
		}
	}
	if len(m.pendingApprovals) == 0 || m.pendingApprovals[0].ToolCallID() != current {
		m.approvalChoice = 0
	}
}

// setDarkBackground adapts styles to the detected terminal background.
func (m *Model) setDarkBackground(isDark bool) {
	if isDark == m.styles.IsDark {
		return
	}
	m.applyStyles(styles.Default(isDark))
	m.refreshViewport()
}

func (m *Model) resize(w, h int) {
	m.width, m.height = w, h
	m.editor.SetWidth(w)
	m.list.SetWidth(w)
	if m.picker != nil {
		m.picker.SetSize(w, h)
	}
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
	m.list.SetHeight(max(1, m.height-1-m.composerHeight()))
	m.list.SetItems(m.blocks)
}
