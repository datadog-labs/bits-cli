package tui

import (
	"image"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/fake"
)

// suggestScript is a turn that ends by suggesting prompts.
const suggestScript = `say("Done."); tool("suggest_follow_ups", {"prompts": ["A", "B"]}, out='{"accepted":true}')`

// newFollowUpsModel runs each script as a turn of a fake conversation, then
// lays the chat out as the program does after them.
func newFollowUpsModel(t *testing.T, scripts ...string) *Model {
	t.Helper()
	m := New(agent.New(&fake.Fake{ContinueDir: t.TempDir(), BundledFixtures: true}, agent.Config{}), Config{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	for _, script := range scripts {
		setConversationInput(m, script)
		_, _ = m.submit()
		drainConversationRemote(t, m)
	}
	m.View()
	return m
}

// sentAfterScript returns the message sent after the script, if any.
func sentAfterScript(m *Model) string {
	if prompts := userTexts(m); len(prompts) > 1 {
		return prompts[len(prompts)-1]
	}
	return ""
}

// shownFollowUps returns the suggestions the chat shows, or nil.
func shownFollowUps(m *Model) []string {
	if f, ok := m.prompt().(*followUps); ok {
		return f.prompts
	}
	return nil
}

func TestFollowUpsShowTheLatestSet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		later  string // a turn after it
		draft  string
		want   []string
	}{
		{name: "suggested", script: suggestScript, want: []string{"A", "B"}},
		{name: "a later message retires them", script: suggestScript, later: `say("Done.")`},
		{name: "a later set replaces an earlier one", script: `load("follow_ups.star", "superseded"); superseded()`, want: []string{"The later suggestion", "Another later one"}},
		{name: "a set without prompts keeps the earlier one", script: suggestScript + `; tool("suggest_follow_ups", {"prompts": [" "]}, out="{}")`, want: []string{"A", "B"}},
		{name: "none suggested", script: `say("Done.")`},
		{name: "a draft hides them", script: suggestScript, draft: "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scripts := []string{tc.script}
			if tc.later != "" {
				scripts = append(scripts, tc.later)
			}
			m := newFollowUpsModel(t, scripts...)
			if tc.draft != "" {
				setConversationInput(m, tc.draft)
			}
			if got := shownFollowUps(m); !slices.Equal(got, tc.want) {
				t.Fatalf("shown %q, want %q", got, tc.want)
			}
		})
	}
}

// The suggestions leave the composer its keys until tab moves to them. A
// pick sends it as the next message, which retires the set.
func TestFollowUpsKeys(t *testing.T) {
	press := func(code rune) tea.Msg { return tea.KeyPressMsg{Code: code} }
	typed := func(r rune) tea.Msg { return tea.KeyPressMsg{Code: r, Text: string(r)} }
	tab, up, down, enter, esc := press(tea.KeyTab), press(tea.KeyUp), press(tea.KeyDown), press(tea.KeyEnter), press(tea.KeyEscape)
	for _, tc := range []struct {
		name   string
		keys   []tea.Msg
		sent   string // the message a pick sent, if any
		shown  bool   // the suggestions still show
		editor string
		exit   bool // the exit card opened
	}{
		{name: "enter alone sends nothing", keys: []tea.Msg{enter}, shown: true},
		{name: "tab then enter sends the first", keys: []tea.Msg{tab, enter}, sent: "A"},
		{name: "down moves to the next", keys: []tea.Msg{tab, down, enter}, sent: "B"},
		{name: "up wraps to the last", keys: []tea.Msg{tab, up, enter}, sent: "B"},
		{name: "esc dismisses them", keys: []tea.Msg{tab, esc}},
		{name: "esc alone opens the exit card", keys: []tea.Msg{esc}, shown: true, exit: true},
		{name: "up alone recalls the last message", keys: []tea.Msg{up}, editor: suggestScript},
		{name: "typing writes a message", keys: []tea.Msg{tab, typed('j')}, editor: "j"},
		{name: "other keys go back to the composer", keys: []tea.Msg{tab, press(tea.KeyBackspace)}, shown: true},
		{name: "a dismissed set stays dismissed", keys: []tea.Msg{tab, esc, typed('j'), press(tea.KeyBackspace)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newFollowUpsModel(t, suggestScript)
			for _, key := range tc.keys {
				m.Update(key)
				m.View()
			}
			drainConversationRemote(t, m)
			sent := sentAfterScript(m)
			if sent != tc.sent || (shownFollowUps(m) != nil) != tc.shown || m.editor.Value() != tc.editor || m.exitCard.open != tc.exit {
				t.Fatalf("sent %q, shown %q, editor %q, exit card %t; want %q, %t, %q, %t",
					sent, shownFollowUps(m), m.editor.Value(), m.exitCard.open, tc.sent, tc.shown, tc.editor, tc.exit)
			}
			if m.focus() != focusEditor && !tc.exit {
				t.Fatalf("focus = %v, want the composer", m.focus())
			}
		})
	}
}

// A click on a suggestion sends it whether or not the list held the keyboard;
// one beside the suggestions only moves the keyboard there.
func TestFollowUpsClicks(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  int // the row clicked, the header being 0
		sent string
	}{
		{name: "first", row: 1, sent: "A"},
		{name: "second", row: 2, sent: "B"},
		{name: "header", row: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newFollowUpsModel(t, suggestScript)
			at := m.frame.prompt.Min.Add(image.Pt(followUpsPadding+2, tc.row))
			tap(m, at)
			m.View()
			focus := focusPrompt
			if tc.sent != "" {
				focus = focusEditor
			}
			drainConversationRemote(t, m)
			if sent := sentAfterScript(m); sent != tc.sent || m.focus() != focus {
				t.Fatalf("sent %q, focus %v; want %q, %v", sent, m.focus(), tc.sent, focus)
			}
		})
	}
}

// Suggestions made before an approval in the same turn wait for the turn to
// end, and show then without taking the keyboard the approval held.
func TestFollowUpsWaitForApprovals(t *testing.T) {
	set, err := agent.NewToolSet() // the turn runs approval_request
	if err != nil {
		t.Fatal(err)
	}
	m := New(agent.New(&fake.Fake{ContinueDir: t.TempDir()}, agent.Config{}), Config{Tools: set})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	setConversationInput(m, `tool("suggest_follow_ups", {"prompts": ["A"]}, out="{}"); call("approval_request", {"tool_name": "mute_monitor", "tool_args": {}}); say("Muted.")`)
	_, _ = m.submit()
	for waitingApprovals(m) == 0 {
		m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
	m.View()
	if _, ok := m.prompt().(*approvalPrompt); !ok || m.focus() != focusPrompt {
		t.Fatalf("shown %T with focus %v, want the focused approval", m.prompt(), m.focus())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // allows
	drainConversationRemote(t, m)
	m.View()
	if got := shownFollowUps(m); !slices.Equal(got, []string{"A"}) || m.focus() != focusEditor {
		t.Fatalf("shown %q with focus %v, want the suggestions and the composer", got, m.focus())
	}
}

// Suggestions without room to show leave the keyboard to the composer, and
// shown ones leave the transcript its jump-to-latest control.
func TestFollowUpsLayout(t *testing.T) {
	t.Run("no room", func(t *testing.T) {
		// A running task takes the tray row the list would need.
		m := newFollowUpsModel(t, `task("Watch", ["_Watching_"], every="1h"); `+suggestScript)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 8})
		m.View()
		if !m.frame.prompt.Empty() || m.frame.tooSmall {
			t.Fatalf("prompt at %v, too small %t; want a chat without the list", m.frame.prompt, m.frame.tooSmall)
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		if m.focus() != focusEditor {
			t.Fatalf("focus = %v, want the composer", m.focus())
		}
	})
	t.Run("jump to latest", func(t *testing.T) {
		m := newFollowUpsModel(t, `say("\n\n".join(["Paragraph %d." % i for i in range(40)])); tool("suggest_follow_ups", {"prompts": ["A"]}, out="{}")`)
		m.list.ScrollBy(-10)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
		m.View()
		if shownFollowUps(m) == nil || m.frame.follow.Empty() {
			t.Fatalf("shown %q, jump control at %v; want both", shownFollowUps(m), m.frame.follow)
		}
	})
}
