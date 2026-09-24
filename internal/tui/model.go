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
	Tools          *agent.ToolSet
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
	status                 *statusview.Model
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
	entitySearchGeneration uint64
	entitySearchCancel     context.CancelFunc
	entitySearchCache      map[string]entitySearchCacheEntry
	entitySearchCacheOrder []string

	statusGeneration uint64
	statusIdentity   string
	statusCancel     context.CancelFunc

	// Startup login stays inside this root model so Bubble Tea owns the
	// alternate screen continuously while switching from login to chat.
	startupCtx        context.Context
	startupCancel     context.CancelFunc
	startupGeneration uint64
	startupCanceled   bool
	startupStopping   bool
	loginModel        *loginui.Model
	engineFactory     EngineFactory
	startupPending    bool
	startupErr        error

	// transcript is the latest snapshot of the engine's aggregated transcript.
	transcript agent.TranscriptSnapshot

	// Active turn: turnEvents is the running turn's event channel (nil when
	// idle); cancelTurn interrupts it.
	turnEvents      <-chan agent.Event
	cancelTurn      context.CancelFunc
	turnGen         uint64
	cancelRequested bool

	// /new and /clear cancel an active turn/restore once, then wait for its
	// channel to close before resetting conversation state.
	pendingNew bool

	// /logout stops active work before deleting credentials, then discards the
	// authenticated engine.
	logout           LogoutFunc
	pendingLogout    bool
	logoutRunning    bool
	logoutCancel     context.CancelFunc
	logoutGeneration uint64
	loggedOut        bool

	pendingApprovals []agent.Block
	approvalChoice   int
	approvalPanel    *components.Panel

	// Top-level screen; transitions go through setMode.
	mode Mode

	// /resume operations are cancellable and generation-stamped. A late result
	// from a cancelled list/load can never mutate the current conversation.
	conversationGeneration uint64
	conversationCancel     context.CancelFunc
	conversationRetry      conversationRetry
	conversationSwitchID   string
	conversationClosing    bool

	// Turn status, surfaced in the status line.
	chatPhase chat.Phase
	convID    string
	usage     *assistant.Usage
	// connectivity is the last observed remote outcome. Active phases override
	// it with connecting/connected when building the status snapshot.
	connectivity          statusview.Connectivity
	authStateOverride     string
	authFailureObserved   bool
	authFailureGeneration uint64

	// notice is the transient status message (error/warn/info) shown in the
	// status line.
	notice    chat.Notice
	noticeSeq int

	// Transcript rendering. list owns the transcript's scroll position and
	// per-item render cache; chatStyles is also used by the notice bar.
	list       *chat.List
	chatStyles chat.Styles
	styles     styles.Theme // terminal styles; dark until detected

	// One repaint clock samples independent elapsed-time timelines. Tool activity
	// covers tool, thinking, and grouped-inspection spinners; the second timeline
	// drives the composer border sweep.
	animClock       animationClock
	animTool        animationTimeline
	animBorderSweep animationTimeline

	// Terminal dimensions are cached so a chat installed after startup login can
	// be laid out immediately; Bubble Tea does not replay its initial size event.
	width  int
	height int

	// Selection belongs to ModeChat; selection.go encapsulates its gesture and
	// auto-scroll state while the model supplies rendered pane frames.
	selection selection

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
	status := statusview.New(1, 1, theme)
	m := &Model{
		editor:            editor.New(),
		list:              chat.NewList(),
		animTool:          newAnimationTimeline(toolAnimInterval),
		animBorderSweep:   newAnimationTimeline(borderSweepInterval),
		status:            &status,
		approvalPanel:     components.NewPanel(theme.Approval.Panel),
		styles:            theme,
		searchSessionID:   newSearchSessionID(),
		entitySearchCache: make(map[string]entitySearchCacheEntry),
		resume:            resume{now: time.Now},
		chatMouseMode:     chatMouseMode(),
	}
	m.editor.SetHistorySource(func() []string { return m.transcript.UserPrompts() })
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
	if m.startupCanceled || (m.mode == ModeLogin && m.loginModel != nil && m.loginModel.Canceled()) {
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
	if m.picker != nil {
		m.picker.SetStyles(theme)
	}
	if m.status != nil {
		m.status.SetStyles(theme)
	}
}

// setMode switches the top-level screen. It is the single entry point for mode
// changes so layout and refresh side effects stay centralized while startup
// login hands control to chat without replacing or quitting the root model.
func (m *Model) setMode(mode Mode) {
	previous := m.mode
	if previous == ModeChat && mode != ModeChat {
		m.clearSelection()
	}
	m.mode = mode
	if previous == ModeTermInit && mode == ModeChat {
		m.syncTranscript()
		return
	}
	m.layoutTranscript()
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
	commands := []tea.Cmd{m.editor.Focus(), requestBG}
	if m.engine == nil {
		return tea.Batch(commands...)
	}
	if m.engine.ConversationID() == "" {
		// The offer's only fetch for the life of the process: skipping it for a
		// restored conversation leaves the offer empty even after /new clears the
		// transcript. Fetching later would read on the gated path, which can fail
		// the next message with ErrOperationActive.
		return tea.Batch(append(commands, m.fetchRecentConversations())...)
	}
	m.chatPhase = chat.PhaseLoading
	ctx, cancel := context.WithTimeout(context.Background(), historyLoadTimeout)
	events := m.engine.Restore(ctx)
	commands = append(commands, m.beginRemote(events, cancel))
	return tea.Batch(commands...)
}
