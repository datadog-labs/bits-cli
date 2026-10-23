package tui

import (
	"context"
	"errors"
	"image"
	"net/url"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/browser"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	conversationview "github.com/datadog-labs/bits-cli/internal/tui/conversations"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
	loginui "github.com/datadog-labs/bits-cli/internal/tui/login"
	"github.com/datadog-labs/bits-cli/internal/tui/splash"
)

// historyLoadTimeout bounds the conversation-history fetch on startup.
const historyLoadTimeout = 30 * time.Second

// mouseWheelDelta is how many transcript lines one wheel notch scrolls,
const mouseWheelDelta = 1

// selectionAutoScrollInterval controls how often an active drag advances the
// transcript while its pointer rests against a viewport edge.
const selectionAutoScrollInterval = 25 * time.Millisecond

// engineEventMsg carries one event of the engine subscription into Update.
type (
	engineEventMsg   struct{ ev agent.Event }
	selectionTickMsg struct{ token uint64 }
	engineReadyMsg   struct {
		generation uint64
		engine     *agent.Engine
		err        error
	}
	browserOpenResultMsg struct {
		epoch   uint64
		url     string
		success string
		err     error
	}
	logoutResultMsg struct {
		generation uint64
		err        error
	}
)

// postNotice adds a session-only message at the current point in the agent
// transcript. It never enters the engine's transcript or backend.
func (m *Model) postNotice(n chat.Notice) tea.Cmd {
	if n.Empty() {
		return nil
	}
	m.nextNoticeID++
	var after agent.BlockID
	if blocks := m.state.Blocks; len(blocks) > 0 {
		after = blocks[len(blocks)-1].ID
	}
	m.notices = append(m.notices, chat.NoticeItem{ID: m.nextNoticeID, After: after, Notice: n})
	if m.list != nil {
		m.syncTranscript()
	}
	return nil
}

// waitEvent reads one event from the engine subscription; Update re-arms it
// after each event, so one pump serves the engine's lifetime. It stops once
// the subscription closes with the engine; without one there is no pump.
func waitEvent(ch <-chan agent.Event) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		if ev, ok := <-ch; ok {
			return engineEventMsg{ev: ev}
		}
		return nil
	}
}

// focus identifies which surface currently owns keyboard input. It is derived
// from mode and turn state, never stored, so input routing and the editor's
// cursor cannot disagree about who is active.
type focus int

const (
	focusEditor      focus = iota // transcript scroll + text input (and its completion menu)
	focusPrompt                   // a tool approval or a tool's UI is shown (see prompt)
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
		if m.prompt() != nil {
			return focusPrompt
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

// reconcilePointerShape shows a hand over a clickable accordion row or link via OSC 22
// (kitty implements it fully; xterm and foot carry an older, simpler version;
// other terminals ignore it). It must go through tea.Raw rather than
// View.Content: Bubble Tea's renderer parses Content into a cell buffer that
// only special-cases SGR and OSC 8, silently swallowing any other escape
// sequence instead of writing it to the terminal.
//
// Exiting never resets it here: main resets the shape once the program has
// stopped, which covers every quit path.
func (m *Model) reconcilePointerShape() tea.Cmd {
	hand := false
	if m.mode == ModeChat && !m.frame.tooSmall {
		hand = m.list.Hovered() || m.follow.hover || (m.focus() == focusEditor && m.editor.ChipHovered())
		if !hand && !m.frame.replaced && !m.editor.MenuOpen() {
			if hit, ok := m.list.HoveredLink(); ok && webLink(hit.URL) {
				hand = true
			}
		}
	}
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
	case tea.ColorProfileMsg:
		// Arrives in any mode. Selection is the only paint that needs it: when
		// colors are off, ultraviolet drops Fg and Bg but keeps attributes.
		m.monochrome = msg.Profile == colorprofile.NoTTY || msg.Profile == colorprofile.ASCII
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

	loadSkillMenu := m.syncClientSkills()
	m.relayout()
	return m, tea.Batch(cmd, loadSkillMenu, m.syncAnimations(), m.reconcileFocus(), m.reconcilePointerShape())
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

	case toolUIOpenedMsg:
		m.openToolUI(msg.request)
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

	case tea.MouseMsg:
		return m, m.handleMouse(msg)

	case selectionTickMsg:
		return m, m.advanceSelectionScroll(msg)

	case engineEventMsg:
		var cmd tea.Cmd
		switch {
		case msg.ev.Run == 0 && msg.ev.Kind == agent.EventState:
			// A change no run made, such as a command's.
			if msg.ev.State.Version > m.state.Version {
				m.state = msg.ev.State
				m.syncTranscript()
			}
		case m.op.run != 0 && msg.ev.Run == m.op.run:
			// Every run is an operation this model started, so other runs'
			// events are ignored.
			cmd = m.applyRunEvent(msg.ev)
		}
		return m, tea.Batch(cmd, waitEvent(m.events))

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

	case browserOpenResultMsg:
		if msg.epoch != m.conversationEpoch {
			return m, nil
		}
		if msg.err != nil {
			return m, m.postNotice(notice(chat.NoticeError, msg.err, "Could not open a browser. Open this URL: %s", msg.url))
		}
		return m, m.postNotice(notice(chat.NoticeInfo, nil, "%s: %s", msg.success, msg.url))
	}

	if m.focus() == focusPrompt {
		// Paste, cursor blink, and the like belong to the prompt, never to the
		// inert editor below it.
		cmd, _ := m.updatePrompt(msg)
		return m, cmd
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
	if paste, ok := msg.(tea.PasteMsg); ok && m.focus() == focusEditor {
		if handled, err := dropImages(m.editor, paste.Content); handled {
			if err != nil {
				return m, m.postNotice(noticeForError("", err))
			}
			return m, nil
		}
	}
	// Paste, cursor blink, and other editor-bound input. When the editor is not
	// the focus it is blurred and ignores these, showing no cursor.
	cmd := m.editor.Update(msg)
	return m, tea.Batch(cmd, m.syncCompletionSearches())
}

func (m *Model) openConversationInBrowser() tea.Cmd {
	if strings.TrimSpace(m.state.ConversationID) == "" {
		return m.postNotice(notice(chat.NoticeWarn, nil, "Start a conversation before using /web."))
	}
	if m.engine == nil {
		return m.postNotice(notice(chat.NoticeError, nil, "This conversation has no Datadog web site."))
	}
	target, err := browser.ConversationURL(m.engine.Site(), m.state.ConversationID)
	if err != nil {
		return m.postNotice(notice(chat.NoticeError, err, "Could not build a web link for this conversation."))
	}
	return m.openBrowser(target, "Opened this conversation in your browser")
}

func (m *Model) openSettingsInBrowser() tea.Cmd {
	if m.engine == nil {
		return m.postNotice(notice(chat.NoticeError, nil, "Assistant settings have no Datadog web site."))
	}
	target, err := browser.SettingsURL(m.engine.Site())
	if err != nil {
		return m.postNotice(notice(chat.NoticeError, err, "Could not build a web link for Assistant settings."))
	}
	return m.openBrowser(target, "Opened Assistant settings in your browser")
}

func (m *Model) openBrowser(target, success string) tea.Cmd {
	openURL := m.openURL
	if openURL == nil {
		openURL = browser.Open
	}
	epoch := m.conversationEpoch
	return func() tea.Msg {
		return browserOpenResultMsg{epoch: epoch, url: target, success: success, err: openURL(context.Background(), target)}
	}
}

func (m *Model) beginSelection(msg tea.MouseClickMsg) tea.Cmd {
	// A completion menu is an editor overlay, not part of the chat document.
	// Close it before taking the frame so the click selects the underlying rows.
	if m.editor.MenuOpen() {
		m.editor.CloseMenu()
	}
	scope := selectionScopeLower
	if m.frame.at(image.Pt(msg.X, msg.Y)) == regionTranscript {
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

type pendingLinkClick struct {
	hit     chat.LinkHit
	x, y    int
	epoch   uint64
	dragged bool
}

// webLink accepts destinations opened by Bits on a plain click. Other OSC 8
// links remain available to the terminal under its own link behavior.
func webLink(raw string) bool {
	if len(raw) == 0 || len(raw) > 8*1024 {
		return false
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return false
		}
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") &&
		u.Hostname() != "" && u.User == nil
}

func (m *Model) beginLinkClick(msg tea.MouseClickMsg) {
	m.pendingLink = nil
	if msg.Mod != 0 || m.frame.replaced || m.editor.MenuOpen() ||
		m.frame.at(image.Pt(msg.X, msg.Y)) != regionTranscript {
		return
	}
	hit, ok := m.list.LinkAt(msg.X, msg.Y)
	if ok && webLink(hit.URL) {
		m.pendingLink = &pendingLinkClick{hit: hit, x: msg.X, y: msg.Y, epoch: m.conversationEpoch}
	}
}

func (m *Model) moveLinkClick(msg tea.MouseMotionMsg) {
	if p := m.pendingLink; p != nil && (msg.X != p.x || msg.Y != p.y) {
		p.dragged = true
	}
}

func (m *Model) finishLinkClick(msg tea.MouseReleaseMsg) tea.Cmd {
	p := m.pendingLink
	m.pendingLink = nil
	wasSelecting := m.selection.selecting()
	hadSelection := m.selection.selected()
	cmd := m.finishSelection(msg)
	if p == nil || msg.Button != tea.MouseLeft || msg.Mod != 0 ||
		!wasSelecting || p.dragged || hadSelection || m.selection.selected() ||
		p.epoch != m.conversationEpoch || m.frame.replaced || m.editor.MenuOpen() ||
		m.frame.at(image.Pt(msg.X, msg.Y)) != regionTranscript {
		return cmd
	}
	hit, ok := m.list.LinkAt(msg.X, msg.Y)
	if !ok || hit != p.hit || !webLink(hit.URL) {
		return cmd
	}
	return tea.Batch(cmd, m.openBrowser(hit.URL, "Opened link in your browser"))
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

// handleMouse routes pointer events by position; chat focus only routes keys.
func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	switch m.focus() {
	case focusPicker:
		return m.updateConversationPicker(msg)
	case focusStatus:
		return m.updateStatus(msg)
	default:
	}
	if m.mode != ModeChat {
		// The permissions popup covers the chat; the pointer drives neither.
		m.pendingLink = nil
		return nil
	}
	switch msg := msg.(type) {
	case tea.MouseMotionMsg:
		m.list.SetPointer(msg.X, msg.Y)
		if p, ok := m.inComposer(msg); ok {
			m.editor.SetPointer(p.X, p.Y)
		} else {
			m.editor.SetPointer(-1, -1)
		}
		m.moveLinkClick(msg)
		return m.extendSelection(msg)
	case tea.MouseReleaseMsg:
		return m.finishLinkClick(msg)
	}
	if m.frame.at(pointAt(msg)) == regionPrompt {
		if cmd, used := m.updatePrompt(m.frame.inPrompt(msg)); used {
			return cmd
		}
	}
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.list.ScrollBy(-mouseWheelDelta)
		case tea.MouseWheelDown:
			m.list.ScrollBy(mouseWheelDelta)
		default:
		}
	case tea.MouseClickMsg:
		if p, ok := m.inComposer(msg); ok && msg.Button == tea.MouseLeft && m.editor.Click(p.X, p.Y) {
			return nil
		}
		if msg.Button == tea.MouseLeft {
			m.beginLinkClick(msg)
			// Defer the toggle until release so drag selection can take over.
			m.pendingAccordionToggle, m.hasPendingAccordionToggle = m.list.AccordionAt(msg.Y)
			return m.beginSelection(msg)
		}
	}
	return nil
}

// inComposer returns the editor-local cell of a pointer event over the
// composer while the editor owns input.
func (m *Model) inComposer(msg tea.MouseMsg) (image.Point, bool) {
	p := pointAt(msg)
	if m.focus() != focusEditor || m.frame.at(p) != regionComposer {
		return image.Point{}, false
	}
	return p.Sub(m.frame.composer.Min), true
}

// applyRunEvent folds an event of the operation's run, and ends the operation
// after the run's terminal event.
func (m *Model) applyRunEvent(ev agent.Event) tea.Cmd {
	cmd := m.applyEvent(ev)
	m.syncStatus()
	if ev.Kind == agent.EventState {
		m.syncTranscript()
	}
	if ev.Terminal() {
		cmd = tea.Batch(cmd, m.endOperation())
	}
	return cmd
}

// endOperation ends the engine operation after its run's terminal event, then
// runs what was queued behind it.
func (m *Model) endOperation() tea.Cmd {
	done := m.op
	m.op = operation{gen: done.gen}
	// The run published its final state before its terminal event.
	if m.chatPhase != chat.PhaseError {
		m.chatPhase = chat.PhaseIdle
	}
	if done.stop != stopAll && done.cancel != nil {
		done.cancel() // release the turn/restore context
	}
	m.clearAsks()

	m.syncStatus()
	switch {
	case done.then == thenLogout:
		return m.startLogout()
	case done.then == thenNewConversation:
		return m.startNewConversation()
	case done.kind == opRestore && done.stop != stopAll:
		return m.resumePendingTools()
	}
	return nil
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
		m.attach(msg.engine)
		if m.entitySearcher == nil {
			m.entitySearcher = msg.engine
		}
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
//
// Ctrl+O toggles tool blocks and Shift+PgUp/PgDn page chat from any chat focus.
// PgUp/PgDn go to the focused prompt or transcript.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" && m.selection.active() {
		m.selection.clear()
		return m, nil
	}
	if m.mode == ModeChat {
		switch msg.String() {
		case "ctrl+o":
			m.list.ToggleAllDisclosure()
			return m, nil
		case "shift+pgup":
			m.list.PageUp()
			return m, nil
		case "shift+pgdown":
			m.list.PageDown()
			return m, nil
		}
	}
	switch m.focus() {
	case focusPicker:
		return m, m.updateConversationPicker(msg)
	case focusPrompt:
		return m, m.handlePromptKey(msg)
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

	// The offers are only visible with an empty composer, so these keys have no
	// competing meaning: submit() already no-ops on empty input.
	if m.showUpgrade() {
		switch msg.String() {
		case "up":
			m.moveUpgrade(-1)
			return m, nil
		case "down":
			m.moveUpgrade(1)
			return m, nil
		case "enter":
			return m, m.chooseUpgrade()
		}
	}
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
		if m.editor.ChipsFocused() {
			return m, m.editor.Update(msg)
		}
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

// handlePromptKey gives a key to the shown prompt; the inert editor never
// sees it. ctrl+x stops the tool round the prompt belongs to.
func (m *Model) handlePromptKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+x" {
		m.stopTools()
		return nil
	}
	cmd, _ := m.updatePrompt(msg)
	return cmd
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

	var skillInvocation *agent.SkillInvocation
	if name, arguments, ok := parseClientSkillInvocation(raw); ok {
		skillInvocation = &agent.SkillInvocation{Name: name, Arguments: arguments}
		text = raw
	} else if name, argument, ok := parseCommand(raw); ok {
		m.editor.Reset()
		m.stopEntitySearch()
		return m.dispatchCommand(name, argument)
	}
	if m.op.busy() {
		return m, nil
	}
	// Once the menu has loaded, an unknown name keeps the draft for correction.
	// Otherwise the engine resolves the name and fails the turn if it is unknown.
	if skillInvocation != nil && m.skillMenu.skills != nil {
		if _, known := m.skillMenu.skills[skillInvocation.Name]; !known {
			return m, m.postNotice(notice(chat.NoticeWarn, nil, "Unknown client skill: %s", escape.Inline(skillInvocation.Name)))
		}
	}
	turnContext := contextFromAttachments(attachments)
	m.editor.Reset()
	closeFileSearch := m.stopCompletionSearches()

	ctx, cancel := context.WithCancel(context.Background())
	run, err := m.engine.StartTurn(ctx, agent.TurnInput{
		Message: text,
		Skill:   skillInvocation,
		Tools:   m.tools,
		Context: turnContext,
		OnDeny:  agent.DenyContinue,
	})
	m.chatPhase = chat.PhaseWaiting
	failed := m.begin(opTurn, run, err, cancel)
	// Submitting always jumps to the tail and re-engages auto-follow, so the
	// user sees their message and the incoming reply even if they had scrolled up.
	m.list.ScrollToBottom()
	return m, tea.Batch(closeFileSearch, failed)
}

// begin starts an exclusive operation, streaming the engine run if any. Its
// generation drops any late message from an earlier one. An engine operation
// that failed to start ends at once with its error.
func (m *Model) begin(kind opKind, run agent.RunID, err error, cancel context.CancelFunc) tea.Cmd {
	m.op = operation{kind: kind, gen: m.op.gen + 1, run: run, cancel: cancel}
	if err != nil {
		return m.applyRunEvent(agent.Event{Kind: agent.EventError, Err: err})
	}
	return nil
}

func (m *Model) resumePendingTools() tea.Cmd {
	if m.engine == nil {
		return nil
	}
	m.engine.SettleRestoredTools(m.tools)
	m.state = m.engine.State() // the event that publishes it follows
	m.syncTranscript()
	if m.tools == nil || !m.engine.CanResumeTools(m.tools) {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	run, err := m.engine.ResumePendingTools(ctx, agent.TurnInput{Tools: m.tools, OnDeny: agent.DenyContinue})
	m.chatPhase = chat.PhaseWaiting
	m.list.ScrollToBottom()
	return m.begin(opTurn, run, err, cancel)
}

// cancelOperation cancels a running engine operation once and drops its tool
// UIs; its approvals go when the operation closes. Logout is never
// interrupted this way.
func (m *Model) cancelOperation() {
	if m.op.run == 0 || m.op.stop == stopAll {
		return
	}
	m.op.stop = stopAll
	m.op.cancel()
	m.dropToolUIs()
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
	case agent.EventState:
		if ev.State.Version > m.state.Version {
			m.state = ev.State
		}
		m.syncApprovals(ev.State.PendingApprovals())
		if ev.State.HasStreamingContent() {
			m.chatPhase = chat.PhaseStreaming
		}
	case agent.EventUsage, agent.EventConversation:
		// The state carries both; these report a backend send.
	case agent.EventDone:
		m.chatPhase = chat.PhaseIdle
	case agent.EventCanceled:
		// endOperation settles the chat phase and resyncs the transcript.
	case agent.EventError:
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

// selectionCellStyle is the style painted over selected cells. A terminal that
// cannot paint color keeps attributes but loses Fg and Bg, which would leave a
// selection indistinguishable, so those fall back to reverse video.
func (m *Model) selectionCellStyle() uv.Style {
	if m.monochrome {
		return uv.Style{Attrs: uv.AttrReverse}
	}
	return uv.Style{Fg: m.styles.Selection.Foreground, Bg: m.styles.Selection.Background}
}

// setDarkBackground adapts styles to the detected terminal background.
func (m *Model) setDarkBackground(isDark bool) {
	if isDark == m.styles.IsDark {
		return
	}
	m.applyStyles(m.theme(isDark))
}

func (m *Model) resize(w, h int) {
	m.selection.clear()
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

// relayout rebuilds the frame and sizes the transcript and editor after each update.
func (m *Model) relayout() {
	if m.mode == ModeTermInit {
		return
	}
	m.reshow()
	m.editor.SetPlaceholder(m.promptPlaceholder())
	m.frame = m.layout()
	m.editor.SetMenuHeight(m.frame.inputTop())
	m.list.SetHeight(m.frame.transcript.Dy())
	m.list.SetHeader(m.headerView())
}

// syncTranscript rebuilds presentation metadata after m.state changes.
func (m *Model) syncTranscript() {
	m.list.SetTranscript(m.state.Blocks, m.state.Tasks, m.notices)
}
