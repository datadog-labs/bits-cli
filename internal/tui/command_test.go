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
		wantArg  string
		wantOk   bool
	}{
		{in: "/quit", wantName: "quit", wantOk: true},
		{in: "/exit", wantName: "exit", wantOk: true},
		{in: "/Quit", wantName: "quit", wantOk: true},
		{in: "/QUIT", wantName: "quit", wantOk: true},
		{in: "/quit now", wantName: "quit", wantArg: "now", wantOk: true}, // only /permissions consumes its argument
		{in: "/permissions", wantName: "permissions", wantOk: true},
		{in: "/permissions skip-permissions", wantName: "permissions", wantArg: "skip-permissions", wantOk: true},
		{in: "/Permissions MANUAL", wantName: "permissions", wantArg: "manual", wantOk: true},
		{in: "/permissions   manual", wantName: "permissions", wantArg: "manual", wantOk: true},
		{in: "/Permissions   SKIP-PERMISSIONS extra", wantName: "permissions", wantArg: "skip-permissions extra", wantOk: true},
		{in: "/permissions manual now extra", wantName: "permissions", wantArg: "manual now extra", wantOk: true},
		{in: "hello", wantOk: false},
		{in: "", wantOk: false},
		{in: "/", wantOk: false},      // bare slash is not a command
		{in: "/ help", wantOk: false}, // space before the name is not a command
		{in: "hello /quit", wantOk: false},
		{in: " /quit", wantOk: false}, // commands must start the prompt
	}
	for _, tc := range cases {
		gotName, gotArg, ok := parseCommand(tc.in)
		if ok != tc.wantOk || (ok && (gotName != tc.wantName || gotArg != tc.wantArg)) {
			t.Errorf("parseCommand(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.in, gotName, gotArg, ok, tc.wantName, tc.wantArg, tc.wantOk)
		}
	}
}

func TestEnterOnLeadingCommandCompletionDispatchesImmediately(t *testing.T) {
	m := newModelWithSpy(t)
	m.editor.Update(tea.PasteMsg{Content: "/q"})
	if !m.editor.MenuOpen() {
		t.Fatal("leading slash command should open the completion menu")
	}

	_, cmd := m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a slash-command completion should dispatch it")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("command result = %T, want tea.QuitMsg", cmd())
	}
	if got := m.editor.Value(); got != "" {
		t.Fatalf("editor value = %q, want empty after command dispatch", got)
	}
}

func TestSlashCompletionDoesNotOpenMidPrompt(t *testing.T) {
	m := newModelWithSpy(t)
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	m.chatPhase = chat.PhaseStreaming
	m.editor.Update(tea.PasteMsg{Content: "explain /new"})
	if m.editor.MenuOpen() {
		t.Fatal("slash completion should not open outside the first prompt token")
	}

	_, cmd := m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("mid-prompt slash should remain ordinary input while a turn is active")
	}
	if got := m.editor.Value(); got != "explain /new" {
		t.Fatalf("editor value = %q, want ordinary input to remain pending", got)
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

func TestCommandCompletionMatchesRegistry(t *testing.T) {
	specs := commandCompletionSpecs()
	if len(specs) != len(commandDefinitions) {
		t.Fatalf("completion has %d entries, registry has %d", len(specs), len(commandDefinitions))
	}
	for _, candidate := range tuieditor.CommandCandidatesFrom(specs, "") {
		if _, ok := lookupCommand(candidate.ID); !ok {
			t.Fatalf("completion advertises unregistered command %q", candidate.ID)
		}
		if candidate.ID == "help" {
			t.Fatal("/help remains in completion")
		}
	}
	m := newModelWithSpy(t)
	_, _ = m.dispatchCommand("help", "")
	if !strings.Contains(latestNotice(m).Text, "Unknown command: /help") {
		t.Fatalf("/help notice = %q", latestNotice(m).Text)
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

func TestFileCompletionRemainsEditorOwned(t *testing.T) {
	m := newModelWithSpy(t)
	m.convID = "conversation-preserved"
	m.editor.Update(tea.PasteMsg{Content: "@README"})
	m.editor.SetFileResults("README", tuieditor.FileReady, []tuieditor.Candidate{{
		Kind: tuieditor.CandidateFile, ID: "README.md", Label: "+ README.md", Insert: "@README.md",
	}})
	if !m.editor.MenuOpen() {
		t.Fatal("expected completion menu to be open")
	}

	_, command := m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || m.convID != "conversation-preserved" || m.editor.Value() != "@README.md " {
		t.Fatalf("completion routing: command=%v conv=%q editor=%q, want nil/preserved/@README.md ", command != nil, m.convID, m.editor.Value())
	}
}

func TestWebIsRegisteredAndAllowedDuringTurn(t *testing.T) {
	web, ok := lookupCommand("web")
	if !ok || web.id != commandWeb || web.activeTurnPolicy != commandAllowedDuringTurn {
		t.Fatalf("web definition = %#v, registered=%v", web, ok)
	}
}

func TestSettingsIsRegisteredAndAllowedDuringTurn(t *testing.T) {
	settings, ok := lookupCommand("settings")
	if !ok || settings.id != commandSettings || settings.activeTurnPolicy != commandAllowedDuringTurn {
		t.Fatalf("settings definition = %#v, registered=%v", settings, ok)
	}
}

func TestSubmitSettingsOpensAssistantSettingsWithoutSending(t *testing.T) {
	var opened string
	m := New(agent.New(&spyBackend{t: t, site: "https://api.us3.datadoghq.com"}, assistant.SendOptions{}), Config{
		OpenURL: func(_ context.Context, target string) error {
			opened = target
			return nil
		},
	})
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "/settings"})

	_, cmd := m.submit()
	if cmd == nil {
		t.Fatal("/settings returned no launcher command")
	}
	msg := cmd()
	_, _ = m.Update(msg)
	want := "https://us3.datadoghq.com/ask/settings"
	if opened != want {
		t.Fatalf("opened URL = %q, want %q", opened, want)
	}
	if !strings.Contains(latestNotice(m).Text, want) || latestNotice(m).Level != chat.NoticeInfo {
		t.Fatalf("success notice = %#v", latestNotice(m))
	}
	if m.editor.Value() != "" {
		t.Fatalf("editor still contains %q", m.editor.Value())
	}
}

func TestSubmitSettingsUnsupportedSiteDoesNotLaunchOrSend(t *testing.T) {
	launched := false
	m := New(agent.New(&spyBackend{t: t, site: "https://api.ddog-gov.com"}, assistant.SendOptions{}), Config{
		OpenURL: func(context.Context, string) error {
			launched = true
			return nil
		},
	})
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "/settings"})

	_, _ = m.submit()
	if launched {
		t.Fatalf("unsupported-site result: launched=%v", launched)
	}
	if latestNotice(m).Level != chat.NoticeError || !strings.Contains(latestNotice(m).Text, "Could not build a web link") || latestNotice(m).Err == nil {
		t.Fatalf("unsupported-site notice = %#v", latestNotice(m))
	}
}

func TestSubmitSettingsLauncherFailureProvidesManualURL(t *testing.T) {
	launchErr := errors.New("no graphical browser is available")
	m := New(agent.New(&spyBackend{t: t, site: "https://api.datadoghq.com"}, assistant.SendOptions{}), Config{
		OpenURL: func(context.Context, string) error { return launchErr },
	})
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "/settings"})

	_, cmd := m.submit()
	msg := cmd()
	_, _ = m.Update(msg)
	if latestNotice(m).Level != chat.NoticeError || !errors.Is(latestNotice(m).Err, launchErr) {
		t.Fatalf("launcher-failure notice = %#v", latestNotice(m))
	}
	for _, want := range []string{"Could not open a browser", "https://app.datadoghq.com/ask/settings"} {
		if !strings.Contains(latestNotice(m).Text, want) {
			t.Fatalf("launcher-failure notice %q does not contain %q", latestNotice(m).Text, want)
		}
	}
}

func TestSubmitSettingsDuringActiveTurnDoesNotCancel(t *testing.T) {
	m := New(agent.New(&spyBackend{t: t, site: "https://api.datadoghq.com"}, assistant.SendOptions{}), Config{
		OpenURL: func(context.Context, string) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.op = operation{kind: opTurn, events: make(chan agent.Event), cancel: cancel}
	m.chatPhase = chat.PhaseStreaming
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "/settings"})

	_, cmd := m.submit()
	if cmd == nil {
		t.Fatal("active-turn /settings returned no launcher command")
	}
	if ctx.Err() != nil {
		t.Fatal("/settings cancelled the active turn")
	}
}

func TestSubmitModelIsUnknown(t *testing.T) {
	m := newModelWithSpy(t)
	m.editor.Update(tea.PasteMsg{Content: "/model"})

	_, _ = m.submit()
	if latestNotice(m).Level != chat.NoticeError || !strings.Contains(latestNotice(m).Text, "Unknown command: /model") {
		t.Fatalf("model command result: notice=%#v", latestNotice(m))
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
	if !strings.Contains(latestNotice(m).Text, want) || latestNotice(m).Level != chat.NoticeInfo {
		t.Fatalf("success notice = %#v", latestNotice(m))
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

	_, _ = m.submit()
	if launched {
		t.Fatalf("missing-conversation result: launched=%v", launched)
	}
	if latestNotice(m).Level != chat.NoticeWarn || !strings.Contains(latestNotice(m).Text, "Start a conversation") {
		t.Fatalf("missing-conversation notice = %#v", latestNotice(m))
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

	_, _ = m.submit()
	if launched {
		t.Fatalf("unsupported-site result: launched=%v", launched)
	}
	if latestNotice(m).Level != chat.NoticeError || !strings.Contains(latestNotice(m).Text, "Could not build a web link") || latestNotice(m).Err == nil {
		t.Fatalf("unsupported-site notice = %#v", latestNotice(m))
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
	if latestNotice(m).Level != chat.NoticeError || !errors.Is(latestNotice(m).Err, launchErr) {
		t.Fatalf("launcher-failure notice = %#v", latestNotice(m))
	}
	for _, want := range []string{"Could not open a browser", "https://app.datadoghq.com/ask/conversation-1"} {
		if !strings.Contains(latestNotice(m).Text, want) {
			t.Fatalf("launcher-failure notice %q does not contain %q", latestNotice(m).Text, want)
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
	m.op = operation{kind: opTurn, events: make(chan agent.Event), cancel: cancel}
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
	m.op = operation{kind: opTurn, events: make(chan agent.Event), cancel: cancel}
	m.chatPhase = chat.PhaseStreaming
	m.editor.Update(tea.PasteMsg{Content: "/resume"})
	_, _ = m.submit()
	if latestNotice(m).Empty() {
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
	m.op = operation{kind: opTurn, events: make(chan agent.Event), cancel: cancel}
	m.chatPhase = chat.PhaseLoading
	m.editor.Update(tea.PasteMsg{Content: "/resume"})
	_, _ = m.submit()
	if latestNotice(m).Empty() {
		t.Fatal("expected history-load rejection notice")
	}
	if ctx.Err() != nil || m.mode == ModeConversations {
		t.Fatal("resume cancelled startup restore or opened picker")
	}
}

func TestDispatchQuitCancelsRunningTurnAndQuits(t *testing.T) {
	m := &Model{}
	ctx, cancel := context.WithCancel(context.Background())
	m.op = operation{kind: opTurn, cancel: cancel}

	got, cmd := m.dispatchCommand("quit", "")
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
	_, cmd := m.dispatchCommand("quit", "")
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
			m.op = operation{kind: opTurn, events: make(chan agent.Event), cancel: cancel}
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
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
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
	m.op = operation{kind: opRestore, cancel: cancel}
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
	m.op = operation{kind: opTurn, events: make(chan agent.Event), cancel: cancel}
	m.editor.Update(tea.PasteMsg{Content: "/nope"})

	_, _ = m.submit()
	if latestNotice(m).Empty() {
		t.Fatal("expected an unknown-command notice")
	}
	if ctx.Err() != nil {
		t.Fatal("unknown command cancelled the active turn")
	}
}

func TestDispatchUnknownCommandPostsNotice(t *testing.T) {
	m := &Model{}
	_, _ = m.dispatchCommand("nope", "")

	// Local messages are appended synchronously and need no timer.
	if latestNotice(m).Empty() {
		t.Fatal("expected an unknown-command notice on the model")
	}
	if latestNotice(m).Level != chat.NoticeError {
		t.Errorf("notice level = %v, want NoticeError", latestNotice(m).Level)
	}
}

func TestLatestCopyableAssistantResponse(t *testing.T) {
	blocks := []agent.Block{
		{
			Role:     assistant.RoleAssistant,
			Kind:     assistant.KindText,
			Complete: true,
			Markdown: &assistant.MarkdownPayload{Content: "earlier response"},
		},
		{
			Role:     assistant.RoleAssistant,
			Kind:     assistant.KindReasoning,
			Complete: true,
			Thinking: &assistant.ThinkingPayload{Content: "hidden reasoning"},
		},
		{
			Role:     assistant.RoleAssistant,
			Kind:     assistant.KindToolResult,
			Complete: true,
			Tool:     &agent.ToolBlock{Output: "credential-like tool output"},
		},
		{
			Role:     assistant.RoleAssistant,
			Kind:     assistant.KindText,
			Complete: false,
			Markdown: &assistant.MarkdownPayload{Content: "partial response"},
		},
		{
			Role:     assistant.RoleAssistant,
			Kind:     assistant.KindText,
			Complete: true,
			Markdown: &assistant.MarkdownPayload{Content: "\x1b[31mlatest\x1b[0m response"},
		},
	}

	got, ok := latestCopyableAssistantResponse(blocks)
	if !ok {
		t.Fatal("latest completed assistant response was not found")
	}
	if want := "latest response"; got != want {
		t.Fatalf("copied text = %q, want %q", got, want)
	}
}

func TestCopyCommandWritesLatestAssistantResponseLocally(t *testing.T) {
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}))
	m.editor.Focus()
	m.transcript.Blocks = []agent.Block{
		{Role: assistant.RoleUser, Kind: assistant.KindText, Complete: true, Markdown: &assistant.MarkdownPayload{Content: "prompt"}},
		{Role: assistant.RoleAssistant, Kind: assistant.KindText, Complete: true, Markdown: &assistant.MarkdownPayload{Content: "answer"}},
	}
	m.editor.Update(tea.PasteMsg{Content: "/copy"})

	_, cmd := m.submit()
	if cmd == nil {
		t.Fatal("/copy returned no clipboard command")
	}
	if latestNotice(m).Level != chat.NoticeInfo || latestNotice(m).Text != "Copied to clipboard." {
		t.Fatalf("notice = %+v, want copy confirmation", latestNotice(m))
	}
	if cmd() == nil {
		t.Fatal("/copy clipboard command returned no message")
	}
}

func TestCopyCommandRejectsStreamingResponse(t *testing.T) {
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}))
	m.editor.Focus()
	m.transcript.Blocks = []agent.Block{{Role: assistant.RoleAssistant, Kind: assistant.KindText, Complete: true, Markdown: &assistant.MarkdownPayload{Content: "previous answer"}}}
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	m.chatPhase = chat.PhaseStreaming
	m.editor.Update(tea.PasteMsg{Content: "/copy"})

	_, _ = m.submit()
	if latestNotice(m).Empty() {
		t.Fatal("streaming /copy should show a rejection notice")
	}
	if !strings.Contains(latestNotice(m).Text, "Wait for the assistant response to finish") {
		t.Fatalf("notice = %q, want active-turn rejection", latestNotice(m).Text)
	}
}

func TestCopyCommandReportsNoCompletedAssistantResponse(t *testing.T) {
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}))
	m.transcript.Blocks = []agent.Block{
		{Role: assistant.RoleUser, Kind: assistant.KindText, Complete: true, Markdown: &assistant.MarkdownPayload{Content: "prompt"}},
		{Role: assistant.RoleAssistant, Kind: assistant.KindText, Complete: false, Markdown: &assistant.MarkdownPayload{Content: "partial"}},
	}

	_, _ = m.dispatchCommand("copy", "")
	if latestNotice(m).Empty() {
		t.Fatal("/copy without a completed response should show a notice")
	}
	if latestNotice(m).Level != chat.NoticeWarn || !strings.Contains(latestNotice(m).Text, "No completed assistant response") {
		t.Fatalf("notice = %+v, want no-response warning", latestNotice(m))
	}
}
