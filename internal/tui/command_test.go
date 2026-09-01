package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	tuieditor "github.com/DataDog/bits-cli/internal/tui/editor"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantOk   bool
	}{
		{"/quit", "quit", true},
		{"/exit", "exit", true},
		{"/Quit", "quit", true},
		{"/QUIT", "quit", true},
		{"/quit now", "quit", true}, // trailing args ignored
		{"hello", "", false},
		{"", "", false},
		{"/", "", false},      // bare slash is not a command
		{"/ help", "", false}, // space before the name is not a command
	}
	for _, tc := range cases {
		got, ok := parseCommand(tc.in)
		if got != tc.wantName || ok != tc.wantOk {
			t.Errorf("parseCommand(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.wantName, tc.wantOk)
		}
	}
}

func TestLookupCommandResolvesExitAlias(t *testing.T) {
	quit, ok := lookupCommand("quit")
	if !ok {
		t.Fatal("/quit was not registered")
	}
	exit, ok := lookupCommand("exit")
	if !ok {
		t.Fatal("/exit alias was not registered")
	}
	if exit.id != quit.id || exit.activeTurnPolicy != commandCancelsTurn {
		t.Fatalf("/exit resolved to %#v, want /quit with cancel policy", exit)
	}
}

func TestLookupCommandResolvesClearAsExactNewAlias(t *testing.T) {
	newCommand, ok := lookupCommand("new")
	if !ok {
		t.Fatal("/new was not registered")
	}
	clearCommand, ok := lookupCommand("clear")
	if !ok {
		t.Fatal("/clear alias was not registered")
	}
	if clearCommand.id != newCommand.id || clearCommand.activeTurnPolicy != newCommand.activeTurnPolicy {
		t.Fatalf("/clear resolved to %#v, want the /new implementation %#v", clearCommand, newCommand)
	}
}

func TestRegisteredCommandsAndCompletionAliasesStayConsistent(t *testing.T) {
	for _, definition := range commandDefinitions {
		for _, query := range append([]string{definition.name}, definition.aliases...) {
			candidates := tuieditor.FakeCommands(query)
			found := false
			for _, candidate := range candidates {
				if candidate.Insert == "/"+definition.name {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("completion query %q does not resolve registered command /%s", query, definition.name)
			}
		}
	}
}

func TestResumeIsRegisteredWithRejectActivePolicy(t *testing.T) {
	resume, ok := lookupCommand("resume")
	if !ok || resume.id != commandResume || resume.activeTurnPolicy != commandRejectedDuringTurn {
		t.Fatalf("resume definition = %#v, registered=%v", resume, ok)
	}
}

func TestStatusIsRegisteredWithAllowActivePolicy(t *testing.T) {
	status, ok := lookupCommand("status")
	if !ok || status.id != commandStatus || status.activeTurnPolicy != commandAllowedDuringTurn {
		t.Fatalf("status definition = %#v, registered=%v", status, ok)
	}
}

func TestExactNewCommandsExecuteOnFirstEnterWithCompletionOpen(t *testing.T) {
	for _, input := range []string{"/new", "/clear"} {
		t.Run(input, func(t *testing.T) {
			m := newModelWithSpy(t)
			m.convID = "conversation-before-reset"
			m.editor.Update(tea.PasteMsg{Content: input})
			if !m.editor.MenuOpen() {
				t.Fatal("expected exact command completion menu to be open")
			}

			_, _ = m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			if m.convID != "" || m.editor.Value() != "" {
				t.Fatalf("first Enter did not reset conversation: conv=%q editor=%q", m.convID, m.editor.Value())
			}
		})
	}
}

func TestPartialSlashAndFileCompletionsRemainEditorOwned(t *testing.T) {
	for _, test := range []struct {
		name      string
		input     string
		wantValue string
	}{
		{name: "partial slash command", input: "/n", wantValue: "/new "},
		{name: "file", input: "@README", wantValue: "@README.md "},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := newModelWithSpy(t)
			m.convID = "conversation-preserved"
			m.editor.Update(tea.PasteMsg{Content: test.input})
			if !m.editor.MenuOpen() {
				t.Fatal("expected completion menu to be open")
			}

			_, command := m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			if command != nil || m.convID != "conversation-preserved" || m.editor.Value() != test.wantValue {
				t.Fatalf("completion routing: command=%v conv=%q editor=%q, want nil/preserved/%q", command != nil, m.convID, m.editor.Value(), test.wantValue)
			}
		})
	}
}

func TestWebIsRegisteredAndAllowedDuringTurn(t *testing.T) {
	web, ok := lookupCommand("web")
	if !ok || web.id != commandWeb || web.activeTurnPolicy != commandAllowedDuringTurn {
		t.Fatalf("web definition = %#v, registered=%v", web, ok)
	}
}

func TestSubmitWebOpensCurrentConversationWithoutSending(t *testing.T) {
	var opened string
	backend := &spyBackend{t: t, site: "https://api.us3.datadoghq.com"}
	m := New(agent.New(backend, assistant.SendOptions{ConversationID: "conversation-1"}), Config{
		OpenURL: func(_ context.Context, target string) error {
			opened = target
			return nil
		},
	})
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "/web"})

	_, cmd := m.submit()
	if cmd == nil {
		t.Fatal("/web returned no launcher command")
	}
	msg := cmd()
	_, _ = m.Update(msg)
	want := "https://us3.datadoghq.com/ask/conversation-1"
	if opened != want {
		t.Fatalf("opened URL = %q, want %q", opened, want)
	}
	if !strings.Contains(m.notice.Text, want) || m.notice.Level != chat.NoticeInfo {
		t.Fatalf("success notice = %#v", m.notice)
	}
	if m.ConversationID() != "conversation-1" {
		t.Fatalf("conversation ID changed to %q", m.ConversationID())
	}
	if m.editor.Value() != "" {
		t.Fatalf("editor still contains %q", m.editor.Value())
	}
}

func TestSubmitWebWithoutConversationDoesNotLaunchOrSend(t *testing.T) {
	launched := false
	m := New(agent.New(&spyBackend{t: t, site: "https://api.datadoghq.com"}, assistant.SendOptions{}), Config{
		OpenURL: func(context.Context, string) error {
			launched = true
			return nil
		},
	})
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "/web"})

	_, cmd := m.submit()
	if cmd == nil || launched {
		t.Fatalf("missing-conversation result: cmd=%v launched=%v", cmd != nil, launched)
	}
	if m.notice.Level != chat.NoticeWarn || !strings.Contains(m.notice.Text, "Start a conversation") {
		t.Fatalf("missing-conversation notice = %#v", m.notice)
	}
}

func TestSubmitWebUnsupportedSiteDoesNotLaunchOrSend(t *testing.T) {
	launched := false
	m := New(agent.New(&spyBackend{t: t, site: "https://api.ddog-gov.com"}, assistant.SendOptions{ConversationID: "conversation-1"}), Config{
		OpenURL: func(context.Context, string) error {
			launched = true
			return nil
		},
	})
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "/web"})

	_, cmd := m.submit()
	if cmd == nil || launched {
		t.Fatalf("unsupported-site result: cmd=%v launched=%v", cmd != nil, launched)
	}
	if m.notice.Level != chat.NoticeError || !strings.Contains(m.notice.Text, "Could not build a web link") || m.notice.Err == nil {
		t.Fatalf("unsupported-site notice = %#v", m.notice)
	}
}

func TestSubmitWebLauncherFailureProvidesManualURL(t *testing.T) {
	launchErr := errors.New("no graphical browser is available")
	m := New(agent.New(&spyBackend{t: t, site: "https://api.datadoghq.com"}, assistant.SendOptions{ConversationID: "conversation-1"}), Config{
		OpenURL: func(context.Context, string) error { return launchErr },
	})
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "/web"})

	_, cmd := m.submit()
	msg := cmd()
	_, _ = m.Update(msg)
	if m.notice.Level != chat.NoticeError || !errors.Is(m.notice.Err, launchErr) {
		t.Fatalf("launcher-failure notice = %#v", m.notice)
	}
	for _, want := range []string{"Could not open a browser", "https://app.datadoghq.com/ask/conversation-1"} {
		if !strings.Contains(m.notice.Text, want) {
			t.Fatalf("launcher-failure notice %q does not contain %q", m.notice.Text, want)
		}
	}
}

func TestSubmitWebDuringActiveTurnDoesNotCancel(t *testing.T) {
	backend := &spyBackend{t: t, site: "https://api.datadoghq.com"}
	m := New(agent.New(backend, assistant.SendOptions{ConversationID: "conversation-1"}), Config{
		OpenURL: func(context.Context, string) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.cancelTurn = cancel
	m.turnEvents = make(chan agent.Event)
	m.chatPhase = chat.PhaseStreaming
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "/web"})

	_, cmd := m.submit()
	if cmd == nil {
		t.Fatal("active-turn /web returned no launcher command")
	}
	if ctx.Err() != nil {
		t.Fatal("/web cancelled the active turn")
	}
}

func TestSubmitResumeNeverCallsSend(t *testing.T) {
	m := newModelWithSpy(t)
	m.editor.Update(tea.PasteMsg{Content: "/resume"})
	_, cmd := m.submit()
	if cmd == nil || m.mode != ModeConversations || m.picker == nil {
		t.Fatalf("resume state: cmd=%v mode=%v picker=%v", cmd != nil, m.mode, m.picker != nil)
	}
	msg := cmd()
	_, _ = m.Update(msg)
	if m.picker == nil || m.picker.State() == 0 {
		t.Fatal("unsupported list backend did not leave loading state")
	}
}

func TestSubmitResumeDuringTurnIsRejectedWithoutCancellation(t *testing.T) {
	m := newModelWithSpy(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.cancelTurn = cancel
	m.turnEvents = make(chan agent.Event)
	m.chatPhase = chat.PhaseStreaming
	m.editor.Update(tea.PasteMsg{Content: "/resume"})
	_, cmd := m.submit()
	if cmd == nil || m.notice.Empty() {
		t.Fatal("expected active-turn rejection notice")
	}
	if ctx.Err() != nil || m.mode == ModeConversations {
		t.Fatal("resume cancelled the turn or opened the picker")
	}
}

func TestSubmitResumeDuringStartupHistoryLoadIsRejected(t *testing.T) {
	m := newModelWithSpy(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.cancelTurn = cancel
	m.turnEvents = make(chan agent.Event)
	m.chatPhase = chat.PhaseLoading
	m.editor.Update(tea.PasteMsg{Content: "/resume"})
	_, cmd := m.submit()
	if cmd == nil || m.notice.Empty() {
		t.Fatal("expected history-load rejection notice")
	}
	if ctx.Err() != nil || m.mode == ModeConversations {
		t.Fatal("resume cancelled startup restore or opened picker")
	}
}

func TestDispatchQuitCancelsRunningTurnAndQuits(t *testing.T) {
	m := &Model{}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel

	got, cmd := m.dispatchCommand("quit")
	if got != m {
		t.Fatal("dispatchCommand should return the same model")
	}
	if cmd == nil {
		t.Fatal("expected a quit command, got nil")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
	if ctx.Err() == nil {
		t.Fatal("running turn was not cancelled before quitting")
	}
}

func TestDispatchQuitWithNoRunningTurnStillQuits(t *testing.T) {
	m := &Model{}
	_, cmd := m.dispatchCommand("quit")
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
}

func TestSubmitQuitAndExitDuringActiveTurnCancelAndQuit(t *testing.T) {
	for _, input := range []string{"/quit", "/exit"} {
		t.Run(input, func(t *testing.T) {
			m := newModelWithSpy(t)
			ctx, cancel := context.WithCancel(context.Background())
			m.cancelTurn = cancel
			m.turnEvents = make(chan agent.Event)
			m.chatPhase = chat.PhaseStreaming
			m.editor.Update(tea.PasteMsg{Content: input})

			got, cmd := m.submit()
			if got != m {
				t.Fatal("submit should return the same model")
			}
			if cmd == nil {
				t.Fatal("expected a quit command")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
			}
			if ctx.Err() == nil {
				t.Fatal("active turn was not cancelled before quitting")
			}
			if got := m.editor.Value(); got != "" {
				t.Fatalf("editor value = %q, want empty after command submission", got)
			}
		})
	}
}

func TestSubmitQuitWhenIdleQuitsWithoutBackendCall(t *testing.T) {
	// Idle (no active turn, not loading): a slash command must dispatch locally
	// and never reach the engine. The spy backend turns any StartTurn call into
	// a test failure, so this encodes the "no backend request" AC explicitly.
	for _, input := range []string{"/quit", "/exit"} {
		t.Run(input, func(t *testing.T) {
			m := newModelWithSpy(t)
			m.editor.Update(tea.PasteMsg{Content: input})

			_, cmd := m.submit()
			if cmd == nil {
				t.Fatal("expected a quit command")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
			}
			if got := m.editor.Value(); got != "" {
				t.Fatalf("editor value = %q, want empty after command submission", got)
			}
		})
	}
}

func TestSubmitOrdinaryInputDuringActiveTurnRemainsPending(t *testing.T) {
	m := newModelWithSpy(t)
	m.turnEvents = make(chan agent.Event)
	m.chatPhase = chat.PhaseStreaming
	m.editor.Update(tea.PasteMsg{Content: "send this later"})

	_, cmd := m.submit()
	if cmd != nil {
		t.Fatal("ordinary input during an active turn returned a command")
	}
	if got := m.editor.Value(); got != "send this later" {
		t.Fatalf("editor value = %q, want busy input to remain pending", got)
	}
}

func TestSubmitExitDuringHistoryLoadingCancelsAndQuits(t *testing.T) {
	m := newModelWithSpy(t)
	m.chatPhase = chat.PhaseLoading
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	m.editor.Update(tea.PasteMsg{Content: "/exit"})

	_, cmd := m.submit()
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
	if ctx.Err() == nil {
		t.Fatal("history restore was not cancelled before quitting")
	}
}

func TestSubmitUnknownCommandDuringActiveTurnDoesNotCancel(t *testing.T) {
	m := newModelWithSpy(t)
	m.chatPhase = chat.PhaseStreaming
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	m.turnEvents = make(chan agent.Event)
	m.editor.Update(tea.PasteMsg{Content: "/nope"})

	_, cmd := m.submit()
	if cmd == nil || m.notice.Empty() {
		t.Fatal("expected an unknown-command notice")
	}
	if ctx.Err() != nil {
		t.Fatal("unknown command cancelled the active turn")
	}
}

func TestDispatchUnknownCommandPostsNotice(t *testing.T) {
	m := &Model{}
	_, cmd := m.dispatchCommand("nope")
	if cmd == nil {
		t.Fatal("expected a notice clear-tick command")
	}
	// showNotice sets the notice synchronously; the returned cmd only clears it.
	if m.notice.Empty() {
		t.Fatal("expected an unknown-command notice on the model")
	}
	if m.notice.Level != chat.NoticeError {
		t.Errorf("notice level = %v, want NoticeError", m.notice.Level)
	}
}
