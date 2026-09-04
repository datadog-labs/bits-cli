package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/browser"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	conversationview "github.com/DataDog/bits-cli/internal/tui/conversations"
	loginui "github.com/DataDog/bits-cli/internal/tui/login"
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
	webOpenResultMsg struct {
		url string
		err error
	}
	logoutResultMsg struct {
		generation uint64
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

// focus identifies which surface currently owns keyboard input. It is derived
// from mode and turn state, never stored, so input routing and the editor's
// cursor cannot disagree about who is active.
type focus int

const (
	focusEditor   focus = iota // transcript scroll + text input (and its completion menu)
	focusApproval              // a tool approval is pending
	focusPicker                // the /resume conversation picker
	focusStatus                // the local /status document
	focusLogin                 // startup OAuth
)

func (m *Model) focus() focus {
	switch m.mode {
	case ModeLogin:
		return focusLogin
	case ModeConversations:
		return focusPicker
	case ModeStatus:
		return focusStatus
	case ModeChat, ModeTermInit:
		if len(m.pendingApprovals) > 0 {
			return focusApproval
		}
	}
	return focusEditor
}

// reconcileFocus makes the editor's focus (and thus its cursor/blink) match the
// current input owner. Called once at the end of Update so every state change
// reconciles uniformly: the editor blinks only while it owns input and stays
// dark — ignoring keys and pastes — whenever the approval, picker, or login
// owns it. It returns the blink command on the transition back into input.
func (m *Model) reconcileFocus() tea.Cmd {
	want := m.focus() == focusEditor
	if want == m.editor.Focused() {
		return nil
	}
	if want {
		return m.editor.Focus()
	}
	m.editor.Blur()
	return nil
}

// Update is the single message handler. Only this thread touches Model state. It
// routes the message to the owning surface, then reconciles editor focus so the
// cursor always tracks the active surface.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// The color profile is handled ahead of the mode check because Bubble Tea
	// reports it once, at startup — which is while the login screen owns the
	// screen. Routing it through updateLogin would drop it, and nothing
	// re-requests it after the handoff, so a low-color terminal reached through
	// login would keep a truecolor sweep it cannot render.
	if profile, ok := msg.(tea.ColorProfileMsg); ok {
		return m, m.setColorProfile(profile.Profile)
	}
	if m.mode == ModeLogin {
		return m.updateLogin(msg)
	}
	// Global quit wins over every surface and must not reconcile focus (we are
	// tearing down); the picker's in-flight request is abandoned on the way out.
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+c" {
		return m.quit()
	}
	// When the chat is too small to render, View shows only a resize hint, so no
	// surface is visible. Drop user input before it can drive a hidden surface
	// (an approval granted blind, the composer, scrolling); ctrl+c already quit
	// above. System and engine messages still flow so the app keeps working and
	// can be resized back.
	if m.chatViewTooSmall() {
		switch msg.(type) {
		case tea.KeyPressMsg, tea.MouseWheelMsg, tea.PasteMsg:
			return m, nil
		}
	}
	next, cmd := m.dispatch(msg)
	return next, tea.Batch(cmd, m.reconcileFocus())
}

func (m *Model) quit() (tea.Model, tea.Cmd) {
	// /resume may own a live list/history request that must be cancelled before
	// the application exits.
	if m.focus() == focusPicker {
		m.abandonConversationPicker()
	}
	if m.statusCancel != nil {
		m.statusCancel()
		m.statusCancel = nil
	}
	if m.cancelTurn != nil {
		m.cancelTurn()
	}
	if m.logoutCancel != nil {
		m.logoutCancel()
	}
	return m, tea.Quit
}

// dispatch routes one message to the owning surface. Non-input messages (resize,
// engine events, conversation results, notices) are handled directly; keyboard,
// mouse, and editor-bound input are routed by focus().
func (m *Model) dispatch(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil

	case tea.BackgroundColorMsg:
		m.setDarkBackground(msg.IsDark())
		return m, nil

	case animTickMsg:
		return m, m.advanceAnimation(msg)

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseWheelMsg:
		return m, m.handleMouseWheel(msg)

	case turnEventMsg:
		if !m.acceptRemoteMessage(msg.generation) {
			return m, nil
		}
		cmd := m.applyEvent(msg.ev)
		m.syncStatus()
		m.refreshViewport()
		// Tool state only changes on engine events, so this is where the chip
		// animation starts and stops.
		return m, tea.Batch(cmd, m.syncAnimation(), waitEvent(msg.generation, m.turnEvents))

	case turnClosedMsg:
		return m.handleTurnClosed(msg)

	case logoutResultMsg:
		return m.applyLogoutResult(msg)

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

	case statusEnvironmentMsg:
		m.applyStatusEnvironment(msg)
		return m, nil

	case statusIdentityMsg:
		m.applyStatusIdentity(msg)
		return m, nil

	case statusClosedMsg:
		if msg.generation == m.statusGeneration && m.mode == ModeStatus {
			m.closeStatus()
		}
		return m, nil

	case noticeExpiredMsg:
		if msg.seq == m.noticeSeq {
			m.notice = chat.Notice{}
		}
		return m, nil

	case webOpenResultMsg:
		if msg.err != nil {
			return m, m.showNotice(notice(chat.NoticeError, msg.err, "Could not open a browser. Open this URL: %s", msg.url), 0)
		}
		return m, m.showNotice(notice(chat.NoticeInfo, nil, "Opened this conversation in your browser: %s", msg.url), 0)
	}

	if m.focus() == focusPicker {
		return m, m.updateConversationPicker(msg)
	}
	if m.focus() == focusStatus {
		return m, m.updateStatus(msg)
	}
	// Paste, cursor blink, and other editor-bound input; a paste can change the
	// editor's height, so relayout. When the editor is not the focus it is
	// blurred and ignores these, showing no cursor.
	cmd := m.editor.Update(msg)
	m.refreshViewport()
	return m, cmd
}

func (m *Model) openConversationInBrowser() tea.Cmd {
	if strings.TrimSpace(m.convID) == "" {
		return m.showNotice(notice(chat.NoticeWarn, nil, "Start a conversation before using /web."), 0)
	}
	if m.engine == nil {
		return m.showNotice(notice(chat.NoticeError, nil, "This conversation has no Datadog web site."), 0)
	}
	target, err := browser.ConversationURL(m.engine.Site(), m.convID)
	if err != nil {
		return m.showNotice(notice(chat.NoticeError, err, "Could not build a web link for this conversation."), 0)
	}
	openURL := m.openURL
	if openURL == nil {
		openURL = browser.Open
	}
	return func() tea.Msg {
		return webOpenResultMsg{url: target, err: openURL(context.Background(), target)}
	}
}

func (m *Model) handleMouseWheel(msg tea.MouseWheelMsg) tea.Cmd {
	if m.focus() == focusPicker {
		return m.updateConversationPicker(msg)
	}
	if m.focus() == focusStatus {
		return m.updateStatus(msg)
	}
	switch msg.Button {
	case tea.MouseWheelUp:
		m.list.ScrollBy(-mouseWheelDelta)
	case tea.MouseWheelDown:
		m.list.ScrollBy(mouseWheelDelta)
	}
	return nil
}

func (m *Model) handleTurnClosed(msg turnClosedMsg) (tea.Model, tea.Cmd) {
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
	m.syncStatus()
	if m.pendingNew {
		m.pendingNew = false
		// Resync *after* the reset. A cancelled client tool can leave its
		// block reporting ToolRunning, so syncing first would see no change
		// and leave the chain armed — and the reset that follows empties the
		// transcript with nothing left to disarm it.
		return m, tea.Batch(m.startNewConversation(), m.syncAnimation())
	}
	if m.pendingLogout {
		m.pendingLogout = false
		return m, tea.Batch(m.startLogout(), m.syncAnimation())
	}
	// Resync in case the turn ended with nothing left in flight.
	//
	// Known gap: a cancelled turn does NOT settle its tools. The engine's
	// tool loop returns on ctx.Done() after cancelling the per-tool
	// contexts, without emitting a final block state, so after a Ctrl+C the
	// blocks still report ToolRunning. This sync therefore sees no change
	// and leaves the tick armed against a tool that is already dead, until
	// the conversation is reset. Settling those blocks belongs in the
	// engine, not here; accepted as out of scope for this change.
	return m, m.syncAnimation()
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

// handleKey routes a keypress to the surface that owns input. Global quit is
// handled earlier in Update.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.focus() {
	case focusPicker:
		return m, m.updateConversationPicker(msg)
	case focusApproval:
		return m.handleApprovalKey(msg)
	case focusStatus:
		return m, m.updateStatus(msg)
	default:
		return m.handleEditorKey(msg)
	}
}

// handleEditorKey handles keys while the editor owns input. The completion menu,
// when open, is a sub-state of the editor and intercepts navigation keys.
func (m *Model) handleEditorKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// While the completion menu is open it owns navigation keys (arrows, tab,
	// enter to accept, esc to close). Enter dispatches the selected registered
	// slash command directly, including a partial command completion.
	if m.editor.MenuOpen() {
		if msg.String() == "enter" {
			if name, selected := m.editor.SelectedCommand(); selected {
				if _, registered := lookupCommand(name); registered {
					m.editor.Reset()
					return m.dispatchCommand(name)
				}
			}
		}
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
	case "esc":
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
	raw := m.editor.Value()
	text := strings.TrimSpace(raw)
	if text == "" {
		return m, nil
	}

	// Slash commands are a native control plane: they never reach the model.
	if name, ok := parseCommand(raw); ok {
		m.editor.Reset()
		return m.dispatchCommand(name)
	}
	if m.pendingLogout || m.logoutRunning {
		return m, nil
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
	m.observeEvent(ev)
	switch ev.Kind {
	case agent.EventTranscript:
		m.blocks = ev.Transcript.Blocks
		m.updatePendingApprovals(ev.Transcript.PendingApprovals())
		if ev.Transcript.HasStreamingContent() {
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

func (m *Model) updatePendingApprovals(pending []agent.Block) {
	current := ""
	if len(m.pendingApprovals) > 0 {
		current = m.pendingApprovals[0].ToolCallID()
	}
	m.pendingApprovals = pending
	if len(m.pendingApprovals) == 0 || m.pendingApprovals[0].ToolCallID() != current {
		m.approvalChoice = 0
	}
}

// setDarkBackground adapts styles to the detected terminal background.
func (m *Model) setDarkBackground(isDark bool) {
	if isDark == m.styles.IsDark {
		return
	}
	m.applyStyles(m.theme(isDark))
	m.refreshViewport()
}

func (m *Model) resize(w, h int) {
	m.width, m.height = w, h
	m.editor.SetWidth(w)
	m.list.SetWidth(w)
	if m.picker != nil {
		m.picker.SetSize(w, h)
	}
	if m.status != nil {
		m.status.SetSize(w, h)
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
