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
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	conversationview "github.com/DataDog/bits-cli/internal/tui/conversations"
	loginui "github.com/DataDog/bits-cli/internal/tui/login"
	"github.com/DataDog/bits-cli/internal/tui/splash"
)

// historyLoadTimeout bounds the conversation-history fetch on startup.
const historyLoadTimeout = 30 * time.Second

// mouseWheelDelta is how many transcript lines one wheel notch scrolls,
const mouseWheelDelta = 1

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
		epoch uint64
		url   string
		err   error
	}
	settingsOpenResultMsg struct {
		epoch uint64
		url   string
		err   error
	}
	logoutResultMsg struct {
		generation uint64
		err        error
	}
)

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

// postNotice adds a session-only message at the current point in the agent
// transcript. It never enters the engine's transcript or backend.
func (m *Model) postNotice(n chat.Notice) tea.Cmd {
	if n.Empty() {
		return nil
	}
	m.nextNoticeID++
	m.notices = append(m.notices, chat.NoticeItem{ID: m.nextNoticeID, After: len(m.transcript.Blocks), Notice: n})
	if m.list != nil {
		m.syncTranscript()
	}
	return nil
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
	focusEditor      focus = iota // transcript scroll + text input (and its completion menu)
	focusToolUI                   // a tool's interactive UI replaces the composer
	focusApproval                 // a tool approval is pending
	focusPicker                   // the /resume conversation picker
	focusStatus                   // the local /status document
	focusPermissions              // the permissions picker
	focusLogin                    // startup OAuth
)

func (m *Model) focus() focus {
	switch m.mode {
	case ModeLogin:
		return focusLogin
	case ModeConversations:
		return focusPicker
	case ModeStatus:
		return focusStatus
	case ModePermissions:
		return focusPermissions
	case ModeChat, ModeTermInit:
		if m.activeToolUI != nil {
			return focusToolUI
		}
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
	hand := m.mode == ModeChat && !m.frame.tooSmall && (m.list.Hovered() || m.follow.hover)
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

// Update is the single message handler. Only this thread touches Model state.
// It routes the message to the owning surface, then derives everything that
// follows from state — geometry, animations, editor focus, pointer shape — in
// one place, so no handler has to remember to relayout.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Global quit wins over every chat surface and skips reconciliation (we are
	// tearing down). Login handles its own ctrl+c.
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+c" && m.mode != ModeLogin {
		return m.quit()
	}

	var cmd tea.Cmd
	switch msg := msg.(type) {
	case uv.KittyGraphicsEvent:
		// The probe answer can arrive in any mode, login included.
		switch {
		case splash.ImageRejected(msg):
			m.splashReady = false
		case !m.splashReady && splash.ProbeSucceeded(msg):
			m.splashReady = true
			cmd = splash.Transmit()
		}
	case recentConversationsMsg:
		// A failed background read the user never asked for stays silent.
		if msg.result.Err == nil {
			m.resume.setConversations(msg.result.Conversations)
		}
	default:
		switch {
		case m.mode == ModeLogin:
			cmd = m.updateLogin(msg)
		case m.frame.tooSmall && isUserInput(msg):
			// Only a resize hint is visible, so input must not drive a hidden
			// surface. System and engine messages still flow.
		default:
			_, cmd = m.dispatch(msg)
		}
	}
	if m.mode == ModeLogin {
		return m, cmd
	}

	clientSkills := m.syncClientSkills()
	m.relayout()
	return m, tea.Batch(cmd, clientSkills, m.syncAnimations(), m.reconcileFocus(), m.reconcilePointerShape())
}

func isUserInput(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg, tea.PasteMsg:
		return true
	}
	return false
}

func (m *Model) quit() (tea.Model, tea.Cmd) {
	// /resume may own a live list/history request that must be cancelled before
	// the application exits.
	if m.focus() == focusPicker {
		m.closeConversationPicker()
	}
	m.skillMenu.task.stop()
	m.statusTask.stop()
	if m.op.cancel != nil {
		m.op.cancel()
	}
	m.stopEntitySearch()
	closeFileSearch := m.stopFileSearch()
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
	if m.handleFollowMouse(msg) {
		return m, nil
	}
	switch msg := msg.(type) {
	case clientSkillsResultMsg:
		return m, m.applyClientSkills(msg)
	case clientSkillsRetryMsg:
		return m, m.retryClientSkills(msg)

	case toolUIOpenedMsg:
		m.activateToolUI(msg.request)
		return m, waitToolUI(m.toolUI)

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
		return m, nil

	case fileSearchSnapshotMsg:
		return m, m.applyFileSearchSnapshot(msg)

	case fileSearchClosedMsg:
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseWheelMsg:
		return m, m.handleMouseWheel(msg)

	case tea.MouseClickMsg:
		if m.focus() == focusToolUI {
			return m, m.updateToolUI(msg)
		}
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
		if !m.op.accepts(msg.generation) {
			return m, nil
		}
		cmd := m.applyEvent(msg.ev)
		m.syncStatus()
		if msg.ev.Kind == agent.EventTranscript {
			m.syncTranscript()
		}
		return m, tea.Batch(cmd, waitEvent(msg.generation, m.op.events))

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
		if msg.generation == m.statusTask.gen {
			m.closeStatus()
		}
		return m, nil

	case webOpenResultMsg:
		if msg.epoch != m.conversationEpoch {
			return m, nil
		}
		if msg.err != nil {
			return m, m.postNotice(notice(chat.NoticeError, msg.err, "Could not open a browser. Open this URL: %s", msg.url))
		}
		return m, m.postNotice(notice(chat.NoticeInfo, nil, "Opened this conversation in your browser: %s", msg.url))

	case settingsOpenResultMsg:
		if msg.epoch != m.conversationEpoch {
			return m, nil
		}
		if msg.err != nil {
			return m, m.postNotice(notice(chat.NoticeError, msg.err, "Could not open a browser. Open this URL: %s", msg.url))
		}
		return m, m.postNotice(notice(chat.NoticeInfo, nil, "Opened Assistant settings in your browser: %s", msg.url))
	}

	if m.focus() == focusToolUI {
		return m, m.updateToolUI(msg)
	}
	if m.focus() == focusPicker {
		return m, m.updateConversationPicker(msg)
	}
	if m.focus() == focusStatus {
		return m, m.updateStatus(msg)
	}
	if m.focus() == focusPermissions {
		return m, nil
	}
	// Paste, cursor blink, and other editor-bound input. When the editor is not
	// the focus it is blurred and ignores these, showing no cursor.
	cmd := m.editor.Update(msg)
	return m, tea.Batch(cmd, m.syncCompletionSearches())
}

func (m *Model) openConversationInBrowser() tea.Cmd {
	if strings.TrimSpace(m.convID) == "" {
		return m.postNotice(notice(chat.NoticeWarn, nil, "Start a conversation before using /web."))
	}
	if m.engine == nil {
		return m.postNotice(notice(chat.NoticeError, nil, "This conversation has no Datadog web site."))
	}
	target, err := browser.ConversationURL(m.engine.Site(), m.convID)
	if err != nil {
		return m.postNotice(notice(chat.NoticeError, err, "Could not build a web link for this conversation."))
	}
	openURL := m.openURL
	epoch := m.conversationEpoch
	if openURL == nil {
		openURL = browser.Open
	}
	return func() tea.Msg {
		return webOpenResultMsg{epoch: epoch, url: target, err: openURL(context.Background(), target)}
	}
}

func (m *Model) openSettingsInBrowser() tea.Cmd {
	if m.engine == nil {
		return m.postNotice(notice(chat.NoticeError, nil, "Assistant settings have no Datadog web site."))
	}
	target, err := browser.SettingsURL(m.engine.Site())
	if err != nil {
		return m.postNotice(notice(chat.NoticeError, err, "Could not build a web link for Assistant settings."))
	}
	openURL := m.openURL
	epoch := m.conversationEpoch
	if openURL == nil {
		openURL = browser.Open
	}
	return func() tea.Msg {
		return settingsOpenResultMsg{epoch: epoch, url: target, err: openURL(context.Background(), target)}
	}
}

func (m *Model) beginSelection(msg tea.MouseClickMsg) tea.Cmd {
	// A completion menu is an editor overlay, not part of the chat document.
	// Close it before taking the frame so the click selects the underlying rows.
	if m.editor.MenuOpen() {
		m.editor.CloseMenu()
	}
	scope := selectionScopeLower
	if msg.Y < m.frame.transcript.Max.Y {
		scope = selectionScopeTranscript
	}
	m.selection.beginClick(m.visibleSelectionFrame(scope), scope, msg.X, msg.Y, m.hasPendingAccordionToggle, time.Now())
	return nil
}

func (m *Model) extendSelection(msg tea.MouseMotionMsg) tea.Cmd {
	if !m.selection.selecting() {
		return nil
	}
	m.selection.extendGesture(m.visibleSelectionFrame(m.selection.scope), msg.X, msg.Y)
	return m.armSelectionScroll()
}

func (m *Model) finishSelection(msg tea.MouseReleaseMsg) tea.Cmd {
	if !m.selection.selecting() {
		return nil
	}
	frame := m.visibleSelectionFrame(m.selection.scope)
	document := frame
	if m.selection.scope == selectionScopeTranscript {
		document = m.transcriptSelectionFrame()
	}
	text := m.selection.finishGesture(frame, msg.X, msg.Y, document)

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
	pointer := m.selection.pointer
	m.selection.extendGesture(m.visibleSelectionFrame(m.selection.scope), pointer.X, pointer.Y)
	return m.armSelectionScroll()
}

func (m *Model) clearSelection() {
	m.selection.clear()
}

func (m *Model) handleMouseWheel(msg tea.MouseWheelMsg) tea.Cmd {
	if m.focus() == focusToolUI {
		if msg.Y >= m.frame.dock.Min.Y {
			return m.updateToolUI(msg)
		}
		switch msg.Button {
		case tea.MouseWheelUp:
			m.list.ScrollBy(-mouseWheelDelta)
		case tea.MouseWheelDown:
			m.list.ScrollBy(mouseWheelDelta)
		default:
		}
		return nil
	}
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
		default:
		}
		return nil
	}
	switch msg.Button {
	case tea.MouseWheelUp:
		m.list.ScrollBy(-mouseWheelDelta)
	case tea.MouseWheelDown:
		m.list.ScrollBy(mouseWheelDelta)
	default:
	}
	return nil
}

// handleTurnClosed ends the engine operation whose channel closed, then runs
// what was queued behind it.
func (m *Model) handleTurnClosed(msg turnClosedMsg) (tea.Model, tea.Cmd) {
	if !m.op.accepts(msg.generation) {
		return m, nil
	}
	// A cancellation can suppress the local echo after the engine accepted the
	// message. Check its settled transcript before restoring a pending draft.
	if pending := m.op.submission; pending != nil && len(m.engine.Snapshot()) > pending.transcriptLength {
		m.op.submission = nil
	}
	m.restorePendingSubmission()
	done := m.op
	m.op = operation{gen: done.gen}
	if m.chatPhase != chat.PhaseError {
		m.chatPhase = chat.PhaseIdle
	}
	if done.stop == stopAll {
		m.transcript = agent.TranscriptSnapshot{Blocks: m.engine.Snapshot()}
		m.syncTranscript()
	} else if done.cancel != nil {
		done.cancel() // release the turn/restore context
	}
	m.pendingApprovals = nil
	m.approvalChoice = 0
	m.approvalPanel.ResetScroll()
	m.clearToolUIs()

	if done.then == thenLogout {
		// Logging out makes a queued permissions mode moot.
		m.pendingPermissions = ""
		m.syncStatus()
		return m, m.startLogout()
	}
	permissionsCommand := m.applyPendingPermissions()
	m.syncStatus()
	switch {
	case done.then == thenNewConversation:
		return m, tea.Batch(permissionsCommand, m.startNewConversation())
	case done.kind == opRestore && done.stop != stopAll:
		return m, tea.Batch(permissionsCommand, m.resumePendingTools())
	}
	return m, permissionsCommand
}

func (m *Model) updateLogin(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		m.setDarkBackground(msg.IsDark())
	case loginui.CompletedMsg:
		if m.startupTask.running() || m.startupStopping {
			return nil
		}
		factory := m.engineFactory
		factoryCtx, generation := m.startupTask.start(m.startupCtx, 0)
		return func() tea.Msg {
			if factory == nil {
				return engineReadyMsg{generation: generation, err: errors.New("authenticated chat is unavailable")}
			}
			engine, err := factory(factoryCtx)
			return engineReadyMsg{generation: generation, engine: engine, err: err}
		}
	case engineReadyMsg:
		// stopStartup advances the generation, so a stopped factory's result drops.
		if msg.generation != m.startupTask.gen {
			return nil
		}
		m.startupTask.done()
		if msg.err != nil {
			m.startupErr = msg.err
			return tea.Quit
		}
		if msg.engine == nil {
			m.startupErr = errors.New("authenticated chat returned no engine")
			return tea.Quit
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
		return m.initChat()
	}

	if m.loginModel == nil {
		m.startupErr = errors.New("startup login is unavailable")
		return tea.Quit
	}
	next, cmd := m.loginModel.Update(msg)
	if loginModel, ok := next.(*loginui.Model); ok {
		m.loginModel = loginModel
	}
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+c" {
		m.stopStartup()
	}
	if m.loginModel.Canceled() {
		m.stopStartup()
	}
	return cmd
}

func (m *Model) stopStartup() {
	if m.startupStopping {
		return
	}
	m.startupStopping = true
	m.startupTask.stop()
}

// handleKey routes a keypress to the surface that owns input. Global quit is
// handled earlier in Update.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" && m.selection.active() {
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
	case focusToolUI:
		return m, m.updateToolUI(msg)
	case focusApproval:
		return m.handleApprovalKey(msg)
	case focusStatus:
		return m, m.updateStatus(msg)
	case focusPermissions:
		return m, m.updatePermissionsKey(msg)
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
					return m.dispatchCommand(name, "")
				}
				if strings.HasPrefix(name, "skill:") {
					m.editor.AcceptCommand()
					return m.submit()
				}
			}
		}
		cmd := m.editor.Update(msg)
		return m, tea.Batch(cmd, m.syncCompletionSearches())
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
			return m, nil
		case "enter":
			if id, ok := m.resume.selectedID(); ok {
				return m, m.resumeSelectedConversation(id)
			}
		}
	}

	switch msg.String() {
	case "esc":
		m.cancelOperation() // interrupt the running turn
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
	return m, tea.Batch(cmd, m.syncCompletionSearches())
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

	var invocation *agent.SkillInvocation
	if name, arguments, ok := parseClientSkillInvocation(raw); ok {
		invocation = &agent.SkillInvocation{Name: name, Arguments: arguments}
		text = raw
	} else if name, argument, ok := parseCommand(raw); ok {
		m.editor.Reset()
		m.stopEntitySearch()
		return m.dispatchCommand(name, argument)
	}
	if m.op.busy() {
		return m, nil
	}
	turnContext := contextFromAttachments(attachments)
	var submission *pendingSubmission
	if invocation != nil {
		submission = &pendingSubmission{draft: m.editor.TakeDraft(), transcriptLength: len(m.transcript.Blocks)}
	} else {
		m.editor.Reset()
	}
	closeFileSearch := m.stopCompletionSearches()

	ctx, cancel := context.WithCancel(context.Background())
	events := m.engine.StartTurn(ctx, agent.TurnInput{
		Message:     text,
		Skill:       invocation,
		Tools:       m.tools,
		Context:     turnContext,
		OnDeny:      agent.DenyContinue,
		UserContext: tools.UserContext(m.workspace, m.tools),
	})
	wait := m.begin(opTurn, events, cancel)
	m.op.submission = submission
	m.chatPhase = chat.PhaseWaiting
	// Submitting always jumps to the tail and re-engages auto-follow, so the
	// user sees their message and the incoming reply even if they had scrolled up.
	m.list.ScrollToBottom()
	return m, tea.Batch(closeFileSearch, wait)
}

// begin starts an exclusive operation. Its generation drops any late message
// from an earlier one.
func (m *Model) begin(kind opKind, events <-chan agent.Event, cancel context.CancelFunc) tea.Cmd {
	m.op = operation{kind: kind, gen: m.op.gen + 1, events: events, cancel: cancel}
	if events == nil {
		return nil
	}
	return waitEvent(m.op.gen, events)
}

func (m *Model) resumePendingTools() tea.Cmd {
	if m.engine == nil {
		return nil
	}
	if m.engine.SettleRestoredTools(m.tools) {
		m.transcript = agent.TranscriptSnapshot{Blocks: m.engine.Snapshot()}
		m.syncTranscript()
	}
	if m.tools == nil || !m.engine.CanResumeTools(m.tools) {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := m.engine.ResumePendingTools(ctx, agent.TurnInput{Tools: m.tools, OnDeny: agent.DenyContinue})
	m.chatPhase = chat.PhaseWaiting
	m.list.ScrollToBottom()
	return m.begin(opTurn, events, cancel)
}

// cancelOperation cancels a running engine operation once and drops its tool
// UIs. Logout is never interrupted this way.
func (m *Model) cancelOperation() {
	if m.op.events == nil || m.op.stop == stopAll {
		return
	}
	m.op.stop = stopAll
	m.op.cancel()
	m.clearToolUIs()
}

// after queues next for when the running operation ends, and cancels the
// operation so that happens soon.
func (m *Model) after(next followUp) {
	m.op.then = max(m.op.then, next)
	m.cancelOperation()
}

// applyEvent folds one engine event into the block snapshot / status. The switch
// is exhaustive over agent.EventKind. It returns a command for side effects (an
// error posts a local transcript message); nil otherwise.
func (m *Model) applyEvent(ev agent.Event) tea.Cmd {
	m.observeEvent(ev)
	switch ev.Kind {
	case agent.EventTranscript:
		if pending := m.op.submission; pending != nil && len(ev.Transcript.Blocks) > pending.transcriptLength {
			m.op.submission = nil
		}
		m.transcript = ev.Transcript
		m.updatePendingApprovals(ev.Transcript.PendingApprovals())
		m.reconcileToolUI()
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
		m.restorePendingSubmission()
		// A failure during restore is benign: drop to idle with a notice so the
		// user can still type. A failure mid-turn is the turn's error state.
		if m.op.kind == opRestore {
			m.chatPhase = chat.PhaseIdle
			if ev.Err != nil {
				return m.postNotice(noticeForError("restore failed", ev.Err))
			}
		} else {
			m.chatPhase = chat.PhaseError
			if ev.Err != nil {
				return m.postNotice(noticeForError("", ev.Err))
			}
		}
	}
	return nil
}

// restorePendingSubmission runs only for the current operation. A queued
// conversation switch or logout discards its draft along with the operation.
func (m *Model) restorePendingSubmission() {
	pending := m.op.submission
	m.op.submission = nil
	if pending != nil && m.op.then == thenNothing {
		m.editor.RestoreDraft(pending.draft)
	}
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
}

func (m *Model) resize(w, h int) {
	m.clearSelection()
	m.follow = followControl{}
	m.width, m.height = w, h
	m.editor.SetWidth(w)
	m.list.SetWidth(w)
	if m.picker != nil {
		m.picker.SetSize(w, h)
	}
	m.status.SetSize(w, h)
	if m.mode == ModeTermInit {
		m.setMode(ModeChat)
	}
}

// relayout derives the frame from current state and sizes the transcript to
// it. Update calls it once, after every handler has run.
func (m *Model) relayout() {
	if m.mode == ModeTermInit {
		return
	}
	m.layoutToolUI()
	m.editor.SetPlaceholder(m.promptPlaceholder())
	m.frame = m.layout()
	m.editor.SetMenuHeight(m.frame.composerTop())
	m.list.SetHeight(m.frame.transcript.Dy())
	m.list.SetHeader(m.headerView())
}

// syncTranscript rebuilds presentation metadata after m.transcript changes.
func (m *Model) syncTranscript() {
	m.list.SetTranscript(m.transcript.Blocks, m.notices)
}
