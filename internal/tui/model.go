// Package tui is the Bubble Tea front end: it owns UI state, runs the turn-event
// pump, and maps agent events onto the transcript store and renderer.
package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	conversationview "github.com/DataDog/bits-cli/internal/tui/conversations"
	"github.com/DataDog/bits-cli/internal/tui/editor"
	loginui "github.com/DataDog/bits-cli/internal/tui/login"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// Mode is the top-level screen the model shows.
type Mode int

const (
	ModeTermInit Mode = iota
	ModeChat
	ModeLogin
	ModeConversations
)

// EngineFactory constructs the authenticated chat engine after startup login
// has persisted a session.
type EngineFactory func(context.Context) (*agent.Engine, error)

// Model is the root Bubble Tea model. All state lives here and is mutated only
// on the tea thread; the sole async source is the engine's event channel.
type Model struct {
	// Collaborators the model drives.
	engine *agent.Engine
	editor *editor.Editor
	picker *conversationview.Model

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

	// blocks is the latest snapshot of the engine's aggregated transcript
	blocks []agent.Block

	// Active turn: turnEvents is the running turn's event channel (nil when
	// idle); cancelTurn interrupts it.
	turnEvents      <-chan agent.Event
	cancelTurn      context.CancelFunc
	turnGen         uint64
	cancelRequested bool

	// /new and /clear cancel an active turn/restore once, then wait for its
	// channel to close before resetting conversation state.
	pendingNew bool

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

	// notice is the transient status message (error/warn/info) shown in the
	// status line.
	notice    chat.Notice
	noticeSeq int

	// Transcript rendering. list owns the transcript's scroll position and
	// per-item render cache; chatStyles is also used by the notice bar.
	list       *chat.List
	chatStyles chat.Styles
	styles     styles.Theme // terminal styles; dark until detected

	// Terminal dimensions are cached so a chat installed after startup login can
	// be laid out immediately; Bubble Tea does not replay its initial size event.
	width  int
	height int
}

// New builds the root model for the given engine. When the engine is bound to a
// conversation, the model shows its id immediately and restores its history on
// Init.
func New(engine *agent.Engine) *Model {
	m := newShell()
	m.engine = engine
	m.convID = engine.ConversationID()
	return m
}

// NewWithLogin creates the same root TUI in login mode. On successful OAuth,
// factory builds the engine and this model transitions to chat without ending
// the Bubble Tea program or leaving the alternate screen.
func NewWithLogin(ctx context.Context, loginModel *loginui.Model, factory EngineFactory) *Model {
	if ctx == nil {
		ctx = context.Background()
	}
	m := newShell()
	m.mode = ModeLogin
	m.startupCtx = ctx
	m.loginModel = loginModel
	m.engineFactory = factory
	return m
}

func newShell() *Model {
	m := &Model{
		editor: editor.New(),
		list:   chat.NewList(),
		styles: styles.Default(true),
	}
	m.applyStyles(m.styles)
	return m
}

// ConversationID returns the active conversation id, or "" when none has been
// established yet. main reads it after the program exits to print a resume hint.
func (m *Model) ConversationID() string { return m.convID }

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
	if m.picker != nil {
		m.picker.SetStyles(theme)
	}
}

// setMode switches the top-level screen. It is the single entry point for mode
// changes so layout and refresh side effects stay centralized while startup
// login hands control to chat without replacing or quitting the root model.
func (m *Model) setMode(mode Mode) {
	m.mode = mode
	m.refreshViewport()
}

// Init starts the active screen. Login owns initial color detection; a direct
// chat startup and the later login-to-chat handoff both use initChat.
func (m *Model) Init() tea.Cmd {
	if m.mode == ModeLogin {
		if m.loginModel == nil {
			m.startupErr = errors.New("startup login is unavailable")
			return tea.Quit
		}
		return m.loginModel.Init()
	}
	return m.initChat()
}

// initChat focuses the editor and, when restoring a conversation, starts the
// history restore through the normal turn-event pump. It is called explicitly
// on login handoff because Bubble Tea calls Init only on the original model.
func (m *Model) initChat() tea.Cmd {
	requestBG := func() tea.Msg { return tea.RequestBackgroundColor() }
	commands := []tea.Cmd{m.editor.Focus(), requestBG}
	if m.engine == nil || m.engine.ConversationID() == "" {
		return tea.Batch(commands...)
	}
	m.chatPhase = chat.PhaseLoading
	ctx, cancel := context.WithTimeout(context.Background(), historyLoadTimeout)
	events := m.engine.Restore(ctx)
	commands = append(commands, m.beginRemote(events, cancel))
	return tea.Batch(commands...)
}
