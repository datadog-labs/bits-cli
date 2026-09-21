package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	clearMultiplexerEnv(t)
	loginModel := loginui.New(context.Background(), nil)
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
	if !chatView.AltScreen || chatView.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("chat view alt=%t mouse=%v", chatView.AltScreen, chatView.MouseMode)
	}

	_, _ = root.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if got := root.editor.Value(); got != "x" {
		t.Fatalf("editor value = %q, want focused input after handoff", got)
	}
}

func TestStartupHandoffKeepsOneAltScreenSession(t *testing.T) {
	output := &synchronizedBuffer{}
	loginModel := loginui.New(context.Background(), func(context.Context, string, func(loginui.BrowserStatus)) error {
		return nil
	})
	root := NewWithLogin(context.Background(), loginModel, func(context.Context) (*agent.Engine, error) {
		return agent.New(fake.New(), assistant.SendOptions{}), nil
	})
	program := tea.NewProgram(root,
		tea.WithInput(bytes.NewReader(nil)),
		tea.WithOutput(output),
		tea.WithWindowSize(80, 24),
		tea.WithEnvironment([]string{"TERM=xterm-256color"}),
	)
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	t.Cleanup(program.Kill)

	waitForOutput(t, output, "Choose your Datadog site")
	program.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitForOutput(t, output, "Authentication complete")
	waitForOutput(t, output, "Ask Bits…")
	program.Quit()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		program.Kill()
		t.Fatal("program did not stop")
	}

	terminalOutput := output.String()
	if got := strings.Count(terminalOutput, ansi.SetModeAltScreenSaveCursor); got != 1 {
		t.Errorf("alternate-screen enters = %d, want 1", got)
	}
	if got := strings.Count(terminalOutput, ansi.ResetModeAltScreenSaveCursor); got != 1 {
		t.Errorf("alternate-screen exits = %d, want one final teardown", got)
	}
	plain := ansi.Strip(terminalOutput)
	loginAt := strings.Index(plain, "Choose your Datadog site")
	completeAt := strings.Index(plain, "Authentication complete")
	chatAt := strings.Index(plain, "Ask Bits…")
	enterAt := strings.Index(terminalOutput, ansi.SetModeAltScreenSaveCursor)
	rawLoginAt := strings.Index(terminalOutput, "Choose your Datadog site")
	rawChatAt := strings.Index(terminalOutput, "Ask Bits…")
	exitAt := strings.Index(terminalOutput, ansi.ResetModeAltScreenSaveCursor)
	if loginAt < 0 || completeAt <= loginAt || chatAt <= completeAt ||
		enterAt < 0 || rawLoginAt <= enterAt || rawChatAt <= rawLoginAt || exitAt <= rawChatAt {
		t.Fatalf("render order login=%d complete=%d chat=%d enter=%d raw-login=%d raw-chat=%d exit=%d",
			loginAt, completeAt, chatAt, enterAt, rawLoginAt, rawChatAt, exitAt)
	}
}

// synchronizedBuffer permits deterministic observation while Bubble Tea's
// renderer is still writing from its own goroutine.
type synchronizedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func waitForOutput(t *testing.T, output *synchronizedBuffer, text string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(ansi.Strip(output.String()), text) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("terminal output did not contain %q", text)
}

func TestStartupHandoffTinyTerminalShowsBoundedResizePrompt(t *testing.T) {
	for _, size := range []struct{ width, height int }{{10, 2}, {24, 4}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			root := NewWithLogin(context.Background(), loginui.New(context.Background(), nil), func(context.Context) (*agent.Engine, error) {
				return agent.New(fake.New(), assistant.SendOptions{}), nil
			})
			_, _ = root.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
			_, factory := root.Update(loginui.CompletedMsg{})
			_, _ = root.Update(factory())
			view := root.View().Content
			if !strings.Contains(ansi.Strip(view), "Resize") {
				t.Fatalf("view = %q, want resize prompt", ansi.Strip(view))
			}
			lines := strings.Split(view, "\n")
			if len(lines) > size.height {
				t.Fatalf("height = %d, want <= %d", len(lines), size.height)
			}
			for _, line := range lines {
				if width := ansi.StringWidth(line); width > size.width {
					t.Fatalf("line width = %d, want <= %d", width, size.width)
				}
			}
		})
	}
}

func TestStartupLoginCompletionIsSingleFlight(t *testing.T) {
	factoryCalls := 0
	root := NewWithLogin(context.Background(), loginui.New(context.Background(), nil), func(context.Context) (*agent.Engine, error) {
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
	root := NewWithLogin(context.Background(), loginui.New(context.Background(), nil), func(context.Context) (*agent.Engine, error) {
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

func TestControlCAfterCompletedLoginInvalidatesPendingFactory(t *testing.T) {
	factoryStarted := make(chan struct{})
	factoryCanceled := make(chan struct{})
	loginModel := loginui.New(context.Background(), func(context.Context, string, func(loginui.BrowserStatus)) error {
		return nil
	})
	root := NewWithLogin(context.Background(), loginModel, func(ctx context.Context) (*agent.Engine, error) {
		close(factoryStarted)
		<-ctx.Done()
		close(factoryCanceled)
		return nil, ctx.Err()
	})
	_, loginCmd := root.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, child := range loginCmd().(tea.BatchMsg) {
		_, _ = root.Update(child())
	}
	if !loginModel.Completed() {
		t.Fatal("test login did not reach persisted completion")
	}
	_, factoryCmd := root.Update(loginui.CompletedMsg{})
	factoryResult := make(chan tea.Msg, 1)
	go func() { factoryResult <- factoryCmd() }()
	<-factoryStarted

	_, quit := root.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("cancel command = %T, want tea.QuitMsg", quit())
	}
	select {
	case <-factoryCanceled:
	case <-time.After(time.Second):
		t.Fatal("pending engine factory context was not canceled")
	}
	_, _ = root.Update(<-factoryResult)
	if root.mode != ModeLogin || root.engine != nil || root.StartupError() != nil || !root.startupStopping {
		t.Fatalf("mode=%v engine=%v startup error=%v stopping=%t",
			root.mode, root.engine != nil, root.StartupError(), root.startupStopping)
	}
}

func TestStartupLoginCancellationRemainsProgramExit(t *testing.T) {
	factoryCalls := 0
	root := NewWithLogin(context.Background(), loginui.New(context.Background(), nil), func(context.Context) (*agent.Engine, error) {
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
	root := NewWithLogin(context.Background(), loginui.New(context.Background(), nil), func(context.Context) (*agent.Engine, error) {
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
