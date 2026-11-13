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
	loginui "github.com/datadog-labs/bits-cli/internal/tui/login"
	"github.com/datadog-labs/bits-cli/internal/tui/splash"
	"github.com/datadog-labs/bits-cli/internal/viz"
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
	focusExitCard                 // the "Exit Bits?" confirmation card
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
		if m.mode == ModeChat && m.exitCard.open {
			return focusExitCard
		}
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
	if m.mode == ModeChat && !m.frame.tooSmall && m.focus() != focusExitCard {
		hand = m.list.Hovered() || m.follow.hover || m.editor.QueueHovered() || (m.focus() == focusEditor && m.editor.ChipHovered())
		if !hand && !m.frame.replaced && !m.editor.MenuOpen() {
			if hit, ok := m.list.HoveredLink(); ok && m.clickableLink(hit) {
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
	m.updateTerminal(msg)
	if cmd, ok := m.widgets.images.Handle(msg); ok {
		return m, cmd
	}
	// ctrl+c wins over every chat surface and skips reconciliation (we are
	// tearing down). Login handles its own ctrl+c.
	if key, ok := msg.(tea.KeyPressMsg); ok && m.mode != ModeLogin {
		if key.String() != "ctrl+c" {
			m.quitConfirm.armed = false
		} else if (m.mode != ModeChat && m.mode != ModeTermInit) || m.exitCard.open || (m.quitConfirm.armed && !m.composerHasDraft()) {
			return m.quit()
		}
	}

	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.FocusMsg, tea.BlurMsg, terminalTickMsg:
		// Terminal events are handled above, including during login.
	case viz.Completion:
		if page := m.widgets.apply(msg); page != "" {
			cmd = m.openBrowser(page, "")
		}
	case uv.PrimaryDeviceAttributesEvent:
		if m.graphics.ObserveDeviceAttributes() && !m.widgets.closed {
			cmd = m.widgets.images.Enqueue(splash.Transmit())
		}
	case tea.TerminalVersionMsg:
		m.graphics.ObserveTerminalVersion(msg.Name)
	case uv.KittyGraphicsEvent:
		// Replies can arrive in any mode, login included.
		m.graphics.Observe(msg, splash.ImageID)
	case tea.ColorProfileMsg:
		// Arrives in any mode. Selection is the only paint that needs it: when
		// colors are off, ultraviolet drops Fg and Bg but keeps attributes.
		m.monochrome = msg.Profile == colorprofile.NoTTY || msg.Profile == colorprofile.ASCII
		m.graphics.ObserveColorProfile(msg.Profile)
	case quitConfirmExpiredMsg:
		m.expireQuitConfirm(msg)
	case recentConversationsMsg:
		m.applyRecentConversations(msg)
	default:
		switch {
		case m.mode == ModeLogin:
			cmd = m.updateLogin(msg)
		case isCtrlC(msg):
			cmd = m.clearDraftOrArmQuit()
		case m.frame.tooSmall && isUserInput(msg):
			// Only a resize hint is visible, so input must not drive a hidden
			// surface. System and engine messages still flow.
		case m.mode == ModeChat && m.exitCard.open && isUserInput(msg):
			cmd = m.updateExitCard(msg)
		default:
			_, cmd = m.dispatch(msg)
		}
	}
	if m.mode == ModeLogin {
		return m, tea.Batch(cmd, m.syncTerminalStatus())
	}

	loadSkillMenu, loadOffer := m.syncClientSkills(), m.syncResume()
	m.relayout()
	return m, tea.Batch(cmd, loadSkillMenu, loadOffer, m.syncWidgets(), m.syncAnimations(), m.reconcileFocus(), m.reconcilePointerShape(), m.syncTerminalStatus())
}

func isCtrlC(msg tea.Msg) bool {
	key, ok := msg.(tea.KeyPressMsg)
	return ok && key.String() == "ctrl+c"
}

func isUserInput(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg, tea.PasteMsg:
		return true
	}
	return false
}

func (m *Model) quit() (tea.Model, tea.Cmd) {
	for _, cancel := range m.backgroundRuns {
		cancel()
	}
	// /resume may own a live list/history request that must be cancelled before
	// the application exits.
	if m.focus() == focusPicker {
		m.closeConversationPicker()
	}
	m.op.cancelOnce()
	closeFileSearch := m.stopBackgroundTasks()
	if closeFileSearch == nil {
		return m, m.quitWith(tea.Quit)
	}
	return m, m.quitWith(func() tea.Msg {
		_ = closeFileSearch()
		return tea.Quit()
	})
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
		case (m.op.run != 0 && msg.ev.Run == m.op.run) || m.backgroundRuns[msg.ev.Run] != nil:
			cmd = m.applyRunEvent(msg.ev)
		}
		return m, tea.Batch(cmd, waitEvent(m.events))

	case logoutResultMsg:
		return m.applyLogoutResult(msg)

	case conversationListResultMsg:
		return m, m.applyConversationListResult(msg)

	case renameResultMsg:
		return m, m.applyRenameResult(msg)

	case forkResultMsg:
		return m, m.applyForkResult(msg)

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

	case currentUserMsg:
		m.currentUser = msg.user
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
			if msg.success == "" {
				// A widget's URL carries the whole widget: too long to show.
				return m, m.postNotice(notice(chat.NoticeError, msg.err, "Could not open a browser for this widget."))
			}
			return m, m.postNotice(notice(chat.NoticeError, msg.err, "Could not open a browser. Open this URL: %s", msg.url))
		}
		if msg.success == "" {
			return m, nil
		}
		return m, m.postNotice(notice(chat.NoticeInfo, nil, "%s: %s", msg.success, msg.url))
	}

	if f := m.focus(); f == focusPrompt || (f == focusExitCard && m.prompt() != nil) {
		// Paste, cursor blink, and the like belong to the prompt, never to the
		// inert editor below it, even while the exit card covers the prompt.
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
	target, err := browser.ConversationURL(m.engine.WebOrigin(), m.state.ConversationID)
	if err != nil {
		return m.postNotice(notice(chat.NoticeError, err, "Could not build a web link for this conversation."))
	}
	return m.openBrowser(target, "Opened this conversation in your browser")
}

func (m *Model) openSettingsInBrowser() tea.Cmd {
	if m.engine == nil {
		return m.postNotice(notice(chat.NoticeError, nil, "Assistant settings have no Datadog web site."))
	}
	target, err := browser.SettingsURL(m.engine.WebOrigin())
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

// clickableLink reports whether a transcript link reacts to clicks: a live
// widget action or a web page.
func (m *Model) clickableLink(hit chat.LinkHit) bool {
	return hit.Action != chat.WidgetNone || webLink(hit.URL)
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
	if ok && m.clickableLink(hit) {
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
	if !ok || hit != p.hit || !m.clickableLink(hit) {
		return cmd
	}
	if hit.Action != chat.WidgetNone {
		action, page := m.widgets.click(hit)
		if page != "" {
			// The URL carries the whole widget: too long to echo as a notice.
			action = m.openBrowser(page, "")
		}
		return tea.Batch(cmd, action)
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
		if p := pointAt(msg); p.In(m.frame.queue) {
			p = p.Sub(m.frame.queue.Min)
			m.editor.SetQueuePointer(p.X, p.Y)
		} else {
			m.editor.SetQueuePointer(-1, -1)
		}
		m.moveLinkClick(msg)
		return m.extendSelection(msg)
	case tea.MouseReleaseMsg:
		return m.finishLinkClick(msg)
	}
	if m.frame.at(pointAt(msg)) == regionQueue {
		p := pointAt(msg).Sub(m.frame.queue.Min)
		switch msg := msg.(type) {
		case tea.MouseClickMsg:
			if msg.Button == tea.MouseLeft && m.editor.ClickQueue(p.X, p.Y) {
				return nil
			}
		case tea.MouseWheelMsg:
			if msg.Button == tea.MouseWheelUp {
				m.editor.ScrollQueue(-1)
			}
			if msg.Button == tea.MouseWheelDown {
				m.editor.ScrollQueue(1)
			}
			return nil
		}
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

// applyRunEvent folds an admitted foreground or background event. Only the
// foreground run may release the operation and start its queued work.
func (m *Model) applyRunEvent(ev agent.Event) tea.Cmd {
	foreground := ev.Run == m.op.run
	version := m.state.Version
	cmd := m.applyEvent(ev, foreground)
	if foreground {
		cmd = tea.Batch(m.observeTerminalOutcome(ev), cmd)
	}
	if m.state.Version != version {
		m.syncTranscript()
	}
	m.syncStatus()
	if foreground {
		if ev.Kind == agent.EventBackground {
			if m.backgroundRuns == nil {
				m.backgroundRuns = make(map[agent.RunID]context.CancelFunc)
			}
			m.backgroundRuns[ev.Run] = m.op.cancel
			m.op.cancel = nil
			cmd = tea.Batch(cmd, m.endOperation())
		} else if ev.Terminal() {
			cmd = tea.Batch(cmd, m.endOperation())
		}
	} else if ev.Terminal() {
		m.backgroundRuns[ev.Run]()
		delete(m.backgroundRuns, ev.Run)
	}
	return cmd
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

	if m.editor.ControlKey(msg.String()) {
		return m, nil
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
		if m.mode == ModeChat && !m.cancellable() && !m.op.loggingOut() && len(m.backgroundRuns) == 0 {
			m.openExitCard()
			return m, nil
		}
		if m.op.busy() {
			m.cancelOperation()
		} else {
			for _, cancel := range m.backgroundRuns {
				cancel()
			}
		}
		return m, nil
	case "enter":
		return m.submit()
	case "ctrl+d":
		// Shell convention: ctrl+d on an empty prompt is EOF and quits. With
		// any text or attachment it falls through to the editor, which deletes
		// the character under the cursor. While an operation runs it is ignored,
		// so a stray press cannot drop a streaming turn: esc interrupts it and
		// a confirmed ctrl+c still quits.
		if m.editor.Value() == "" && len(m.editor.Attachments()) == 0 {
			if m.op.busy() {
				return m, nil
			}
			return m.quit()
		}
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
// sees it. ctrl+x stops the tool round the prompt belongs to, or dismisses a
// command prompt.
func (m *Model) handlePromptKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+x" {
		if m.shown == nil {
			m.commandPrompt = nil // a command prompt has no tools to stop
		} else {
			m.stopTools()
		}
		return nil
	}
	cmd, _ := m.updatePrompt(msg)
	return cmd
}

func (m *Model) resumePendingTools() tea.Cmd {
	if m.engine == nil || m.op.busy() {
		return nil
	}
	m.engine.SettleRestoredTools(m.tools)
	m.state = m.engine.State() // the event that publishes it follows
	m.syncTranscript()
	if m.tools == nil || !m.engine.CanResumeTools(m.tools) {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	if !m.begin(opTurn, cancel) {
		return nil
	}
	run, err := m.engine.ResumePendingTools(ctx, agent.TurnInput{Tools: m.tools, OnDeny: agent.DenyContinue})
	m.chatPhase = chat.PhaseWaiting
	m.list.ScrollToBottom()
	return m.watchRun(run, err)
}

// applyEvent folds one engine event into the block snapshot / status. The switch
// is exhaustive over agent.EventKind. It returns a command for side effects (an
// error posts a local transcript message); nil otherwise.
func (m *Model) applyEvent(ev agent.Event, foreground bool) tea.Cmd {
	m.observeEvent(ev)
	switch ev.Kind {
	case agent.EventState:
		if ev.State.Version <= m.state.Version {
			return nil
		}
		m.state = ev.State
		m.syncApprovals(m.state.PendingApprovals())
		if foreground && ev.State.HasStreamingContent(ev.Run) {
			m.chatPhase = chat.PhaseStreaming
		}
	case agent.EventUsage, agent.EventConversation:
		// The state carries both; these report a backend send.
	case agent.EventDone, agent.EventBackground:
		if foreground {
			m.op.completed = true
			m.chatPhase = chat.PhaseIdle
		}
	case agent.EventCanceled:
		// endOperation settles the chat phase and resyncs the transcript.
	case agent.EventError:
		// A failure during restore is benign: drop to idle with a notice so the
		// user can still type. A failure mid-turn is the turn's error state.
		if foreground && m.op.kind == opRestore {
			m.chatPhase = chat.PhaseIdle
			if ev.Err != nil {
				return m.postNotice(noticeForError("restore failed", ev.Err))
			}
		} else {
			if foreground {
				m.chatPhase = chat.PhaseError
			}
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
	m.syncQueue()
	m.editor.SetPlaceholder(m.promptPlaceholder())
	previousQueue := m.frame.queue
	m.frame = m.layout()
	if m.frame.queue != previousQueue {
		m.editor.SetQueuePointer(-1, -1)
		m.frame = m.layout()
	}
	m.editor.SetMenuHeight(m.frame.inputTop())
	m.list.SetHeight(m.frame.transcript.Dy())
	m.list.SetHeader(m.headerView())
}

// syncTranscript rebuilds presentation metadata after m.state changes.
func (m *Model) syncTranscript() {
	m.list.SetTranscript(m.state.Blocks, m.state.Tasks, m.transcriptNotices())
}
