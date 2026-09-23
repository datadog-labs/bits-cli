package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/browser"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	conversationview "github.com/DataDog/bits-cli/internal/tui/conversations"
	loginui "github.com/DataDog/bits-cli/internal/tui/login"
	"github.com/DataDog/bits-cli/internal/tui/splash"
)

// historyLoadTimeout bounds the conversation-history fetch on startup.
const historyLoadTimeout = 30 * time.Second

// mouseWheelDelta is how many transcript lines one wheel notch scrolls,
const mouseWheelDelta = 1

// defaultNoticeTTL is how long a transient status notice stays before it clears.
const defaultNoticeTTL = 10 * time.Second

// selectionAutoScrollInterval controls how often an active drag advances the
// transcript while its pointer rests against a viewport edge.
const selectionAutoScrollInterval = 25 * time.Millisecond

// turnEventMsg carries one engine event into Update; turnClosedMsg signals the
// turn's channel was closed (turn finished or cancelled). A history restore runs
// through the same pump, so its events flow here too.
type (
	turnEventMsg struct {
		generation uint64
		ev         agent.Event
	}
	turnClosedMsg    struct{ generation uint64 }
	selectionTickMsg struct{ token uint64 }
	engineReadyMsg   struct {
		generation uint64
		engine     *agent.Engine
		err        error
	}
	webOpenResultMsg struct {
		url string
		err error
	}
	settingsOpenResultMsg struct {
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

type approvalChoice struct {
	decision     agent.ApprovalDecision
	label        string
	compactLabel string
}

var approvalChoices = [...]approvalChoice{
	{decision: agent.ApprovalAllowOnce, label: "Allow", compactLabel: "Allow"},
	{decision: agent.ApprovalAllowSession, label: "Allow for session", compactLabel: "Session"},
	{decision: agent.ApprovalDeny, label: "Deny", compactLabel: "Deny"},
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

// reconcilePointerShape shows a hand over a clickable accordion row via OSC 22
// (kitty implements it fully; xterm and foot carry an older, simpler version;
// other terminals ignore it). It must go through tea.Raw rather than
// View.Content: Bubble Tea's renderer parses Content into a cell buffer that
// only special-cases SGR and OSC 8, silently swallowing any other escape
// sequence instead of writing it to the terminal.
//
// Exiting never resets it here: main resets the shape once the program has
// stopped, which covers every quit path.
func (m *Model) reconcilePointerShape() tea.Cmd {
	hand := m.mode == ModeChat && !m.chatViewTooSmall() && m.list.Hovered()
	if hand == m.pointerIsHand {
		return nil
	}
	m.pointerIsHand = hand
	shape := "default"
	if hand {
		shape = "pointer"
	}
	return tea.Raw(ansi.SetPointerShape(shape))
}

// Update is the single message handler. Only this thread touches Model state. It
// routes the message to the owning surface, then reconciles editor focus so the
// cursor always tracks the active surface.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// The probe answer can arrive in any mode and no surface uses it. Skipping
	// reconcileFocus is safe: it is idempotent and runs on the next message.
	if event, ok := msg.(uv.KittyGraphicsEvent); ok {
		switch {
		case splash.ImageRejected(event):
			m.splashReady = false
			m.layoutTranscript()
		case !m.splashReady && splash.ProbeSucceeded(event):
			m.splashReady = true
			m.layoutTranscript()
			return m, splash.Transmit()
		}
		return m, nil
	}
	if event, ok := msg.(recentConversationsMsg); ok {
		// A failed background read the user never asked for stays silent.
		if event.result.Err == nil {
			m.resume.setConversations(event.result.Conversations)
			m.layoutTranscript()
		}
		return m, nil
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
		case tea.KeyPressMsg, tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg, tea.PasteMsg:
			return m, nil
		}
	}
	next, cmd := m.dispatch(msg)
	return next, tea.Batch(cmd, m.syncAnimations(), m.reconcileFocus(), m.reconcilePointerShape())
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
	m.stopEntitySearch()
	closeFileSearch := m.stopFileSearch()
	if m.logoutCancel != nil {
		m.logoutCancel()
	}
	if closeFileSearch == nil {
		return m, tea.Quit
	}
	return m, func() tea.Msg {
		_ = closeFileSearch()
		return tea.Quit()
	}
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

	case animationTickMsg:
		return m, m.advanceAnimations(msg)

	case entitySearchDebounceMsg:
		return m, m.beginEntitySearch(msg)

	case entitySearchResultMsg:
		m.applyEntitySearchResult(msg)
		m.layoutTranscript()
		return m, nil

	case fileSearchSnapshotMsg:
		cmd := m.applyFileSearchSnapshot(msg)
		m.layoutTranscript()
		return m, cmd

	case fileSearchClosedMsg:
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseWheelMsg:
		return m, m.handleMouseWheel(msg)

	case tea.MouseClickMsg:
		if m.mode == ModeChat {
			if msg.Button == tea.MouseLeft {
				// The press might turn into a drag-select, so it isn't a toggle
				// yet: arm it, and let finishSelection decide on release whether
				// the gesture stayed a plain click or moved and became a
				// selection.
				m.pendingAccordionToggle, m.hasPendingAccordionToggle = m.list.HeaderAt(msg.Y)
				return m, m.beginSelection(msg)
			}
			return m, nil
		}

	case tea.MouseMotionMsg:
		if m.mode == ModeChat {
			m.list.SetPointerRow(msg.Y)
			if m.selection.selecting() {
				return m, m.extendSelection(msg)
			}
			return m, nil
		}

	case tea.MouseReleaseMsg:
		if m.mode == ModeChat {
			if m.selection.selecting() {
				return m, m.finishSelection(msg)
			}
			return m, nil
		}

	case selectionTickMsg:
		return m, m.advanceSelectionScroll(msg)

	case turnEventMsg:
		if !m.acceptRemoteMessage(msg.generation) {
			return m, nil
		}
		cmd := m.applyEvent(msg.ev)
		m.syncStatus()
		if msg.ev.Kind == agent.EventTranscript {
			m.syncTranscript()
		} else {
			m.layoutTranscript()
		}
		return m, tea.Batch(cmd, waitEvent(msg.generation, m.turnEvents))

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

	case statusWorkspaceMsg:
		m.applyStatusWorkspace(msg)
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

	case settingsOpenResultMsg:
		if msg.err != nil {
			return m, m.showNotice(notice(chat.NoticeError, msg.err, "Could not open a browser. Open this URL: %s", msg.url), 0)
		}
		return m, m.showNotice(notice(chat.NoticeInfo, nil, "Opened Assistant settings in your browser: %s", msg.url), 0)
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
	m.layoutTranscript()
	return m, batchCommands(cmd, m.syncCompletionSearches())
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

func (m *Model) openSettingsInBrowser() tea.Cmd {
	if m.engine == nil {
		return m.showNotice(notice(chat.NoticeError, nil, "Assistant settings have no Datadog web site."), 0)
	}
	target, err := browser.SettingsURL(m.engine.Site())
	if err != nil {
		return m.showNotice(notice(chat.NoticeError, err, "Could not build a web link for Assistant settings."), 0)
	}
	openURL := m.openURL
	if openURL == nil {
		openURL = browser.Open
	}
	return func() tea.Msg {
		return settingsOpenResultMsg{url: target, err: openURL(context.Background(), target)}
	}
}

func (m *Model) beginSelection(msg tea.MouseClickMsg) tea.Cmd {
	// A completion menu is an editor overlay, not part of the chat document.
	// Close it before taking the frame so the click selects the underlying rows.
	if m.editor.MenuOpen() {
		m.editor.CloseMenu()
	}
	transcriptHeight := m.list.Height()
	scope := selectionScopeAt(msg.Y, transcriptHeight)
	frame := m.visibleSelectionFrame(scope)
	m.selection.beginGesture(frame, scope, msg.X, msg.Y, transcriptHeight, m.height)
	return nil
}

func (m *Model) extendSelection(msg tea.MouseMotionMsg) tea.Cmd {
	if !m.selection.selecting() {
		return nil
	}
	transcriptHeight := m.list.Height()
	frame := m.visibleSelectionFrame(m.selection.scope)
	m.selection.extendGesture(frame, msg.X, msg.Y, transcriptHeight, m.height)
	return m.armSelectionScroll()
}

func (m *Model) finishSelection(msg tea.MouseReleaseMsg) tea.Cmd {
	if !m.selection.selecting() {
		return nil
	}
	transcriptHeight := m.list.Height()
	frame := m.visibleSelectionFrame(m.selection.scope)
	document := frame
	if m.selection.scope == selectionScopeTranscript {
		document = m.transcriptSelectionFrame()
	}
	text := m.selection.finishGesture(frame, msg.X, msg.Y, transcriptHeight, m.height, document)

	// A gesture that never turned into a real range (anchor == focus) was a
	// plain click, not a drag-select: if it started on an accordion row,
	// that's a toggle. A real drag leaves anchor != focus even when the
	// selected text trims to "", so this checks the range, not the text.
	if m.hasPendingAccordionToggle && !m.selection.selected() {
		m.list.ToggleDisclosure(m.pendingAccordionToggle)
	}
	m.hasPendingAccordionToggle = false

	if text == "" {
		return nil
	}
	return tea.SetClipboard(text)
}

func (m *Model) armSelectionScroll() tea.Cmd {
	token, ok := m.selection.armScroll()
	if !ok {
		return nil
	}
	return tea.Tick(selectionAutoScrollInterval, func(time.Time) tea.Msg {
		return selectionTickMsg{token: token}
	})
}

func (m *Model) advanceSelectionScroll(msg selectionTickMsg) tea.Cmd {
	edge, ok := m.selection.consumeScrollTick(msg.token)
	if !ok {
		return nil
	}
	if !m.list.ScrollByChanged(edge) {
		m.selection.stopScroll()
		return nil
	}
	frame := m.visibleSelectionFrame(m.selection.scope)
	pointer := m.selection.pointer
	m.selection.extendGesture(frame, pointer.X, pointer.Y, m.list.Height(), m.height)
	return m.armSelectionScroll()
}

func (m *Model) clearSelection() {
	m.selection.clear()
}

func (m *Model) handleMouseWheel(msg tea.MouseWheelMsg) tea.Cmd {
	if m.focus() == focusPicker {
		return m.updateConversationPicker(msg)
	}
	if m.focus() == focusStatus {
		return m.updateStatus(msg)
	}
	if m.focus() == focusApproval {
		switch msg.Button {
		case tea.MouseWheelUp:
			m.approvalPanel.ScrollBy(-mouseWheelDelta)
		case tea.MouseWheelDown:
			m.approvalPanel.ScrollBy(mouseWheelDelta)
		}
		return nil
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
	m.approvalPanel.ResetScroll()
	if m.cancelTurn != nil && !m.cancelRequested {
		m.cancelTurn() // release the turn/restore context
	}
	m.cancelTurn = nil
	m.cancelRequested = false
	m.syncStatus()
	if m.pendingNew {
		m.pendingNew = false
		return m, m.startNewConversation()
	}
	if m.pendingLogout {
		m.pendingLogout = false
		return m, m.startLogout()
	}
	return m, nil
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
		if m.entitySearcher == nil {
			m.entitySearcher = msg.engine
		}
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
	if msg.String() == "esc" && (m.selection.selecting() || m.selection.selected()) {
		m.clearSelection()
		return m, nil
	}
	// ctrl+o toggles every tool block whenever the transcript is visible,
	// whichever chat surface (editor, completion menu, approval) owns input.
	if msg.String() == "ctrl+o" && m.mode == ModeChat {
		m.list.ToggleAllDisclosure()
		return m, nil
	}
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
		if msg.String() == "enter" && !m.editor.MenuHasCandidates() {
			m.editor.CloseMenu()
			return m.submit()
		}
		if msg.String() == "enter" {
			if name, selected := m.editor.SelectedCommand(); selected {
				if _, registered := lookupCommand(name); registered {
					m.editor.Reset()
					m.stopEntitySearch()
					return m.dispatchCommand(name)
				}
			}
		}
		cmd := m.editor.Update(msg)
		m.layoutTranscript()
		return m, batchCommands(cmd, m.syncCompletionSearches())
	}

	// The offer is only visible with an empty composer, so these keys have no
	// competing meaning: submit() already no-ops on empty input.
	if m.showResume() {
		key := msg.String()
		switch key {
		case "up", "down":
			delta := 1
			if key == "up" {
				delta = -1
			}
			m.resume.move(delta, m.resumeVisibleRows())
			m.layoutTranscript()
			return m, nil
		case "enter":
			if id, ok := m.resume.selectedID(); ok {
				return m, m.resumeSelectedConversation(id)
			}
		}
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
	m.layoutTranscript()
	return m, batchCommands(cmd, m.syncCompletionSearches())
}

func (m *Model) handleApprovalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "shift+tab":
		m.approvalChoice = (m.approvalChoice + len(approvalChoices) - 1) % len(approvalChoices)
	case "right", "tab":
		m.approvalChoice = (m.approvalChoice + 1) % len(approvalChoices)
	case "pgup":
		m.approvalPanel.PageUp()
	case "pgdown":
		m.approvalPanel.PageDown()
	case "esc":
		m.respondToApproval(agent.ApprovalDeny)
	case "enter":
		m.respondToApproval(approvalChoices[m.approvalChoice].decision)
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
	attachments := m.editor.Attachments()
	if text == "" && len(attachments) == 0 {
		return m, nil
	}
	if text == "" {
		text = " "
	}

	// Slash commands are a native control plane: they never reach the model.
	if name, ok := parseCommand(raw); ok {
		m.editor.Reset()
		m.stopEntitySearch()
		return m.dispatchCommand(name)
	}
	if m.pendingLogout || m.logoutRunning {
		return m, nil
	}

	if m.turnEvents != nil || m.chatPhase == chat.PhaseLoading {
		return m, nil
	}
	turnContext := contextFromAttachments(attachments)
	m.editor.Reset()
	closeFileSearch := m.stopCompletionSearches()

	ctx, cancel := context.WithCancel(context.Background())
	events := m.engine.StartTurn(ctx, agent.TurnInput{Message: text, Tools: m.tools, Context: turnContext, OnDeny: agent.DenyContinue})
	wait := m.beginRemote(events, cancel)
	m.chatPhase = chat.PhaseWaiting
	m.clearNotice()
	m.layoutTranscript()
	// Submitting always jumps to the tail and re-engages auto-follow, so the
	// user sees their message and the incoming reply even if they had scrolled up.
	m.list.ScrollToBottom()
	return m, batchCommands(closeFileSearch, wait)
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
		m.approvalPanel.ResetScroll()
	}
}

// setDarkBackground adapts styles to the detected terminal background.
func (m *Model) setDarkBackground(isDark bool) {
	if isDark == m.styles.IsDark {
		return
	}
	m.applyStyles(m.theme(isDark))
	m.layoutTranscript()
}

func (m *Model) resize(w, h int) {
	m.clearSelection()
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
	m.layoutTranscript()
}

// layoutTranscript sizes the transcript to the space left by the notice row,
// footer, and (possibly multi-row) editor without rebuilding presentation data.
func (m *Model) layoutTranscript() {
	if m.mode == ModeTermInit {
		return
	}
	m.editor.SetMenuHeight(max(0, m.height-chatFooterHeight-m.editor.Height()))
	m.list.SetHeight(m.transcriptHeight())
	m.list.SetHeader(m.headerView())
}

// syncTranscript rebuilds presentation metadata only after m.blocks changes.
// Editor, cursor, resize, theme, and mode updates need layoutTranscript only.
func (m *Model) syncTranscript() {
	if m.mode == ModeTermInit {
		return
	}
	m.layoutTranscript()
	m.list.SetItems(m.blocks)
}
