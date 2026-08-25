package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantOk   bool
	}{
		{"/quit", "quit", true},
		{"/Quit", "quit", true},
		{"/QUIT", "quit", true},
		{"/quit now", "quit", true}, // trailing args ignored
		{"hello", "", false},
		{"", "", false},
		{"/", "", false},     // bare slash is not a command
		{"/ help", "", false}, // space before the name is not a command
	}
	for _, tc := range cases {
		got, ok := parseCommand(tc.in)
		if got != tc.wantName || ok != tc.wantOk {
			t.Errorf("parseCommand(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.wantName, tc.wantOk)
		}
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
