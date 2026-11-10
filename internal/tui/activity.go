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

// localTurnCatchUp follows the latest local turn through live subscription
// markers. Historical markers in a replay never advance it.
type localTurnCatchUp uint8

const (
	localTurnCaughtUp localTurnCatchUp = iota
	localTurnSent                      // no live marker has shown the turn yet
	localTurnObserved                  // a live start (or active ready) followed the send
)

type activityUpdateMsg struct {
	generation uint64
	update     agent.ActivityUpdate
}

type activityClosedMsg struct{ generation uint64 }

// activityReadyTimeoutMsg fires when an attachment has not finished replay.
type activityReadyTimeoutMsg struct{ generation uint64 }

const (
	// activityReadyTimeout and activityGateRetries bound how long prompts wait
	// for a subscription that cannot attach. After either, prompts fail open.
	activityReadyTimeout = 10 * time.Second
	activityGateRetries  = 3
)

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
	return tea.Batch(waitActivity(generation, m.activityEvents), tea.Tick(activityReadyTimeout, func(time.Time) tea.Msg {
		return activityReadyTimeoutMsg{generation: generation}
	}))
}

// handleActivityReadyTimeout stops holding prompts for an attachment that
// has not become ready.
func (m *Model) handleActivityReadyTimeout(msg activityReadyTimeoutMsg) tea.Cmd {
	if msg.generation != m.activityGeneration || m.activitySnapshotReady || m.activityConversationID == "" {
		return nil
	}
	return m.openActivityGate()
}

// openActivityGate lets prompts through while the subscription cannot attach.
// A ready attachment closes it again.
func (m *Model) openActivityGate() tea.Cmd {
	id := m.activityConversationID
	if id == "" || m.activityGateOpenFor == id {
		return nil
	}
	m.activityGateOpenFor = id
	return m.postNotice(notice(chat.NoticeWarn, nil, "Live conversation updates are unavailable. Prompts are no longer held while reconnecting."))
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

// activityAttached reports whether a ready subscription follows the current
// conversation and has caught up with the last local turn.
func (m *Model) activityAttached() bool {
	return m.activityEvents != nil && m.activitySnapshotReady && m.activityLocalTurn == localTurnCaughtUp &&
		m.activityConversationID != "" && m.activityConversationID == m.state.ConversationID
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
		m.activityGateOpenFor = ""
		m.activityTurnActive = msg.update.Activity.Active
		if msg.update.Activity.Active && m.activityLocalTurn == localTurnSent {
			// Prompts are held during confirmed remote turns, so an active
			// turn after a send is normally the one this TUI sent.
			m.activityLocalTurn = localTurnObserved
		}
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
		if m.activitySnapshotReady && m.activityLocalTurn == localTurnSent {
			m.activityLocalTurn = localTurnObserved
		}
		if m.subscriptionRendersTurn() {
			m.activityState = activityRemoteTurn
			m.publishActivityTranscript()
		}
	case assistant.ConversationActivityEnded:
		m.activityTurnActive = false
		// Every message of the local turn precedes its live end marker.
		if m.activitySnapshotReady && m.activityLocalTurn == localTurnObserved {
			m.activityLocalTurn = localTurnCaughtUp
		}
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

// syncActivityTranscript folds buffered activity before a local prompt. An
// unfinished replay is never applied: it would replace the transcript with a
// partial history. The prompt then proceeds from the engine's transcript.
func (m *Model) syncActivityTranscript() bool {
	if !m.activityNeedsSync || m.engine == nil || !m.activitySnapshotReady {
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
	if !m.localTurnOwned || !m.localTurnRecoverable || m.op.busy() ||
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
	m.activityLocalTurn = localTurnSent
	ctx, cancel := context.WithCancel(context.Background())
	if !m.begin(opTurn, cancel) {
		return nil
	}
	run, err := m.engine.ResumeToolTurn(ctx, agent.ToolRecoveryInput{
		ConversationID: m.state.ConversationID,
		Calls:          calls,
		Tools:          m.localTurnInput.Tools,
		OnDeny:         m.localTurnInput.OnDeny,
	})
	m.chatPhase = chat.PhaseWaiting
	return m.watchRun(run, err)
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
	if errors.As(err, &apiErr) && activityUnavailableStatus(apiErr.StatusCode) {
		m.activityState = activityUnavailable
		m.activityReplay = nil
		m.activityNeedsSync = false
		m.clearLocalTurnOwnership()
		return m.postNotice(noticeForError("conversation activity unavailable", err))
	}
	m.activityState = activityDisconnected
	// Servers close idle streams, sometimes without a clean EOF. Reattach
	// quietly unless a failed local turn is waiting for a fresh replay.
	quiet := m.activitySnapshotReady || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
	if quiet && !m.localTurnRecoverable {
		m.activityState = activityConnecting
		if m.activitySnapshotReady {
			m.activityRetry = 0
		}
	}
	m.activityRetry++
	var opened tea.Cmd
	if m.activityRetry >= activityGateRetries {
		opened = m.openActivityGate()
	}
	delay := 250 * time.Millisecond * time.Duration(1<<min(m.activityRetry-1, 4))
	conversationID := m.activityConversationID
	return tea.Batch(opened, tea.Tick(delay, func(time.Time) tea.Msg {
		return activityRetryMsg{generation: generation, conversationID: conversationID}
	}))
}

// activityUnavailableStatus reports responses that retrying will not fix.
func activityUnavailableStatus(code int) bool {
	switch code {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	}
	return false
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

// activityBlockedNotice explains why a prompt was held.
func (m *Model) activityBlockedNotice() tea.Cmd {
	if m.activityState == activityRemoteTurn || m.activityTurnActive {
		return m.postNotice(notice(chat.NoticeWarn, nil, "Another turn is in progress in this conversation. Wait for it to finish."))
	}
	return m.postNotice(notice(chat.NoticeWarn, nil, "Syncing the conversation. Try again in a moment."))
}

func (m *Model) activityBlocksSubmission() bool {
	if m.state.ConversationID == "" {
		return false
	}
	if m.activityGateOpenFor == m.state.ConversationID {
		// Fail open: hold prompts only for a turn the server confirmed.
		return m.activityState == activityRemoteTurn
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
