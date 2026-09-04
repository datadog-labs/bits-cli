package tui

import (
	"context"
	"errors"
	"net"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	statusview "github.com/DataDog/bits-cli/internal/tui/status"
)

type statusEnvironmentMsg struct {
	generation  uint64
	environment statusview.Environment
}

type statusIdentityMsg struct {
	generation uint64
	identity   assistant.CurrentUser
	err        error
}

type statusClosedMsg struct{ generation uint64 }

// openStatus switches to the status surface and collects fresh workspace and
// identity state asynchronously. Active engine events continue through the
// root Update loop while the surface is open.
func (m *Model) openStatus() tea.Cmd {
	if m.status == nil {
		status := statusview.New(m.width, m.height, m.styles)
		m.status = &status
	}
	m.statusGeneration++
	generation := m.statusGeneration
	if m.statusCancel != nil {
		m.statusCancel()
	}
	statusContext, cancel := context.WithCancel(context.Background())
	m.statusCancel = cancel
	m.statusIdentity = "collecting…"
	m.status.SetSize(m.width, m.height)
	m.status.Open(m.statusRuntime())
	m.setMode(ModeStatus)
	m.clearNotice()
	provider := m.statusProvider
	if provider == nil {
		provider = statusview.SystemProvider{}
	}
	engine := m.engine
	environmentCommand := func() tea.Msg {
		return statusEnvironmentMsg{
			generation:  generation,
			environment: provider.Collect(statusContext),
		}
	}
	identityCommand := func() tea.Msg {
		if engine == nil {
			return statusIdentityMsg{generation: generation, err: agent.ErrCurrentUserUnsupported}
		}
		identity, err := engine.CurrentUser(statusContext)
		return statusIdentityMsg{generation: generation, identity: identity, err: err}
	}
	return tea.Batch(environmentCommand, identityCommand)
}

func (m *Model) closeStatus() {
	m.statusGeneration++
	if m.statusCancel != nil {
		m.statusCancel()
		m.statusCancel = nil
	}
	m.setMode(ModeChat)
}

func (m *Model) applyStatusEnvironment(message statusEnvironmentMsg) {
	if m.mode != ModeStatus || message.generation != m.statusGeneration || m.status == nil {
		return
	}
	m.status.SetEnvironment(message.environment)
}

func (m *Model) applyStatusIdentity(message statusIdentityMsg) {
	if m.mode != ModeStatus || message.generation != m.statusGeneration || m.status == nil {
		return
	}
	switch {
	case message.err == nil:
		m.markAuthenticated()
	case errors.Is(message.err, assistant.ErrUnauthorized):
		m.markAuthenticationFailed()
	case errors.Is(message.err, assistant.ErrForbidden):
		m.markAuthenticated()
	}
	if message.err != nil {
		m.statusIdentity = unavailableIdentity(message.err)
	} else {
		m.statusIdentity = identityLabel(message.identity)
	}
	m.status.SetRuntime(m.statusRuntime())
}

func (m *Model) updateStatus(message tea.Msg) tea.Cmd {
	if m.status == nil {
		return nil
	}
	generation := m.statusGeneration
	next, command := m.status.Update(message)
	*m.status = next
	if command == nil {
		return nil
	}
	return func() tea.Msg {
		message := command()
		if _, ok := message.(statusview.ClosedMsg); ok {
			return statusClosedMsg{generation: generation}
		}
		return message
	}
}

func (m *Model) syncStatus() {
	if m.mode == ModeStatus && m.status != nil {
		m.status.SetRuntime(m.statusRuntime())
	}
}

func (m *Model) statusRuntime() statusview.Runtime {
	var runtime agent.RuntimeStatus
	if m.engine != nil {
		runtime = m.engine.Status()
	}
	authenticationState := runtime.Backend.AuthenticationState
	if m.authStateOverride != "" {
		authenticationState = m.authStateOverride
	}
	approvalMode := ""
	if m.tools != nil {
		approvalMode = string(m.tools.ApprovalMode())
	}
	return statusview.Runtime{
		Site:                runtime.Backend.Site,
		AuthenticationMode:  runtime.Backend.AuthenticationMode,
		AuthenticationState: authenticationState,
		Identity:            m.statusIdentity,
		ConversationID:      m.convID,
		Profile:             string(runtime.Profile),
		Model:               runtime.Model,
		ApprovalMode:        approvalMode,
		Phase:               phaseName(m.chatPhase),
		Connectivity:        m.statusConnectivity(),
		Usage:               m.usage,
	}
}

func identityLabel(identity assistant.CurrentUser) string {
	user := firstNonEmpty(identity.Name, identity.Handle, identity.Email)
	organization := firstNonEmpty(identity.Organization, identity.OrganizationID)
	switch {
	case user != "" && organization != "":
		return user + " · " + organization
	case user != "":
		return user
	case organization != "":
		return organization
	default:
		return "unavailable — identity response did not include a user or organization"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func unavailableIdentity(err error) string {
	switch {
	case errors.Is(err, agent.ErrCurrentUserUnsupported):
		return "unavailable — authenticated identity is not exposed by this backend"
	case errors.Is(err, assistant.ErrUnauthorized):
		return "unavailable — Datadog login must be refreshed"
	case errors.Is(err, assistant.ErrForbidden):
		return "unavailable — current login does not grant profile access"
	default:
		return "unavailable — identity could not be loaded"
	}
}

func phaseName(phase chat.Phase) string {
	switch phase {
	case chat.PhaseLoading:
		return "loading"
	case chat.PhaseWaiting:
		return "waiting"
	case chat.PhaseStreaming:
		return "streaming"
	case chat.PhaseError:
		return "error"
	default:
		return "idle"
	}
}

func (m *Model) statusConnectivity() statusview.Connectivity {
	switch m.chatPhase {
	case chat.PhaseLoading, chat.PhaseWaiting:
		return statusview.ConnectivityConnecting
	case chat.PhaseStreaming:
		return statusview.ConnectivityConnected
	case chat.PhaseIdle, chat.PhaseError:
		break
	}
	if m.connectivity == "" {
		return statusview.ConnectivityNotChecked
	}
	return m.connectivity
}

// observeEvent retains only a coarse transport/auth outcome. Error strings are
// never copied into status state.
func (m *Model) observeEvent(event agent.Event) {
	switch event.Kind {
	case agent.EventTranscript:
		if event.Origin != agent.TranscriptOriginLocal {
			m.markConnected()
		}
	case agent.EventUsage, agent.EventConversation, agent.EventTurnDone:
		m.markConnected()
	case agent.EventError:
		if !event.BackendFailure {
			return
		}
		if errors.Is(event.Err, assistant.ErrUnauthorized) {
			m.markAuthenticationFailed()
		} else if errors.Is(event.Err, assistant.ErrForbidden) {
			m.markAuthenticated()
		}
		var networkError net.Error
		if errors.As(event.Err, &networkError) {
			m.connectivity = statusview.ConnectivityOffline
		} else {
			m.connectivity = statusview.ConnectivityError
		}
	}
}

func (m *Model) markConnected() {
	m.connectivity = statusview.ConnectivityConnected
	if m.authFailureObserved && m.turnGen <= m.authFailureGeneration {
		return
	}
	if m.engine == nil {
		return
	}
	backend := m.engine.Status().Backend
	if backend.AuthenticationMode != "" && backend.AuthenticationState != "unauthenticated" {
		m.markAuthenticated()
	}
}

func (m *Model) markAuthenticationFailed() {
	m.authStateOverride = "authentication failed"
	m.authFailureObserved = true
	m.authFailureGeneration = m.turnGen
}

func (m *Model) markAuthenticated() {
	m.authStateOverride = "authenticated"
	m.authFailureObserved = false
}
