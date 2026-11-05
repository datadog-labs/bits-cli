package tui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

type conversationActivityState uint8

const (
	activityNone conversationActivityState = iota
	activityConnecting
	activityIdle
	activityRemoteTurn
	activityDisconnected
	activityUnavailable
)

type activityUpdateMsg struct {
	generation uint64
	update     agent.ActivityUpdate
}

type activityClosedMsg struct{ generation uint64 }

type activityRetryMsg struct {
	generation     uint64
	conversationID string
}

func waitActivity(generation uint64, events <-chan agent.ActivityUpdate) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-events
		if !ok {
			return activityClosedMsg{generation: generation}
		}
		return activityUpdateMsg{generation: generation, update: update}
	}
}

func (m *Model) startConversationSubscription(conversationID string) tea.Cmd {
	m.stopConversationSubscription()
	m.activityRetry = 0
	return m.connectConversationSubscription(conversationID)
}

func (m *Model) connectConversationSubscription(conversationID string) tea.Cmd {
	if m.engine == nil || conversationID == "" {
		m.activityState = activityNone
		return nil
	}
	if !m.engine.SupportsActivity() {
		m.activityState = activityUnavailable
		m.activityConversationID = conversationID
		return nil
	}
	m.activityGeneration++
	generation := m.activityGeneration
	ctx, cancel := context.WithCancel(context.Background())
	m.activityCancel = cancel
	m.activityConversationID = conversationID
	if m.activityRetry == 0 {
		m.activityState = activityConnecting
	}
	m.activityReplay = agent.NewActivityReplay()
	m.activityNeedsSync = false
	m.activitySnapshotReady = false
	// A dropped stream may have missed the final ended marker. Treat remote
	// activity as idle until replay or live markers report active work, but keep
	// a locally owned failed turn gated for safe client-tool recovery.
	if !m.localTurnOwned {
		m.activityTurnActive = false
	}
	m.activityEvents = m.engine.SubscribeActivity(ctx, conversationID)
	return waitActivity(generation, m.activityEvents)
}

func (m *Model) stopConversationSubscription() {
	m.activityGeneration++
	if m.activityCancel != nil {
		m.activityCancel()
	}
	m.activityCancel = nil
	m.activityEvents = nil
	m.activityConversationID = ""
	m.activityState = activityNone
	m.activityRetry = 0
	m.activityReplay = nil
	m.activityNeedsSync = false
	m.activitySnapshotReady = false
	m.activityTurnActive = false
}

func (m *Model) acceptActivityMessage(generation uint64) bool {
	return m.activityEvents != nil && generation == m.activityGeneration
}

func (m *Model) applyActivityUpdate(msg activityUpdateMsg) tea.Cmd {
	if !m.acceptActivityMessage(msg.generation) {
		return nil
	}
	if msg.update.Err != nil {
		return m.handleActivityDisconnect(msg.generation, msg.update.Err)
	}
	var recovery tea.Cmd
	switch msg.update.Activity.Kind {
	case assistant.ConversationActivityReady:
		m.activitySnapshotReady = true
		m.activityTurnActive = msg.update.Activity.Active
		if msg.update.Activity.Active && m.subscriptionRendersTurn() {
			m.activityState = activityRemoteTurn
			m.publishActivityTranscript()
		} else {
			m.activityState = activityIdle
			if !msg.update.Activity.Active {
				// A ready snapshot can race with a local POST that the server has
				// not marked started yet. Preserve ownership while that request is
				// still running; an ended marker or the local completion will clear it.
				if m.localTurnRecoverable && m.op.run == 0 {
					m.clearLocalTurnOwnership()
				}
				if !m.ownsLocalTurn() {
					m.publishActivityTranscript()
				}
			}
		}
		recovery = m.maybeResumeLocalTurn()
	case assistant.ConversationActivityStarted:
		m.activityTurnActive = true
		if m.subscriptionRendersTurn() {
			m.activityState = activityRemoteTurn
			m.publishActivityTranscript()
		}
	case assistant.ConversationActivityEnded:
		m.activityTurnActive = false
		wasRemote := m.activityState == activityRemoteTurn
		m.activityState = activityIdle
		if wasRemote && m.subscriptionRendersTurn() {
			m.publishActivityTranscript()
		}
		if m.activitySnapshotReady && m.op.run == 0 {
			m.clearLocalTurnOwnership()
		}
	case assistant.ConversationActivityMessage:
		if m.activitySnapshotReady {
			m.activityRetry = 0
		}
		if m.activityReplay != nil {
			m.activityReplay.Append(msg.update.Activity.Message)
			m.activityNeedsSync = true
			if m.activitySnapshotReady && m.activityState == activityRemoteTurn && m.subscriptionRendersTurn() {
				m.publishActivityTranscript()
			}
		}
		recovery = m.maybeResumeLocalTurn()
	}
	return tea.Batch(waitActivity(msg.generation, m.activityEvents), recovery)
}

func (m *Model) publishActivityTranscript() {
	if m.engine == nil || m.op.busy() || !m.activitySnapshotReady || m.activityReplay == nil {
		return
	}
	snapshot, err := m.engine.SyncConversationTranscript(m.activityConversationID, m.activityReplay, m.activityTurnActive)
	if err != nil {
		return
	}
	m.state = snapshot
	m.activityNeedsSync = false
	m.syncApprovals(nil)
}

func (m *Model) syncActivityTranscript() bool {
	if !m.activityNeedsSync || m.engine == nil {
		return true
	}
	snapshot, err := m.engine.SyncConversationTranscript(m.activityConversationID, m.activityReplay, m.activityTurnActive)
	if err != nil {
		return false
	}
	m.activityNeedsSync = false
	m.state = snapshot
	return true
}

// maybeResumeLocalTurn recovers only a turn started by this TUI and only after
// the subscription's ready frame has terminated replay. Waiting for that frame
// prevents replaying a tool call before its already-persisted response arrives.
func (m *Model) maybeResumeLocalTurn() tea.Cmd {
	if !m.localTurnOwned || !m.localTurnRecoverable || m.op.run != 0 ||
		!m.activitySnapshotReady || !m.activityTurnActive || m.engine == nil {
		return nil
	}
	calls := m.engine.PendingActivityTools(m.activityReplay)
	if len(calls) == 0 {
		return nil
	}
	snapshot, err := m.engine.SyncConversationTranscript(m.activityConversationID, m.activityReplay, m.activityTurnActive)
	if err != nil {
		return nil
	}
	m.state = snapshot
	m.activityNeedsSync = false
	m.localTurnRecoverable = false
	ctx, cancel := context.WithCancel(context.Background())
	run, err := m.engine.ResumeToolTurn(ctx, agent.ToolRecoveryInput{
		ConversationID: m.state.ConversationID,
		Calls:          calls,
		Tools:          m.localTurnInput.Tools,
		OnDeny:         m.localTurnInput.OnDeny,
	})
	m.chatPhase = chat.PhaseWaiting
	return m.begin(opTurn, run, err, cancel)
}

func (m *Model) clearLocalTurnOwnership() {
	m.localTurnOwned = false
	m.localTurnRecoverable = false
	m.localTurnInput = agent.TurnInput{}
}

func (m *Model) handleActivityDisconnect(generation uint64, err error) tea.Cmd {
	if !m.acceptActivityMessage(generation) {
		return nil
	}
	m.activityEvents = nil
	if m.activityCancel != nil {
		m.activityCancel()
		m.activityCancel = nil
	}
	if errors.Is(err, agent.ErrActivitySubscriptionUnsupported) {
		m.activityState = activityUnavailable
		return nil
	}
	var apiErr *assistant.APIError
	if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusBadRequest ||
		apiErr.StatusCode == http.StatusForbidden || apiErr.StatusCode == http.StatusNotFound) {
		m.activityState = activityUnavailable
		m.activityReplay = nil
		m.activityNeedsSync = false
		m.clearLocalTurnOwnership()
		return m.postNotice(noticeForError("conversation activity unavailable", err))
	}
	m.activityState = activityDisconnected
	if errors.Is(err, io.EOF) && !m.localTurnRecoverable {
		m.activityState = activityConnecting
		if m.activitySnapshotReady {
			m.activityRetry = 0
		}
	}
	m.activityRetry++
	delay := 250 * time.Millisecond * time.Duration(1<<min(m.activityRetry-1, 4))
	conversationID := m.activityConversationID
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return activityRetryMsg{generation: generation, conversationID: conversationID}
	})
}

func (m *Model) handleActivityClosed(msg activityClosedMsg) tea.Cmd {
	return m.handleActivityDisconnect(msg.generation, io.EOF)
}

func (m *Model) retryConversationSubscription(msg activityRetryMsg) tea.Cmd {
	if msg.generation != m.activityGeneration || msg.conversationID == "" ||
		msg.conversationID != m.state.ConversationID || (m.activityState != activityDisconnected && m.activityState != activityConnecting) {
		return nil
	}
	return m.connectConversationSubscription(msg.conversationID)
}

func (m *Model) ownsLocalTurn() bool {
	return m.localTurnOwned || (m.op.run != 0 && m.chatPhase != chat.PhaseLoading)
}

// subscriptionRendersTurn reports whether the live subscription owns display
// updates. A locally-started turn moves here only after its request stream has
// failed and closed; if recovery starts a client-tool continuation, turnEvents
// takes ownership again.
func (m *Model) subscriptionRendersTurn() bool {
	return !m.ownsLocalTurn() || (m.localTurnRecoverable && m.op.run == 0)
}

func (m *Model) activityBlocksSubmission() bool {
	if m.state.ConversationID == "" {
		return false
	}
	if m.activityEvents != nil && !m.activitySnapshotReady {
		return true
	}
	if m.localTurnOwned && m.activityTurnActive {
		return true
	}
	switch m.activityState {
	case activityNone, activityIdle, activityUnavailable:
		return false
	default:
		return true
	}
}
