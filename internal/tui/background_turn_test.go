package tui

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

func backgroundTurnModel(t *testing.T) (*Model, context.Context) {
	t.Helper()
	m := newModelWithSpy(t)
	t.Cleanup(m.engine.Close)
	t.Cleanup(m.Detach)
	m.mode = ModeChat
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.begin(opTurn, 1, nil, cancel)
	m.chatPhase = chat.PhaseStreaming
	m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventBackground, Run: 1}})
	return m, ctx
}

func TestBackgroundTurnReleasesComposerWithoutCancelingRun(t *testing.T) {
	m, ctx := backgroundTurnModel(t)
	if m.op.busy() || m.op.run != 0 {
		t.Fatalf("root completion left operation busy: %+v", m.op)
	}
	if ctx.Err() != nil {
		t.Fatal("root completion canceled the background stream")
	}
	if m.backgroundRuns[1] == nil {
		t.Fatal("background stream lost its cancellation handle")
	}
	if m.chatPhase != chat.PhaseIdle {
		t.Fatalf("chat phase = %v, want idle", m.chatPhase)
	}
}

func TestBackgroundTurnStateAndTerminalPreserveNewOperation(t *testing.T) {
	for _, terminal := range []agent.EventKind{agent.EventDone, agent.EventError, agent.EventCanceled} {
		t.Run(map[agent.EventKind]string{agent.EventDone: "done", agent.EventError: "error", agent.EventCanceled: "canceled"}[terminal], func(t *testing.T) {
			m, oldContext := backgroundTurnModel(t)
			currentContext, cancel := context.WithCancel(context.Background())
			defer cancel()
			m.begin(opTurn, 2, nil, cancel)
			m.chatPhase = chat.PhaseStreaming
			generation := m.op.gen
			pending := awaitingCall("child-approval")
			pending.Run = 1
			state := agent.State{Version: m.state.Version + 1, ConversationID: "conversation", Blocks: []agent.Block{pending}}
			m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventState, Run: 1, State: state}})
			if m.state.Version != state.Version || m.state.ConversationID != state.ConversationID || waitingApprovals(m) != 1 {
				t.Fatalf("background snapshot was not applied: %+v", m.state)
			}
			event := agent.Event{Kind: terminal, Run: 1}
			if terminal == agent.EventError {
				event.Err = errors.New("child stream failed")
			}
			m.Update(engineEventMsg{ev: event})
			if m.op.run != 2 || !m.op.busy() || m.op.gen != generation || currentContext.Err() != nil {
				t.Fatalf("background terminal changed foreground operation: %+v; context = %v", m.op, currentContext.Err())
			}
			if m.chatPhase != chat.PhaseStreaming {
				t.Fatalf("background terminal changed foreground phase to %v", m.chatPhase)
			}
			if oldContext.Err() == nil || m.backgroundRuns[1] != nil {
				t.Fatal("background terminal retained its context or run")
			}
		})
	}
}

func TestBackgroundTurnApprovalRemainsActionableAfterForegroundEnds(t *testing.T) {
	m, oldContext := backgroundTurnModel(t)
	currentContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.begin(opTurn, 2, nil, cancel)
	pending := awaitingCall("child-approval")
	pending.Run = 1
	m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventState, Run: 1, State: agent.State{Version: m.state.Version + 1, Blocks: []agent.Block{pending}}}})
	m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventDone, Run: 2}})
	if m.op.busy() || currentContext.Err() == nil || oldContext.Err() != nil {
		t.Fatal("foreground completion did not leave only background alive")
	}
	if m.prompt() == nil || waitingApprovals(m) != 1 {
		t.Fatal("foreground completion discarded child approval")
	}
	var decidedID string
	var decision agent.ApprovalDecision
	m.approvals["child-approval"].decide = func(id string, value agent.ApprovalDecision) bool { decidedID, decision = id, value; return true }
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if decidedID != "child-approval" || decision != agent.ApprovalAllowOnce || m.prompt() != nil {
		t.Fatalf("background approval not delivered: id=%q decision=%q prompt=%v", decidedID, decision, m.prompt())
	}
}

func TestBackgroundTurnQuitCancelsEveryRun(t *testing.T) {
	m, oldContext := backgroundTurnModel(t)
	currentContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.begin(opTurn, 2, nil, cancel)
	m.quit()
	if oldContext.Err() == nil || currentContext.Err() == nil {
		t.Fatalf("quit left streams alive: background=%v foreground=%v", oldContext.Err(), currentContext.Err())
	}
}

func TestBackgroundTurnDetachCancelsBackgroundAndUnsubscribes(t *testing.T) {
	m, ctx := backgroundTurnModel(t)
	m.Detach()
	if ctx.Err() == nil || len(m.backgroundRuns) != 0 {
		t.Fatal("detach retained background context")
	}
	for range m.events {
	}
	m.Detach()
}

func TestBackgroundTurnConversationReplacementClearsApprovals(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "new"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			backend := newResumeBackend()
			engine := agent.New(backend, agent.Config{})
			defer engine.Close()
			m := New(engine)
			defer m.Detach()
			m.mode = ModeChat
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m.begin(opTurn, 1, nil, cancel)
			m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventBackground, Run: 1}})
			pending := awaitingCall("old-child")
			pending.Run = 1
			stale := agent.Event{Kind: agent.EventState, Run: 1, State: agent.State{Version: m.state.Version + 1, Blocks: []agent.Block{pending}}}
			m.Update(engineEventMsg{ev: stale})
			if m.prompt() == nil {
				t.Fatal("missing initial approval")
			}
			if resume {
				m.openConversationPicker()
				backend.histories <- resumeHistoryReply{response: history(assistant.AssistantMessage("restored", assistant.TextContent("restored answer")))}
				m.Update(m.startConversationSwitch(resumeConversationID)())
				if m.state.ConversationID != resumeConversationID {
					t.Fatal("conversation was not installed")
				}
			} else {
				m.startNewConversation()
			}
			if ctx.Err() == nil || len(m.backgroundRuns) != 0 || m.prompt() != nil || waitingApprovals(m) != 0 {
				t.Fatal("conversation replacement retained old context or approvals")
			}
			before := m.state.Version
			m.Update(engineEventMsg{ev: stale})
			m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventCanceled, Run: 1}})
			if m.state.Version != before || m.prompt() != nil || waitingApprovals(m) != 0 {
				t.Fatal("queued old events restored old approval")
			}
		})
	}
}

func TestBackgroundTurnStaleSnapshotCannotReplaceApprovals(t *testing.T) {
	m, _ := backgroundTurnModel(t)
	old := awaitingCall("old-child")
	old.Run = 1
	stale := agent.Event{Kind: agent.EventState, Run: 1, State: agent.State{Version: m.state.Version + 1, Blocks: []agent.Block{old}}}
	current := awaitingCall("current-child")
	current.Run = 1
	m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventState, Run: 1, State: agent.State{Version: stale.State.Version + 1, Blocks: []agent.Block{current}}}})
	m.Update(engineEventMsg{ev: stale})
	if m.approvals["old-child"] != nil || m.approvals["current-child"] == nil || waitingApprovals(m) != 1 {
		t.Fatal("rejected snapshot replaced current approvals")
	}
}

type backgroundLogoutBackend struct {
	spyBackend
	canceled chan struct{}
	release  chan struct{}
	cleaned  chan struct{}
}

func (b *backgroundLogoutBackend) Stream(ctx context.Context, _ string, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) error {
	var response assistant.AssistantResponse
	response.RootTurnCompleted = true
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("root", assistant.TextContent("children running"))
	if err := emit(response); err != nil {
		return err
	}
	<-ctx.Done()
	close(b.canceled)
	<-b.release
	close(b.cleaned)
	return ctx.Err()
}

func (*backgroundLogoutBackend) SubmitToolResults(context.Context, []assistant.ClientToolResponse) error {
	return nil
}

func TestBackgroundTurnLogoutWaitsForTransportCleanup(t *testing.T) {
	for _, reset := range []bool{false, true} {
		name := "current conversation"
		if reset {
			name = "previous conversation"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				backend := &backgroundLogoutBackend{spyBackend: spyBackend{t: t}, canceled: make(chan struct{}), release: make(chan struct{}), cleaned: make(chan struct{})}
				engine := agent.New(backend, agent.Config{})
				defer engine.Close()
				called := make(chan struct{})
				m := New(engine, Config{Logout: func(context.Context) (bool, error, error) {
					select {
					case <-backend.cleaned:
					default:
						t.Error("credentials deleted before stream cleanup")
					}
					close(called)
					return true, nil, nil
				}})
				defer m.Detach()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				run, err := engine.StartTurn(ctx, agent.TurnInput{Message: "start"})
				if err != nil {
					t.Fatal(err)
				}
				m.begin(opTurn, run, nil, cancel)
				for {
					ev := <-m.events
					m.Update(engineEventMsg{ev: ev})
					if ev.Kind == agent.EventBackground {
						break
					}
				}
				if reset {
					m.startNewConversation()
				}
				cmd := m.startLogout()
				result := make(chan tea.Msg, 1)
				go func() { result <- cmd() }()
				<-backend.canceled
				synctest.Wait()
				select {
				case <-called:
					t.Fatal("logout ran while stream cleanup was blocked")
				default:
				}
				close(backend.release)
				message := <-result
				<-called
				m.Update(message)
				if !m.LoggedOut() {
					t.Fatal("logout did not finish after background cleanup")
				}
			})
		})
	}
}

func TestBackgroundTurnEscapeCancelsAfterForegroundEnds(t *testing.T) {
	m, oldContext := backgroundTurnModel(t)
	currentContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.begin(opTurn, 2, nil, cancel)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if currentContext.Err() == nil || oldContext.Err() != nil {
		t.Fatal("Escape must cancel only the foreground operation while it is active")
	}
	m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventCanceled, Run: 2}})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if oldContext.Err() == nil {
		t.Fatal("Escape with no foreground operation must cancel background work")
	}
	if m.backgroundRuns[1] == nil {
		t.Fatal("canceled background run must remain registered until its terminal event")
	}
}
