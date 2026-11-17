package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/fake"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

// testConversations is the resume history of workspace dir, in a fresh config
// directory.
func testConversations(t *testing.T, dir string) *workspaceConversations {
	t.Helper()
	t.Setenv("BITS_CONFIG_DIR", t.TempDir())
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return newWorkspaceConversations(ws)
}

// opened records ids as opened in order, so the last is the most recent.
func opened(t *testing.T, w *workspaceConversations, ids ...string) {
	t.Helper()
	history, err := w.history()
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if err := history.Record(context.Background(), id, time.Unix(int64(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
}

// pushed returns the ids of n conversations the fake now owns, most recently
// updated first, as the account's list orders them.
func pushed(t *testing.T, backend *fake.Fake, n int) []string {
	t.Helper()
	backend.Delay = 0
	for range n {
		if _, err := backend.PushConversation(context.Background(), "", `say("earlier")`); err != nil {
			t.Fatal(err)
		}
	}
	list, err := backend.UserConversations(context.Background(), assistant.UserConversationsInput{})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(list.Data.Attributes.Conversations))
	for i, c := range list.Data.Attributes.Conversations {
		ids[i] = c.ConversationID
	}
	return ids
}

type failingLister struct{ fake.Fake }

func (*failingLister) UserConversations(context.Context, assistant.UserConversationsInput) (*assistant.UserConversationsResponse, error) {
	return nil, errors.New("service unavailable")
}

// --last takes the most recently opened conversation the account owns,
// wherever it falls in the account's list, and never substitutes another
// account's or a new one when it owns none.
func TestLastPicksMostRecentOwned(t *testing.T) {
	const deleted = "99999999-9999-4999-8999-999999999999"
	// owned holds 150 conversations, so the last 50 are on the second page.
	cases := []struct {
		name    string
		opened  func(owned []string) []string // oldest first
		backend agent.Backend                 // the fake when nil
		want    func(owned []string) string
		wantErr string
	}{
		{
			name:   "most recently opened",
			opened: func(owned []string) []string { return []string{owned[1], owned[0]} },
			want:   func(owned []string) string { return owned[0] },
		},
		{
			name:   "skips a conversation the account no longer owns",
			opened: func(owned []string) []string { return []string{owned[1], deleted} },
			want:   func(owned []string) string { return owned[1] },
		},
		{
			name:   "reopened long after its last update",
			opened: func(owned []string) []string { return []string{owned[0], owned[140]} },
			want:   func(owned []string) string { return owned[140] },
		},
		{
			name:    "none owned",
			opened:  func([]string) []string { return []string{deleted} },
			wantErr: "for the signed-in account",
		},
		{
			name:    "list failure is not an empty list",
			opened:  func(owned []string) []string { return owned[:1] },
			backend: &failingLister{},
			wantErr: "service unavailable",
		},
	}
	backend := fake.New()
	owned := pushed(t, backend, 150)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := testConversations(t, t.TempDir())
			opened(t, w, tc.opened(owned)...)
			var b agent.Backend = backend
			if tc.backend != nil {
				b = tc.backend
			}
			start, err := w.start(ChatOptions{Last: true})
			if err != nil {
				t.Fatal(err)
			}
			got, err := start(context.Background(), b)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || got != "" {
					t.Fatalf("last = %q, %v; want error %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want(owned) {
				t.Fatalf("last = %q, %v; want %q", got, err, tc.want(owned))
			}
		})
	}
}

// `bits run --last` fails before any turn when the workspace has nothing to
// resume, and says which of the two reasons applies.
func TestRunLastWithoutResumableConversation(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "1")
	cases := []struct {
		name    string
		opened  []string
		wantErr string
	}{
		{"nothing opened here", nil, "no conversation to resume in"},
		{"none owned by this login", []string{"99999999-9999-4999-8999-999999999999"}, "for the signed-in account"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			w := testConversations(t, dir)
			opened(t, w, tc.opened...)
			history, err := w.history()
			if err != nil {
				t.Fatal(err)
			}
			out, _, err := executeForTest(t, []string{"run", "--last", "--prompt", `say("hi")`, "--delivery", "adeep"})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), history.Workspace()) {
				t.Fatalf("error = %v, want %q naming the workspace", err, tc.wantErr)
			}
			var exit *ExitError
			if errors.As(err, &exit) && exit.Code == ExitUsage {
				t.Fatalf("missing conversation reported as usage: %v", err)
			}
			if out != "" {
				t.Fatalf("a turn started: %q", out)
			}
		})
	}
}

// awaitRun drains run's events and fails the test on its error.
func awaitRun(t *testing.T, events <-chan agent.Event, run agent.RunID, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	for event := range agent.Follow(events, run) {
		if event.Kind == agent.EventError {
			t.Fatal(event.Err)
		}
	}
}

// drainRun drains run's events whatever its outcome.
func drainRun(t *testing.T, events <-chan agent.Event, run agent.RunID, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	for range agent.Follow(events, run) {
	}
}

// The recorder keeps the conversation a turn creates, one the chat switches
// to, and one a later chat restores at startup, as --last does, so --last
// returns to whichever was open last. A conversation that fails to restore is
// not recorded: only an id the server confirmed is.
func TestFollowRecordsConfirmedConversations(t *testing.T) {
	w := testConversations(t, t.TempDir())
	backend := fake.New()
	earlier := pushed(t, backend, 1)[0]
	engine := agent.New(backend, agent.Config{})
	w.follow(engine)
	events, unsubscribe := engine.Subscribe()
	run, err := engine.StartTurn(context.Background(), agent.TurnInput{Message: `say("hi")`})
	awaitRun(t, events, run, err)
	created := engine.State().ConversationID
	conversation, err := engine.LoadConversation(context.Background(), earlier)
	if err == nil {
		err = engine.InstallConversation(context.Background(), conversation)
	}
	if err != nil {
		t.Fatal(err)
	}
	unsubscribe()
	engine.Close()
	assertRecent(t, w, earlier, created)

	restored := agent.New(backend, agent.Config{ConversationID: created})
	w.follow(restored)
	events, unsubscribe = restored.Subscribe()
	run, err = restored.Restore(context.Background())
	awaitRun(t, events, run, err)
	unsubscribe()
	restored.Close()
	assertRecent(t, w, created, earlier)

	failed := agent.New(backend, agent.Config{ConversationID: "99999999-9999-4999-8999-999999999999"})
	w.follow(failed)
	events, unsubscribe = failed.Subscribe()
	defer unsubscribe()
	run, err = failed.Restore(context.Background())
	drainRun(t, events, run, err)
	failed.Close()
	assertRecent(t, w, created, earlier)
}

// A record that cannot be saved is kept for the exit warning. A workspace Git
// cannot resolve records nothing and warns nothing: the chat never asked for
// the history, and /resume and --last report why.
func TestFollowReportsFailedRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in git is a shell script")
	}
	cases := []struct {
		name     string
		setup    func(t *testing.T)
		wantWarn bool
	}{
		{"config directory unwritable", func(t *testing.T) {
			notDir := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(notDir, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BITS_CONFIG_DIR", notDir)
		}, true},
		{"workspace unresolvable", func(t *testing.T) {
			bin := t.TempDir()
			script := "#!/bin/sh\necho 'fatal: detected dubious ownership' >&2\nexit 128\n"
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := testConversations(t, t.TempDir())
			tc.setup(t)
			engine := agent.New(fake.New(), agent.Config{})
			w.follow(engine)
			events, unsubscribe := engine.Subscribe()
			defer unsubscribe()
			run, err := engine.StartTurn(context.Background(), agent.TurnInput{Message: `say("hi")`})
			awaitRun(t, events, run, err)
			engine.Close()
			if err := w.wait(); (err != nil) != tc.wantWarn {
				t.Fatalf("wait = %v, want a warning: %t", err, tc.wantWarn)
			}
		})
	}
}

// assertRecent waits for the records of every engine followed so far, then
// checks their order.
func assertRecent(t *testing.T, w *workspaceConversations, want ...string) {
	t.Helper()
	w.recorders.Wait()
	if err := w.err; err != nil {
		t.Fatal(err)
	}
	got, err := w.recent()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("recent = %q, want %q", got, want)
	}
}
