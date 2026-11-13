package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

func backgroundTurnModel(t *testing.T) (*Model, context.Context) {
	t.Helper()
	return prepareBackgroundTurnModel(t, newModelWithSpy(t))
}

func prepareBackgroundTurnModel(t *testing.T, m *Model) (*Model, context.Context) {
	t.Helper()
	t.Cleanup(m.engine.Close)
	t.Cleanup(m.Detach)
	m.mode = ModeChat
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.Update(recentConversationsMsg{generation: m.resume.task.gen})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.begin(opTurn, cancel)
	m.watchRun(1, nil)
	m.chatPhase = chat.PhaseStreaming
	m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventBackground, Run: 1}})
	return m, ctx
}

func TestBackgroundTurnStateAndTerminalPreserveNewOperation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		terminal  agent.EventKind
		operation opKind
	}{
		{"done", agent.EventDone, opTurn},
		{"error", agent.EventError, opTurn},
		{"canceled", agent.EventCanceled, opTurn},
		{"error during restore", agent.EventError, opRestore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal := tc.terminal
			m, oldContext := backgroundTurnModel(t)
			if m.op.busy() || m.op.run != 0 || oldContext.Err() != nil || m.backgroundRuns[1] == nil || m.chatPhase != chat.PhaseIdle {
				t.Fatal("root completion must release the composer while retaining the background run")
			}
			currentContext, cancel := context.WithCancel(context.Background())
			defer cancel()
			m.begin(tc.operation, cancel)
			m.watchRun(2, nil)
			m.chatPhase = chat.PhaseWaiting
			generation := m.op.gen
			pending := awaitingCall("child-approval")
			pending.Run = 1
			state := agent.State{
				Version: m.state.Version + 1, ConversationID: "conversation",
				Blocks: []agent.Block{pending, {
					ID:  agent.BlockID{Key: "child-text", Kind: assistant.KindText},
					Run: 1, Kind: assistant.KindText,
					Markdown: &assistant.MarkdownPayload{Content: "child output"},
				}},
			}
			m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventState, Run: 1, State: state}})
			if m.state.Version != state.Version || m.state.ConversationID != state.ConversationID || waitingApprovals(m) != 1 {
				t.Fatalf("background snapshot was not applied: %+v", m.state)
			}
			m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventBackground, Run: 1}})
			if m.op.run != 2 || m.op.gen != generation || currentContext.Err() != nil || oldContext.Err() != nil || m.chatPhase != chat.PhaseWaiting {
				t.Fatal("background state or repeated completion changed foreground ownership")
			}
			event := agent.Event{Kind: terminal, Run: 1}
			if terminal == agent.EventError {
				event.Err = errors.New("child stream failed")
			}
			m.Update(engineEventMsg{ev: event})
			if m.op.kind != tc.operation || m.op.run != 2 || !m.op.busy() || m.op.gen != generation || currentContext.Err() != nil {
				t.Fatalf("background terminal changed foreground operation: %+v; context = %v", m.op, currentContext.Err())
			}
			if m.chatPhase != chat.PhaseWaiting {
				t.Fatalf("background terminal changed foreground phase to %v", m.chatPhase)
			}
			if terminal == agent.EventError {
				notice := latestNotice(m).Text
				if !strings.Contains(notice, "child stream failed") || strings.Contains(notice, "restore failed") {
					t.Fatalf("background error notice = %q", notice)
				}
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
	m.begin(opTurn, cancel)
	m.watchRun(2, nil)
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
			m, ctx := prepareBackgroundTurnModel(t, New(engine))
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
	partialFollowup bool
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
	if b.partialFollowup {
		response.RootTurnCompleted = false
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("followup", assistant.TextContent("partial follow-up"))
		if err := emit(response); err != nil {
			return err
		}
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
				m.begin(opTurn, cancel)
				m.watchRun(run, nil)
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
	m.begin(opTurn, cancel)
	m.watchRun(2, nil)
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
	if m.exitCard.open {
		t.Fatal("Escape must cancel background work before offering to exit")
	}
	m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventCanceled, Run: 1}})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.exitCard.open {
		t.Fatal("Escape must offer to exit once all runs have ended")
	}
}

func TestBeginFailureEndsOperation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   opKind
		phase  chat.Phase
		notice string
	}{
		{"turn", opTurn, chat.PhaseError, "startup failed"},
		{"restore", opRestore, chat.PhaseIdle, "restore failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModelWithSpy(t)
			defer m.engine.Close()
			defer m.Detach()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m.chatPhase = chat.PhaseWaiting
			m.begin(tc.kind, cancel)
			m.watchRun(0, errors.New("startup failed"))
			if ctx.Err() == nil || m.op.busy() || m.op.run != 0 || m.op.gen != 1 {
				t.Fatalf("failed startup retained operation: %+v; context=%v", m.op, ctx.Err())
			}
			if m.chatPhase != tc.phase || !strings.Contains(latestNotice(m).Text, tc.notice) {
				t.Fatalf("failed startup phase=%v notice=%+v", m.chatPhase, latestNotice(m))
			}
		})
	}
}

func TestBackgroundTurnCtrlCConfirmsBeforeCancelingRuns(t *testing.T) {
	for _, foreground := range []bool{false, true} {
		name := "background only"
		if foreground {
			name = "overlapping foreground"
		}
		t.Run(name, func(t *testing.T) {
			m, backgroundContext := backgroundTurnModel(t)
			foregroundContext, cancel := context.WithCancel(context.Background())
			defer cancel()
			if foreground {
				m.begin(opTurn, cancel)
				m.watchRun(2, nil)
			}
			m.editor.SetValue("next thought")
			_, cmd := m.Update(ctrlC)
			if isQuitCmd(cmd) || m.editor.Value() != "" || m.quitConfirm.armed {
				t.Fatal("clearing the draft must neither quit nor arm exit")
			}
			if backgroundContext.Err() != nil || foregroundContext.Err() != nil {
				t.Fatal("clearing the draft canceled a run")
			}
			_, cmd = m.Update(ctrlC)
			if isQuitCmd(cmd) || !m.quitConfirm.armed {
				t.Fatal("first empty-prompt Ctrl+C must arm exit")
			}
			if backgroundContext.Err() != nil || foregroundContext.Err() != nil {
				t.Fatal("arming exit canceled a run")
			}
			_, cmd = m.Update(ctrlC)
			if !isQuitCmd(cmd) || backgroundContext.Err() == nil {
				t.Fatal("confirmed exit must quit and cancel the background run")
			}
			if foreground && foregroundContext.Err() == nil {
				t.Fatal("confirmed exit left the foreground run active")
			}
		})
	}
}

func TestForegroundStreamingStatusIgnoresBackgroundContent(t *testing.T) {
	m, _ := backgroundTurnModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.begin(opTurn, cancel)
	m.watchRun(2, nil)
	m.chatPhase = chat.PhaseWaiting
	block := agent.Block{ID: agent.BlockID{Key: "background", Kind: assistant.KindText}, Run: 1, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: "follow-up"}}
	state := agent.State{Version: m.state.Version + 1, Blocks: []agent.Block{block}}
	m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventState, Run: 2, State: state}})
	if m.chatPhase != chat.PhaseWaiting || ctx.Err() != nil {
		t.Fatal("background text changed foreground status")
	}
	block.ID.Key = "foreground"
	block.Run = 2
	state.Version++
	state.Blocks = append(state.Blocks, block)
	m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventState, Run: 2, State: state}})
	if m.chatPhase != chat.PhaseStreaming {
		t.Fatal("foreground text did not update streaming status")
	}
}

func TestFailedLogoutRefreshesCanceledBackgroundStream(t *testing.T) {
	backend := &backgroundLogoutBackend{spyBackend: spyBackend{t: t}, partialFollowup: true, canceled: make(chan struct{}), release: make(chan struct{}), cleaned: make(chan struct{})}
	close(backend.release)
	engine := agent.New(backend, agent.Config{})
	defer engine.Close()
	m := New(engine, Config{Logout: func(context.Context) (bool, error, error) {
		return false, nil, errors.New("credential store unavailable")
	}})
	defer m.Detach()
	m.mode = ModeChat
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run, err := engine.StartTurn(ctx, agent.TurnInput{Message: "start"})
	if err != nil {
		t.Fatal(err)
	}
	m.begin(opTurn, cancel)
	m.watchRun(run, nil)
	for {
		ev := <-m.events
		m.Update(engineEventMsg{ev: ev})
		if ev.Kind == agent.EventState && len(m.backgroundRuns) > 0 && ev.State.HasStreamingContent(run) {
			break
		}
	}
	result := m.startLogout()()
	for {
		ev := <-m.events
		m.Update(engineEventMsg{ev: ev})
		if ev.Terminal() {
			break
		}
	}
	m.Update(result)
	if engine.State().HasStreamingContent(run) {
		t.Fatal("engine did not settle background stream")
	}
	if m.state.HasStreamingContent(run) {
		t.Fatal("failed logout retains canceled background block as streaming")
	}
}

func TestBackgroundActivityFooterAndClock(t *testing.T) {
	m, _ := backgroundTurnModel(t)
	m.workspaceDisplayPath = "/workspace"
	if got := ansi.Strip(m.chatFooter()); !strings.Contains(got, "1 background run") || !strings.Contains(got, "/workspace") {
		t.Fatalf("footer = %q", got)
	}
	if !m.animClock.armed || m.animBorderSweep.active {
		t.Fatal("background activity must animate without marking the composer busy")
	}
	if m.promptPlaceholder() != "Ask Bits…" {
		t.Fatal("background activity blocked the composer")
	}
	m.backgroundRuns[2] = func() {}
	if got := ansi.Strip(m.chatFooter()); !strings.Contains(got, "2 background runs") {
		t.Fatalf("footer = %q", got)
	}
	m.applyStyles(m.styles.WithoutMotion())
	m.syncAnimations()
	if m.animClock.armed {
		t.Fatal("background activity ignored disabled motion")
	}
	if got := ansi.Strip(m.chatFooter()); !strings.Contains(got, "• 2 background runs") {
		t.Fatalf("static footer = %q", got)
	}
	m.Update(ctrlC)
	if got := ansi.Strip(m.chatFooter()); !strings.Contains(got, quitConfirmHint) || strings.Contains(got, "background") {
		t.Fatalf("quit confirmation must take precedence: %q", got)
	}
	m.Update(quitConfirmExpiredMsg{token: m.quitConfirm.token})
	m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventDone, Run: 1}})
	m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventDone, Run: 2}})
	if got := ansi.Strip(m.chatFooter()); strings.Contains(got, "background") || !strings.Contains(got, "/workspace") {
		t.Fatalf("finished footer = %q", got)
	}
}

func TestBackgroundActivityAnimationStopsWhenHiddenOrFinished(t *testing.T) {
	m, _ := backgroundTurnModel(t)
	m.Update(tea.WindowSizeMsg{Width: 10, Height: 3})
	if m.animClock.armed {
		t.Fatal("hidden background indicator kept animation clock running")
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	if !m.animClock.armed {
		t.Fatal("visible background indicator did not restart clock")
	}
	m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventDone, Run: 1}})
	if m.animClock.armed {
		t.Fatal("completed background run kept animation clock running")
	}
}

func TestBackgroundActivityDoesNotAnimateFinishedRun(t *testing.T) {
	m, _ := backgroundTurnModel(t)
	stale := animToolBlock(agent.ToolRunning)
	stale.Run = 2
	m.state.Blocks = []agent.Block{stale}
	m.syncTranscript()
	m.syncAnimations()
	if m.list.HasAnimated() {
		t.Fatal("unrelated background run animated a stale tool")
	}
	if !m.animClock.armed {
		t.Fatal("background footer must still animate")
	}
	stale.Run = 1
	m.state.Blocks = []agent.Block{stale}
	m.syncTranscript()
	m.syncAnimations()
	if !m.list.HasAnimated() || !m.animClock.armed {
		t.Fatal("active background tool must share the activity clock")
	}
	m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventDone, Run: 1}})
	if m.list.HasAnimated() || m.animClock.armed {
		t.Fatal("completed background run left tool motion active")
	}
}

func TestBackgroundFooterWidePathFits(t *testing.T) {
	m, _ := backgroundTurnModel(t)
	m.workspaceDisplayPath = "/工作/very-long-project/with-many-components"
	for width := 12; width <= 100; width++ {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		if got := ansi.StringWidth(m.chatFooter()); got > m.list.Width() {
			t.Fatalf("footer width %d exceeds %d", got, m.list.Width())
		}
	}
}

type backgroundApprovalBackend struct {
	spyBackend
	release chan struct{}
}

func (b *backgroundApprovalBackend) Stream(ctx context.Context, prompt string, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) error {
	if prompt == "old" {
		var response assistant.AssistantResponse
		response.RootTurnCompleted = true
		if err := emit(response); err != nil {
			return err
		}
		response.RootTurnCompleted = false
		content := assistant.ToolCallContent("child-approval", approvalToolName, "{}")
		content.Type = assistant.ContentClientToolCall
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("child", content)
		if err := emit(response); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*backgroundApprovalBackend) SubmitToolResults(context.Context, []assistant.ClientToolResponse) error {
	return nil
}

func TestBackgroundToolStopDoesNotPauseForegroundQueue(t *testing.T) {
	backend := &backgroundApprovalBackend{spyBackend: spyBackend{t: t}, release: make(chan struct{})}
	tools, err := agent.NewToolSet(newApprovalTool())
	if err != nil {
		t.Fatal(err)
	}
	engine := agent.New(backend, agent.Config{})
	t.Cleanup(engine.Close)
	m := New(engine, Config{Tools: tools})
	t.Cleanup(m.Detach)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	setConversationInput(m, "old")
	m.submit()
	for waitingApprovals(m) == 0 {
		m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
	old := m.shown.callID()
	setConversationInput(m, "current")
	m.submit()
	run := m.op.run
	setConversationInput(m, "later")
	m.submit()
	if len(m.state.Queued) != 1 || len(m.backgroundRuns) != 1 {
		t.Fatal("missing queued prompt or background run")
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if m.op.run != run || m.op.stop != stopNone {
		t.Fatal("background tool stop changed foreground stop state")
	}
	for m.approvals[old] != nil {
		m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
	close(backend.release)
	drainConversationRemote(t, m)
	if m.op.busy() || len(m.state.Queued) != 0 {
		t.Fatal("background tool stop paused foreground queue")
	}
	found := false
	for _, prompt := range m.state.UserPrompts() {
		found = found || prompt.Text == "later"
	}
	if !found {
		t.Fatal("queued foreground prompt was not sent")
	}
}
