// Package tui is the Bubble Tea front end: it owns UI state, runs the turn-event
// pump, and maps agent events onto the transcript store and renderer.
package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/browser"
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/components"
	conversationview "github.com/DataDog/bits-cli/internal/tui/conversations"
	"github.com/DataDog/bits-cli/internal/tui/editor"
	loginui "github.com/DataDog/bits-cli/internal/tui/login"
	"github.com/DataDog/bits-cli/internal/tui/splash"
	statusview "github.com/DataDog/bits-cli/internal/tui/status"
	"github.com/DataDog/bits-cli/internal/tui/styles"
	"github.com/DataDog/bits-cli/internal/workspace"
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

// operation is the one exclusive session operation: a turn (resumed client
// tools included), a history restore, or logout. Anything that asks "can I
// start something?" or "what happens when it ends?" reads it; chatPhase only
// drives what the chat displays.
type operation struct {
	kind   opKind
	gen    uint64             // stamps the operation's messages; never reset, so stale ones drop
	events <-chan agent.Event // engine operations only
	cancel context.CancelFunc
	stop   stopLevel
	then   followUp
}

type opKind uint8

const (
	opIdle    opKind = iota
	opTurn           // a user turn or resumed client tools
	opRestore        // conversation history loading at startup
	opLogout         // credential deletion; the session quits once it succeeds
)

// stopLevel only rises within an operation: stopping its tools never undoes a
// full cancel.
type stopLevel uint8

const (
	stopNone  stopLevel = iota
	stopTools           // the engine stops client tools; the turn continues
	stopAll             // the operation's context is cancelled
)

// followUp runs once the operation ends. It only rises, so a queued logout
// wins over a queued new conversation.
type followUp uint8

const (
	thenNothing followUp = iota
	thenNewConversation
	thenLogout
)

func (o operation) busy() bool { return o.kind != opIdle }

func (o operation) loggingOut() bool { return o.kind == opLogout || o.then == thenLogout }

// accepts reports whether an engine message belongs to the running operation.
func (o operation) accepts(generation uint64) bool {
	return o.events != nil && generation == o.gen
}

// task is one cancellable request whose results carry its generation.
// Starting or stopping advances the generation, so a result from an earlier
// request drops.
type task struct {
	gen    uint64
	cancel context.CancelFunc
}

// start stops any running request and returns the next one's context and
// generation. A zero timeout means no deadline.
func (t *task) start(parent context.Context, timeout time.Duration) (context.Context, uint64) {
	t.stop()
	var ctx context.Context
	if timeout > 0 {
		ctx, t.cancel = context.WithTimeout(parent, timeout)
	} else {
		ctx, t.cancel = context.WithCancel(parent)
	}
	return ctx, t.gen
}

// stop cancels the running request and invalidates its results.
func (t *task) stop() {
	t.gen++
	t.done()
}

// done releases a finished request's context; its results stay current.
func (t *task) done() {
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
}

func (t *task) running() bool { return t.cancel != nil }

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
	Tools          *agent.ToolSet
	ToolUI         *tools.UI
	Version        string
	Workspace      *workspace.Workspace
	EntitySearcher EntitySearcher
	OpenURL        func(context.Context, string) error
	Logout         LogoutFunc
}

// Model is the root Bubble Tea model. All state lives here and is mutated only
// on the tea thread; engine and entity-search work return typed messages.
type Model struct {
	// Collaborators the model drives.
	engine                 *agent.Engine
	tools                  *agent.ToolSet
	openURL                func(context.Context, string) error
	editor                 *editor.Editor
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

	localSkills       map[string]agent.LocalSkill
	localSkillsTask   task
	localSkillsEngine *agent.Engine
	localSkillsEpoch  uint64

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

	// transcript is the latest snapshot of the engine's aggregated transcript.
	transcript agent.TranscriptSnapshot

	// op is the one exclusive session operation; see operation.
	op operation

	// /logout stops active work before deleting credentials, then discards the
	// authenticated engine.
	logout    LogoutFunc
	loggedOut bool

	toolUI           *tools.UI
	activeToolUI     *toolUISession
	queuedToolUIs    []*toolUISession
	pendingApprovals []agent.Block
	approvalChoice   int
	approvalPanel    *components.Panel
	permissionsPanel *components.Panel
	// The picker shows the options, or the full-access confirmation; the
	// cursor is the row on the page shown (on the confirmation, 0 is Yes).
	permissionConfirm bool
	permissionCursor  int
	// A mode selected during an active turn applies when that turn closes.
	pendingPermissions agent.PermissionsMode

	// Top-level screen; transitions go through setMode.
	mode Mode

	// /resume operations are cancellable and generation-stamped. A late result
	// from a cancelled list/load can never mutate the current conversation.
	conversationTask     task
	conversationSwitchID string

	// Turn status, surfaced in the editor and local status view.
	chatPhase chat.Phase
	convID    string
	// Increments when the visible conversation changes; delayed local results
	// from an earlier conversation must not enter the new transcript.
	conversationEpoch uint64
	usage             *assistant.Usage
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

	// pendingAccordionToggle is the block a mouse-down landed on inside an
	// accordion header row. Every left-click also begins a potential
	// drag-select (see beginSelection), so the toggle stays pending until
	// finishSelection sees the gesture through: it fires only if the release
	// never turned it into a real selection range.
	pendingAccordionToggle    agent.BlockID
	hasPendingAccordionToggle bool

	// pointerIsHand is the OS pointer shape last written to the terminal, so
	// reconcilePointerShape only emits an OSC 22 sequence on a real change.
	pointerIsHand bool

	// chatMouseMode is the chat surface's mouse tracking, resolved once from
	// the environment (see chatMouseMode()).
	chatMouseMode tea.MouseMode

	// splashReady is set once the terminal has answered the Kitty graphics
	// probe and the logo's pixels have been transmitted.
	splashReady bool

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
	m.engine = engine
	m.convID = engine.ConversationID()
	m.configure(configs)
	return m
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
		editor:            editor.New(),
		list:              chat.NewList(),
		animActivity:      newAnimationTimeline(activityAnimInterval),
		animBorderSweep:   newAnimationTimeline(borderSweepInterval),
		status:            statusview.New(1, 1, theme),
		approvalPanel:     components.NewPanel(theme.Approval.Panel),
		permissionsPanel:  components.NewPanel(theme.Permissions),
		styles:            theme,
		searchSessionID:   newSearchSessionID(),
		entitySearchCache: make(map[string]entitySearchCacheEntry),
		resume:            resume{now: time.Now},
		chatMouseMode:     chatMouseMode(),
	}
	m.editor.SetHistorySource(func() []string { return m.transcript.UserPrompts() })
	m.editor.SetCommands(commandCompletionSpecs())
	m.applyStyles(m.styles)
	return m
}

// ConversationID returns the active conversation id, or "" when none has been
// established yet. main reads it after the program exits to print a resume hint.
func (m *Model) ConversationID() string { return m.convID }

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
	m.approvalPanel.SetStyles(theme.Approval.Panel)
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
		m.clearSelection()
		m.follow = followControl{}
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
		return tea.Batch(m.loginModel.Init(), splash.Query())
	}
	return tea.Batch(m.initChat(), splash.Query())
}

// initChat focuses the editor and, when restoring a conversation, starts the
// history restore through the normal turn-event pump. It is called explicitly
// on login handoff because Bubble Tea calls Init only on the original model.
func (m *Model) initChat() tea.Cmd {
	requestBG := func() tea.Msg { return tea.RequestBackgroundColor() }
	commands := []tea.Cmd{m.editor.Focus(), requestBG, waitToolUI(m.toolUI)}
	if m.engine == nil {
		return tea.Batch(commands...)
	}
	if m.engine.ConversationID() == "" {
		// Fetch the startup offer only when there is no history to restore.
		return tea.Batch(append(commands, m.fetchRecentConversations(), m.syncLocalSkills())...)
	}
	m.chatPhase = chat.PhaseLoading
	ctx, cancel := context.WithTimeout(context.Background(), historyLoadTimeout)
	commands = append(commands, m.begin(opRestore, m.engine.Restore(ctx), cancel))
	return tea.Batch(commands...)
}
