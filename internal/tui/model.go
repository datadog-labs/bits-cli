// Package tui is the Bubble Tea front end: it owns UI state, runs the turn-event
// pump, and maps agent events onto the transcript store and renderer.
package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/browser"
	"github.com/datadog-labs/bits-cli/internal/tools"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/components"
	conversationview "github.com/datadog-labs/bits-cli/internal/tui/conversations"
	"github.com/datadog-labs/bits-cli/internal/tui/editor"
	"github.com/datadog-labs/bits-cli/internal/tui/graphics"
	loginui "github.com/datadog-labs/bits-cli/internal/tui/login"
	statusview "github.com/datadog-labs/bits-cli/internal/tui/status"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
	"github.com/datadog-labs/bits-cli/internal/update"
	"github.com/datadog-labs/bits-cli/internal/viz"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

// Mode is the top-level screen the model shows.
type Mode int

const (
	ModeTermInit Mode = iota
	ModeChat
	ModeLogin
	ModeConversations
	ModeStatus
	ModePermissions
)

// EngineFactory constructs the authenticated chat engine after startup login
// has persisted a session.
type EngineFactory func(context.Context) (*agent.Engine, error)

// LogoutFunc removes the durable OAuth session and best-effort revokes its
// grant. It matches auth.Logout without coupling the TUI to a concrete store.
type LogoutFunc func(context.Context) (hadSession bool, revokeErr, err error)

// EntitySearcher is the narrow autocomplete dependency used by the TUI.
type EntitySearcher interface {
	SearchEntities(context.Context, assistant.SearchEntitiesInput) (assistant.SearchEntitiesResponse, error)
}

type Config struct {
	// Visualization renders widgets; nil leaves them as links to the web app.
	Visualization  viz.Client
	Tools          *agent.ToolSet
	ToolUI         *tools.UI
	Version        string
	Workspace      *workspace.Workspace
	EntitySearcher EntitySearcher
	OpenURL        func(context.Context, string) error
	Logout         LogoutFunc
	Upgrade        *update.Offer // nil when this installation cannot update itself
}

// Model is the root Bubble Tea model. All state lives here and is mutated only
// on the tea thread; engine and entity-search work return typed messages.
type Model struct {
	backgroundRuns map[agent.RunID]context.CancelFunc
	// Collaborators the model drives.
	engine                 *agent.Engine
	events                 <-chan agent.Event // the engine subscription, pumped for the model's lifetime
	unsubscribe            func()
	tools                  *agent.ToolSet
	openURL                func(context.Context, string) error
	editor                 *editor.Editor
	quitConfirm            quitConfirm
	picker                 *conversationview.Model
	status                 statusview.Model
	workspace              *workspace.Workspace
	workspaceDisplayPath   string
	fileSearchSession      *workspace.FileSearchSession
	fileSearchQuery        string
	fileSearchGeneration   uint64
	fileSearchWaiting      bool
	entitySearcher         EntitySearcher
	searchSessionID        string
	entitySearchQuery      string
	entitySearchActive     bool
	entitySearchTask       task
	entitySearchCache      map[string]entitySearchCacheEntry
	entitySearchCacheOrder []string

	skillMenu skillMenu

	statusTask     task
	statusIdentity string

	// Startup login stays inside this root model so Bubble Tea owns the
	// alternate screen continuously while switching from login to chat.
	startupCtx      context.Context
	startupTask     task
	startupStopping bool
	loginModel      *loginui.Model
	engineFactory   EngineFactory
	startupErr      error

	// state is the conversation's latest state, as the engine published it.
	state agent.State

	// op is the one exclusive session operation; see operation.
	op operation

	// /logout stops active work before deleting credentials, then discards the
	// authenticated engine.
	logout    LogoutFunc
	loggedOut bool

	// Tool calls waiting on the user, by source, and the one shown (see ask).
	toolUI    *tools.UI
	approvals map[string]*approvalAsk // mirrors the transcript, by call ID
	toolUIs   []*toolAsk              // requests from the tools.UI channel
	shown     ask
	// The prompt a slash command opened, shown when no ask is.
	commandPrompt *commandPrompt

	permissionsPanel *components.Panel
	// The picker shows the options, or the full-access confirmation; the
	// cursor is the row on the page shown (on the confirmation, 0 is Yes).
	permissionConfirm bool
	permissionCursor  int

	upgrade upgradePrompt

	// Top-level screen; transitions go through setMode.
	mode Mode

	// /resume operations are cancellable and generation-stamped. A late result
	// from a cancelled list/load can never mutate the current conversation.
	conversationTask     task
	conversationSwitchID string

	// Turn status, surfaced in the editor and local status view.
	chatPhase chat.Phase
	// Increments when the visible conversation changes; delayed local results
	// from an earlier conversation must not enter the new transcript.
	conversationEpoch uint64
	// connectivity is the last observed remote outcome. Active phases override
	// it with connecting/connected when building the status snapshot.
	connectivity          statusview.Connectivity
	authStateOverride     string
	authFailureGeneration uint64

	// Session-only UI messages are merged with the agent snapshot for display.
	notices      []chat.NoticeItem
	nextNoticeID uint64

	// Transcript rendering. list owns the transcript's scroll position and
	// per-item render cache.
	list       *chat.List
	chatStyles chat.Styles
	styles     styles.Theme // terminal styles; dark until detected
	monochrome bool         // terminal cannot paint color; set from ColorProfileMsg

	// One repaint clock samples independent elapsed-time timelines. Activity
	// covers chat spinners and picker loading; the second timeline drives the
	// composer border sweep.
	animClock       animationClock
	animActivity    animationTimeline
	animBorderSweep animationTimeline

	// frame is the chat geometry derived at the end of the last Update.
	frame frame

	// Terminal dimensions are cached so a chat installed after startup login can
	// be laid out immediately; Bubble Tea does not replay its initial size event.
	width  int
	height int

	// Selection belongs to ModeChat; selection.go encapsulates its gesture and
	// auto-scroll state while the model supplies rendered pane frames.
	selection selection
	follow    followControl
	exitCard  exitCard

	// pendingAccordionToggle is the block a mouse-down landed on inside an
	// accordion zone: any row an expandable block renders, collapsed preview
	// rows included. Every left-click also begins a potential drag-select (see beginSelection), so the toggle stays pending until
	// finishSelection sees the gesture through: it fires only if the release
	// never turned it into a real selection range.
	pendingAccordionToggle    agent.BlockID
	hasPendingAccordionToggle bool
	pendingLink               *pendingLinkClick

	// pointerIsHand is the OS pointer shape last written to the terminal, so
	// reconcilePointerShape only emits an OSC 22 sequence on a real change.
	pointerIsHand bool
	terminal      terminalStatus

	// chatMouseMode is the chat surface's mouse tracking, resolved once from
	// the environment (see chatMouseMode()).
	chatMouseMode tea.MouseMode

	widgets widgets

	// graphics is whether the terminal paints Kitty images, from its answer to
	// the probe and any image it rejected.
	graphics graphics.Detection

	// resume is the startup conversation offer shown under the splash panel.
	resume resume

	// version is injected so the TUI stays independent of the command layer.
	version string
}

// New builds the root model for the given engine. When the engine is bound to a
// conversation, the model shows its id immediately and restores its history on
// Init.
func New(engine *agent.Engine, configs ...Config) *Model {
	m := newShell()
	m.attach(engine)
	m.configure(configs)
	return m
}

// attach binds the model to its engine and subscribes to the engine's events
// before any operation starts, so the pump started by initChat misses none.
func (m *Model) attach(engine *agent.Engine) {
	m.engine = engine
	m.widgets.origin = engine.WebOrigin()
	m.state = engine.State()
	m.events, m.unsubscribe = engine.Subscribe()
}

// Detach ends the model's engine subscription once the program has stopped.
// Closing the engine is not enough: its subscriptions deliver their queued
// events first, and nothing reads them anymore.
func (m *Model) Detach() {
	m.op.cancelOnce()
	if closeFiles := m.stopBackgroundTasks(); closeFiles != nil {
		_ = closeFiles()
	}
	m.widgets.close()
	m.clearBackgroundRuns()
	if m.unsubscribe != nil {
		m.unsubscribe()
	}
}

// NewWithLogin creates the same root TUI in login mode. On successful OAuth,
// factory builds the engine and this model transitions to chat without ending
// the Bubble Tea program or leaving the alternate screen.
func NewWithLogin(ctx context.Context, loginModel *loginui.Model, factory EngineFactory, configs ...Config) *Model {
	if ctx == nil {
		ctx = context.Background()
	}
	m := newShell()
	m.mode = ModeLogin
	m.startupCtx = ctx
	m.loginModel = loginModel
	m.engineFactory = factory
	m.configure(configs)
	return m
}

func (m *Model) configure(configs []Config) {
	if len(configs) > 0 {
		m.widgets.client = configs[0].Visualization
		m.tools = configs[0].Tools
		m.toolUI = configs[0].ToolUI
		m.workspace = configs[0].Workspace
		if m.workspace != nil {
			m.workspaceDisplayPath = m.workspace.DisplayPath()
		}
		m.entitySearcher = configs[0].EntitySearcher
		m.openURL = configs[0].OpenURL
		m.logout = configs[0].Logout
		m.version = configs[0].Version
		m.upgrade = newUpgradePrompt(configs[0].Upgrade)
	}
	if m.entitySearcher == nil && m.engine != nil {
		m.entitySearcher = m.engine
	}
	if m.openURL == nil {
		m.openURL = browser.Open
	}
}

func newShell() *Model {
	theme := styles.Default(true)
	m := &Model{
		terminal:          terminalStatus{focused: true},
		editor:            editor.New(),
		list:              chat.NewList(),
		animActivity:      newAnimationTimeline(activityAnimInterval),
		animBorderSweep:   newAnimationTimeline(borderSweepInterval),
		status:            statusview.New(1, 1, theme),
		permissionsPanel:  components.NewPanel(theme.Permissions),
		styles:            theme,
		searchSessionID:   newSearchSessionID(),
		entitySearchCache: make(map[string]entitySearchCacheEntry),
		approvals:         make(map[string]*approvalAsk),
		resume:            resume{now: time.Now},
		chatMouseMode:     chatMouseMode(),
	}
	m.editor.SetHistorySource(func() []editor.HistoryEntry { return historyEntries(m.state.UserPrompts()) })
	m.editor.SetQueueRemover(func(id uint64) {
		if m.engine != nil {
			_ = m.engine.Unqueue(agent.QueueID(id)) // ErrStale: already sent
			m.state = m.engine.State()              // the event that publishes it follows
		}
	})
	m.editor.SetCommands(commandCompletionSpecs())
	m.applyStyles(m.styles)
	return m
}

// ConversationID returns the active conversation id, or "" when none has been
// established yet. main reads it after the program exits to print a resume hint.
func (m *Model) ConversationID() string { return m.state.ConversationID }

// LoggedOut show confirmation message after /logout is used
func (m *Model) LoggedOut() bool { return m.loggedOut }

// StartupError reports why login could not transition into chat. Cancellation
// remains distinguishable from post-login client construction failures.
func (m *Model) StartupError() error {
	// The login model is kept until the handoff to chat, which a cancel prevents.
	if m.loginModel != nil && m.loginModel.Canceled() {
		return loginui.ErrCanceled
	}
	if m.startupErr != nil {
		return m.startupErr
	}
	return nil
}

// applyStyles propagates one complete theme to every component that copies
// style values. Keep this as the single fan-out point for theme changes.
func (m *Model) applyStyles(theme styles.Theme) {
	m.styles = theme
	m.chatStyles = chat.StylesFor(theme)
	m.list.SetStyles(m.chatStyles)
	m.editor.SetInputStyles(theme.Input)
	m.editor.SetStyles(theme.Editor)
	for _, a := range m.approvals {
		a.panel.SetStyles(theme)
	}
	for _, t := range m.toolUIs {
		t.form.SetStyles(theme)
	}
	if m.commandPrompt != nil {
		m.commandPrompt.form.SetStyles(theme)
	}
	m.permissionsPanel.SetStyles(theme.Permissions)
	if m.picker != nil {
		m.picker.SetStyles(theme)
	}
	m.status.SetStyles(theme)
}

// setMode switches the top-level screen. It is the single entry point for mode
// changes; Update relayouts after every handler, so a startup login can hand
// control to chat without replacing or quitting the root model.
func (m *Model) setMode(mode Mode) {
	if m.mode == ModeChat && mode != ModeChat {
		m.selection.clear()
		m.follow = followControl{}
		m.closeExitCard()
	}
	m.mode = mode
}

// Init starts the active screen. Login owns initial color detection; a direct
// chat startup and the later login-to-chat handoff both use initChat.
func (m *Model) Init() tea.Cmd {
	if m.mode == ModeLogin {
		if m.loginModel == nil {
			m.startupErr = errors.New("startup login is unavailable")
			return tea.Quit
		}
		// Probe during login so the logo is ready at the handoff to chat.
		return tea.Batch(m.loginModel.Init(), m.queryGraphics())
	}
	return tea.Batch(m.initChat(), m.queryGraphics())
}

// queryGraphics probes the terminal. One that must not be asked is known
// unsupported at once, so Unknown always means an answer is on its way.
func (m *Model) queryGraphics() tea.Cmd {
	return m.graphics.Query()
}

// initChat focuses the editor, starts the engine event pump and, when
// restoring a conversation, the history restore. It is called explicitly on
// login handoff because Bubble Tea calls Init only on the original model.
func (m *Model) initChat() tea.Cmd {
	requestBG := func() tea.Msg { return tea.RequestBackgroundColor() }
	commands := []tea.Cmd{m.editor.Focus(), requestBG, waitToolUI(m.toolUI)}
	if m.engine == nil {
		return tea.Batch(commands...)
	}
	commands = append(commands, waitEvent(m.events))
	if m.engine.State().ConversationID == "" {
		// Fetch the startup offer only when there is no history to restore.
		return tea.Batch(append(commands, m.syncResume(), m.syncClientSkills())...)
	}
	m.chatPhase = chat.PhaseLoading
	ctx, cancel := context.WithTimeout(context.Background(), historyLoadTimeout)
	if m.begin(opRestore, cancel) {
		run, err := m.engine.Restore(ctx)
		commands = append(commands, m.watchRun(run, err))
	}
	return tea.Batch(commands...)
}

// clearBackgroundRuns releases the old conversation's streams and prompts.
func (m *Model) clearBackgroundRuns() {
	for _, cancel := range m.backgroundRuns {
		cancel()
	}
	clear(m.backgroundRuns)
	m.clearAsks()
}
