package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/agent/fake"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	loginui "github.com/DataDog/bits-cli/internal/tui/login"
)

func TestStartupLoginTransitionsToChatInSameRootModel(t *testing.T) {
	loginModel := loginui.New(context.Background(), nil, "")
	factoryCalls := 0
	root := NewWithLogin(context.Background(), loginModel, func(context.Context) (*agent.Engine, error) {
		factoryCalls++
		return agent.New(fake.New(), assistant.SendOptions{}), nil
	})

	_, _ = root.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_, _ = root.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#FFFFFF")})
	loginView := root.View()
	if !loginView.AltScreen || loginView.MouseMode != tea.MouseModeNone {
		t.Fatalf("login view alt=%t mouse=%v", loginView.AltScreen, loginView.MouseMode)
	}

	model, cmd := root.Update(loginui.CompletedMsg{})
	if model != root || cmd == nil || factoryCalls != 0 {
		t.Fatalf("completion model=%p root=%p cmd=%v calls=%d", model, root, cmd != nil, factoryCalls)
	}
	model, initCmd := root.Update(cmd())
	if model != root || initCmd == nil {
		t.Fatal("engine result did not retain root and initialize chat")
	}
	if factoryCalls != 1 || root.mode != ModeChat || root.engine == nil {
		t.Fatalf("calls=%d mode=%v engine=%v", factoryCalls, root.mode, root.engine != nil)
	}
	if root.width != 80 || root.height != 24 || root.list.Width() != 80 {
		t.Fatalf("cached layout width=%d height=%d list=%d", root.width, root.height, root.list.Width())
	}
	if root.styles.IsDark {
		t.Fatal("light terminal theme was lost during handoff")
	}
	chatView := root.View()
	if !chatView.AltScreen || chatView.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("chat view alt=%t mouse=%v", chatView.AltScreen, chatView.MouseMode)
	}

	_, _ = root.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if got := root.editor.Value(); got != "x" {
		t.Fatalf("editor value = %q, want focused input after handoff", got)
	}
}

func TestStartupHandoffKeepsOneAltScreenSession(t *testing.T) {
	var output bytes.Buffer
	factoryCalled := make(chan struct{})
	root := NewWithLogin(context.Background(), loginui.New(context.Background(), nil, ""), func(context.Context) (*agent.Engine, error) {
		close(factoryCalled)
		return agent.New(fake.New(), assistant.SendOptions{}), nil
	})
	program := tea.NewProgram(root,
		tea.WithInput(bytes.NewReader(nil)),
		tea.WithOutput(&output),
		tea.WithWindowSize(80, 24),
		tea.WithEnvironment([]string{"TERM=xterm-256color"}),
	)
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	program.Send(loginui.CompletedMsg{})
	select {
	case <-factoryCalled:
	case <-time.After(time.Second):
		program.Kill()
		t.Fatal("engine factory was not called")
	}
	// Let the engine-ready message render chat before the deliberate final exit.
	time.Sleep(50 * time.Millisecond)
	program.Quit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	terminalOutput := output.String()
	if got := strings.Count(terminalOutput, ansi.SetModeAltScreenSaveCursor); got != 1 {
		t.Errorf("alternate-screen enters = %d, want 1", got)
	}
	if got := strings.Count(terminalOutput, ansi.ResetModeAltScreenSaveCursor); got != 1 {
		t.Errorf("alternate-screen exits = %d, want one final teardown", got)
	}
}

func TestStartupLoginCompletionIsSingleFlight(t *testing.T) {
	factoryCalls := 0
	root := NewWithLogin(context.Background(), loginui.New(context.Background(), nil, ""), func(context.Context) (*agent.Engine, error) {
		factoryCalls++
		return agent.New(fake.New(), assistant.SendOptions{}), nil
	})
	_, first := root.Update(loginui.CompletedMsg{})
	_, duplicate := root.Update(loginui.CompletedMsg{})
	if first == nil || duplicate != nil {
		t.Fatalf("first=%v duplicate=%v", first != nil, duplicate != nil)
	}
	_, _ = root.Update(first())
	if factoryCalls != 1 || root.mode != ModeChat {
		t.Fatalf("factory calls=%d mode=%v", factoryCalls, root.mode)
	}
}

func TestStartupLoginFactoryErrorQuitsWithStoredError(t *testing.T) {
	want := errors.New("load persisted login")
	root := NewWithLogin(context.Background(), loginui.New(context.Background(), nil, ""), func(context.Context) (*agent.Engine, error) {
		return nil, want
	})
	_, cmd := root.Update(loginui.CompletedMsg{})
	_, quit := root.Update(cmd())
	if !errors.Is(root.StartupError(), want) {
		t.Fatalf("startup error = %v", root.StartupError())
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("factory failure command = %T, want tea.QuitMsg", quit())
	}
}

func TestStartupLoginCancellationRemainsProgramExit(t *testing.T) {
	factoryCalls := 0
	root := NewWithLogin(context.Background(), loginui.New(context.Background(), nil, ""), func(context.Context) (*agent.Engine, error) {
		factoryCalls++
		return nil, nil
	})
	_, quit := root.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !errors.Is(root.StartupError(), loginui.ErrCanceled) || factoryCalls != 0 {
		t.Fatalf("startup error=%v factory calls=%d", root.StartupError(), factoryCalls)
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("cancel command = %T, want tea.QuitMsg", quit())
	}
}

func TestStartupLoginInitializesConversationRestore(t *testing.T) {
	root := NewWithLogin(context.Background(), loginui.New(context.Background(), nil, ""), func(context.Context) (*agent.Engine, error) {
		return agent.New(fake.New(), assistant.SendOptions{ConversationID: "conversation-1"}), nil
	})
	_, cmd := root.Update(loginui.CompletedMsg{})
	_, initCmd := root.Update(cmd())
	if initCmd == nil || root.mode != ModeChat || root.chatPhase != chat.PhaseLoading {
		t.Fatalf("init=%v mode=%v phase=%v", initCmd != nil, root.mode, root.chatPhase)
	}
	if root.ConversationID() != "conversation-1" {
		t.Fatalf("conversation id = %q", root.ConversationID())
	}
}
