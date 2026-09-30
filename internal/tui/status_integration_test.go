package tui

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/agent/fake"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	statusview "github.com/DataDog/bits-cli/internal/tui/status"
	"github.com/DataDog/bits-cli/internal/workspace"
)

type localStatusBackend struct{ t *testing.T }

func (b *localStatusBackend) Send(context.Context, any, assistant.SendOptions, func(assistant.AssistantResponse) error) (string, error) {
	b.t.Fatal("/status sent an Assistant chat request")
	return "", nil
}

func (*localStatusBackend) BackendStatus() assistant.BackendStatus {
	return assistant.BackendStatus{
		Site:                "https://api.us3.datadoghq.com",
		AuthenticationMode:  "oauth",
		AuthenticationState: "configured, not verified",
		HasCredentials:      true,
	}
}

func (*localStatusBackend) CurrentUser(context.Context) (assistant.CurrentUser, error) {
	return assistant.CurrentUser{
		Name:           "Bits User",
		Handle:         "bits.user@example.com",
		Email:          "bits.user@example.com",
		Organization:   "Bits Staging",
		OrganizationID: "bits-staging",
	}, nil
}

func newStatusModel(t *testing.T) *Model {
	t.Helper()
	return newStatusModelWithBackend(t, &localStatusBackend{t: t})
}

func newStatusModelWithBackend(t *testing.T, backend agent.Backend) *Model {
	t.Helper()
	return newStatusModelAt(t, t.TempDir(), backend)
}

func newStatusModelAt(t *testing.T, root string, backend agent.Backend) *Model {
	t.Helper()
	tools, err := agent.NewToolSet(agent.ModeManual)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	m := New(
		agent.New(backend, assistant.SendOptions{Model: "model-x"}),
		Config{Tools: tools, Workspace: ws},
	)
	_ = m.editor.Focus()
	return m
}

func statusBatch(t *testing.T, command tea.Cmd) tea.BatchMsg {
	t.Helper()
	batch, ok := command().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("status command = %T with %d children, want two-command batch", batch, len(batch))
	}
	return batch
}

func runStatusLoad(t *testing.T, m *Model, command tea.Cmd) {
	t.Helper()
	message := command()
	if batch, ok := message.(tea.BatchMsg); ok {
		for _, child := range batch {
			_, _ = m.Update(child())
		}
		return
	}
	_, _ = m.Update(message)
}

func TestSubmitStatusIsLocalAndLoadsFreshWorkspace(t *testing.T) {
	repo := t.TempDir()
	runStatusGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runStatusGit(t, repo, "add", ".")
	runStatusGit(t, repo, "-c", "user.name=Bits Test", "-c", "user.email=bits@example.com", "commit", "-m", "initial")
	m := newStatusModelAt(t, repo, &localStatusBackend{t: t})
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 40})
	m.editor.Update(tea.PasteMsg{Content: "/status"})
	_, command := m.submit()
	if command == nil || m.mode != ModeStatus {
		t.Fatalf("status open state: command=%v mode=%v", command != nil, m.mode)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "User / org") || !strings.Contains(view, "collecting…") {
		t.Fatalf("status did not show pending identity state:\n%s", view)
	}
	runStatusLoad(t, m, command)
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"https://api.us3.datadoghq.com", "oauth · authenticated", "cli", "model-x", "manual",
		"Bits User · Bits Staging",
		"Workspace", "Directory", "Repository", filepath.Base(repo), "main", "clean",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("status missing %q:\n%s", want, view)
		}
	}

	m.closeStatus()
	runStatusGit(t, repo, "switch", "-c", "fresh-branch")
	_, command = m.dispatchCommand("status", "")
	runStatusLoad(t, m, command)
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "fresh-branch") {
		t.Fatalf("reopened status did not load a fresh workspace snapshot:\n%s", view)
	}
}

func TestStatusExecutesOnFirstEnterWithExactCompletionOpen(t *testing.T) {
	m := newStatusModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.editor.Update(tea.PasteMsg{Content: "/status"})
	if !m.editor.MenuOpen() {
		t.Fatal("expected exact /status completion menu to be open")
	}

	_, command := m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil || m.mode != ModeStatus {
		t.Fatalf("first Enter did not execute /status: command=%v mode=%v", command != nil, m.mode)
	}
}

func TestStatusDuringActiveTurnPreservesTurnAndTracksObservedState(t *testing.T) {
	m := newStatusModel(t)
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 40})
	turn := make(chan agent.Event)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.op = operation{kind: opTurn, gen: 7, events: turn, cancel: cancel}
	m.chatPhase = chat.PhaseStreaming
	m.editor.Update(tea.PasteMsg{Content: "draft survives"})

	_, load := m.dispatchCommand("status", "")
	if load == nil || m.mode != ModeStatus || m.op.events != turn || ctx.Err() != nil {
		t.Fatalf("opening status changed active turn: load=%v mode=%v turn=%v cancelled=%v", load != nil, m.mode, m.op.events == turn, ctx.Err() != nil)
	}
	runStatusLoad(t, m, load)
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "streaming") || !strings.Contains(view, "connected") {
		t.Fatalf("active status did not show observed stream state:\n%s", view)
	}

	_, _ = m.Update(turnEventMsg{generation: 7, ev: agent.Event{Kind: agent.EventConversation, ConvID: "conversation-live"}})
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "conversation-live") {
		t.Fatalf("open status did not refresh conversation id:\n%s", view)
	}

	offline := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("network unavailable")}
	_, _ = m.Update(turnEventMsg{generation: 7, ev: agent.Event{Kind: agent.EventError, Err: offline, BackendFailure: true}})
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "offline") || !strings.Contains(view, "error") {
		t.Fatalf("open status did not retain observed offline failure:\n%s", view)
	}
	if ctx.Err() != nil {
		t.Fatal("status updates cancelled the active turn")
	}

	closeCommand := m.updateStatus(tea.KeyPressMsg{Code: tea.KeyEscape})
	if closeCommand == nil {
		t.Fatal("status escape returned no close command")
	}
	_, _ = m.Update(closeCommand())
	if m.mode != ModeChat || m.editor.Value() != "draft survives" || m.op.events != turn || ctx.Err() != nil {
		t.Fatalf("closing status changed chat: mode=%v draft=%q turn=%v cancelled=%v", m.mode, m.editor.Value(), m.op.events == turn, ctx.Err() != nil)
	}
}

func TestLocalTurnErrorDoesNotBecomeAssistantConnectivityFailure(t *testing.T) {
	m := newStatusModel(t)
	m.observeEvent(agent.Event{Kind: agent.EventError, Err: errors.New("local tool failed")})

	if got := m.statusConnectivity(); got != statusview.ConnectivityNotChecked {
		t.Fatalf("connectivity after local failure = %q, want not checked", got)
	}
}

func TestSuccessfulBackendEventConfirmsConfiguredAuthentication(t *testing.T) {
	m := newStatusModel(t)
	m.observeEvent(agent.Event{Kind: agent.EventUsage})

	if got := m.statusRuntime().AuthenticationState; got != "authenticated" {
		t.Fatalf("authentication after successful backend event = %q, want authenticated", got)
	}
}

func TestSuccessfulBackendEventDoesNotAuthenticateBackendWithoutCredentials(t *testing.T) {
	m := newStatusModelWithBackend(t, fake.New())
	m.observeEvent(agent.Event{Kind: agent.EventUsage})

	if got := m.statusRuntime().AuthenticationState; got != "unauthenticated" {
		t.Fatalf("authentication after fake backend event = %q, want unauthenticated", got)
	}
}

type statusIdentityResultBackend struct {
	*localStatusBackend
	identity assistant.CurrentUser
	err      error
}

func (b *statusIdentityResultBackend) CurrentUser(context.Context) (assistant.CurrentUser, error) {
	return b.identity, b.err
}

func TestStatusExplainsMissingProfilePermissionWithoutRenderingServerError(t *testing.T) {
	backend := &statusIdentityResultBackend{
		localStatusBackend: &localStatusBackend{t: t},
		err:                errors.Join(assistant.ErrForbidden, errors.New("sensitive upstream detail")),
	}
	m := newStatusModelWithBackend(t, backend)
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 40})

	_, command := m.dispatchCommand("status", "")
	runStatusLoad(t, m, command)
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "unavailable — current login does not grant profile access") {
		t.Fatalf("status did not explain missing profile permission:\n%s", view)
	}
	if !strings.Contains(view, "oauth · authenticated") {
		t.Fatalf("forbidden profile lookup did not confirm authentication:\n%s", view)
	}
	if strings.Contains(view, "sensitive upstream detail") {
		t.Fatalf("status rendered raw server error:\n%s", view)
	}
}

func TestStatusMarksUnauthorizedIdentityLookupAsAuthenticationFailure(t *testing.T) {
	backend := &statusIdentityResultBackend{
		localStatusBackend: &localStatusBackend{t: t},
		err:                assistant.ErrUnauthorized,
	}
	m := newStatusModelWithBackend(t, backend)
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 40})

	_, command := m.dispatchCommand("status", "")
	runStatusLoad(t, m, command)
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "oauth · authentication failed") {
		t.Fatalf("unauthorized profile lookup did not mark authentication failed:\n%s", view)
	}
}

func TestActiveTurnEventCannotClearNewerIdentityAuthenticationFailure(t *testing.T) {
	backend := &statusIdentityResultBackend{
		localStatusBackend: &localStatusBackend{t: t},
		err:                assistant.ErrUnauthorized,
	}
	m := newStatusModelWithBackend(t, backend)
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 40})
	m.op = operation{kind: opTurn, gen: 7, events: make(chan agent.Event)}
	m.chatPhase = chat.PhaseStreaming

	_, command := m.dispatchCommand("status", "")
	runStatusLoad(t, m, command)
	_, _ = m.Update(turnEventMsg{generation: 7, ev: agent.Event{Kind: agent.EventUsage}})
	if got := m.statusRuntime().AuthenticationState; got != "authentication failed" {
		t.Fatalf("same-turn event changed authentication state to %q", got)
	}

	m.op.gen = 8
	_, _ = m.Update(turnEventMsg{generation: 8, ev: agent.Event{Kind: agent.EventUsage}})
	if got := m.statusRuntime().AuthenticationState; got != "authenticated" {
		t.Fatalf("newer-turn success left authentication state at %q", got)
	}
}

type sequencedStatusIdentityBackend struct {
	*localStatusBackend
	calls        atomic.Int32
	firstStarted chan struct{}
	firstRelease chan struct{}
	firstDone    chan bool
}

func (b *sequencedStatusIdentityBackend) CurrentUser(ctx context.Context) (assistant.CurrentUser, error) {
	if b.calls.Add(1) != 1 {
		return assistant.CurrentUser{Name: "New User", Organization: "New Org"}, nil
	}
	close(b.firstStarted)
	<-b.firstRelease
	b.firstDone <- errors.Is(ctx.Err(), context.Canceled)
	return assistant.CurrentUser{Name: "Old User", Organization: "Old Org"}, nil
}

func TestStatusReopenCancelsAndIgnoresStaleIdentity(t *testing.T) {
	backend := &sequencedStatusIdentityBackend{
		localStatusBackend: &localStatusBackend{t: t},
		firstStarted:       make(chan struct{}),
		firstRelease:       make(chan struct{}),
		firstDone:          make(chan bool, 1),
	}
	m := newStatusModelWithBackend(t, backend)
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 40})

	_, firstLoad := m.dispatchCommand("status", "")
	firstBatch := statusBatch(t, firstLoad)
	firstResult := make(chan tea.Msg, 1)
	go func() { firstResult <- firstBatch[1]() }()
	select {
	case <-backend.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first identity request did not start")
	}

	m.closeStatus()
	_, secondLoad := m.dispatchCommand("status", "")
	secondBatch := statusBatch(t, secondLoad)
	_, _ = m.Update(secondBatch[1]())
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "New User · New Org") {
		t.Fatalf("reopened status did not show fresh identity:\n%s", view)
	}

	close(backend.firstRelease)
	select {
	case message := <-firstResult:
		_, _ = m.Update(message)
	case <-time.After(time.Second):
		t.Fatal("first identity request did not finish")
	}
	if canceled := <-backend.firstDone; !canceled {
		t.Fatal("closing status did not cancel the previous identity request")
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "New User · New Org") || strings.Contains(view, "Old User · Old Org") {
		t.Fatalf("stale identity replaced the fresh result:\n%s", view)
	}
}

func TestStatusReopenIgnoresStaleCloseMessage(t *testing.T) {
	m := newStatusModel(t)
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 40})
	defer m.closeStatus()

	_, _ = m.dispatchCommand("status", "")
	firstClose := m.updateStatus(tea.KeyPressMsg{Code: tea.KeyEscape})
	staleClose := m.updateStatus(tea.KeyPressMsg{Code: tea.KeyEscape})
	if firstClose == nil || staleClose == nil {
		t.Fatal("status Escape did not schedule close messages")
	}

	_, _ = m.Update(firstClose())
	_, _ = m.dispatchCommand("status", "")
	_, _ = m.Update(staleClose())
	if m.mode != ModeStatus {
		t.Fatal("stale close message closed the reopened status panel")
	}
}

func TestCtrlCCancelsStatusCollectionAndQuits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX executable script")
	}
	bin := t.TempDir()
	started := filepath.Join(t.TempDir(), "started")
	gitPath := filepath.Join(bin, "git")
	script := "#!/bin/sh\n: > \"$BITS_STATUS_GIT_STARTED\"\nexec sleep 30\n"
	if err := os.WriteFile(gitPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BITS_STATUS_GIT_STARTED", started)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := newStatusModel(t)
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 40})

	_, load := m.dispatchCommand("status", "")
	batch := statusBatch(t, load)
	workspaceDone := make(chan struct{})
	go func() {
		_ = batch[0]()
		close(workspaceDone)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("status collection did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	_, quit := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if quit == nil {
		t.Fatal("ctrl+c did not return a quit command")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c command = %T, want tea.QuitMsg", quit())
	}
	select {
	case <-workspaceDone:
	case <-time.After(time.Second):
		t.Fatal("ctrl+c did not cancel status collection")
	}
	if m.statusTask.running() {
		t.Fatal("ctrl+c retained status cancellation handle")
	}
}

func runStatusGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-c", "commit.gpgsign=false"}, args...)
	command := exec.CommandContext(t.Context(), "git", commandArgs...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
