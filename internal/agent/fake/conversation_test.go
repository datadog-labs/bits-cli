package fake

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// blockView is the part of a block that must survive a history round trip.
type blockView struct {
	role               assistant.Role
	kind               assistant.ContentKind
	text, tool, output string
	status             agent.ToolStatus
}

func viewOf(blocks []agent.Block) []blockView {
	views := make([]blockView, len(blocks))
	for i, b := range blocks {
		views[i] = blockView{role: b.Role, kind: b.Kind}
		switch {
		case b.Markdown != nil:
			views[i].text = b.Markdown.Content
		case b.Thinking != nil:
			views[i].text = b.Thinking.Content
		case b.Tool != nil:
			views[i].tool, views[i].output, views[i].status = b.Tool.Name, b.Tool.Output, b.Tool.Status
		}
	}
	return views
}

func TestConversationRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	engine := agent.New(f, agent.Config{PermissionsMode: agent.ModeSkipPermissions})
	set := workspaceTools(t)
	runTurn(t, engine, "random()", set, agent.DenyContinue, nil)
	if got := engine.State().Title; got != generatedTitle(1) {
		t.Fatalf("streamed title = %q, want the generated one", got)
	}
	blocks, _ := runTurn(t, engine, `r = call("read_file", {"path": "go.mod"}); say(r.output)`, set, agent.DenyContinue, nil)
	id := engine.State().ConversationID

	list := engine.ListConversations(ctx, 0)
	if list.Err != nil || len(list.Conversations) != 1 || list.Conversations[0].ConversationID != id {
		t.Fatalf("list = %+v, want only %s", list, id)
	}

	ttl := 7
	shared, err := engine.ShareConversation(ctx, assistant.ShareConversationInput{ConversationID: id, Shared: true, TTLDays: &ttl})
	if err != nil {
		t.Fatal(err)
	}
	if left := time.Until(*shared); left <= 6*24*time.Hour || left > 7*24*time.Hour {
		t.Fatalf("share expires in %v, want 7 days", left)
	}

	if list := engine.ListConversations(ctx, 0); list.Err != nil || len(list.Conversations) != 1 || list.Conversations[0].SharedExpires == nil {
		t.Fatalf("list = %+v, want the shared conversation", list)
	}

	restored := agent.New(f, agent.Config{})
	conversation, err := restored.LoadConversation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.InstallConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	if got, want := viewOf(restored.State().Blocks), viewOf(blocks); !slices.Equal(got, want) {
		t.Fatalf("restored blocks differ\n got %+v\nwant %+v", got, want)
	}
	if got := restored.State().Title; got != generatedTitle(1) {
		t.Fatalf("restored title = %q", got)
	}
	if got := restored.State().SharedUntil; got == nil || !got.Equal(*shared) {
		t.Fatalf("restored share = %v, want %v", got, shared)
	}

	// Like the real API, the history resource has its own id.
	history, err := f.ConversationHistory(ctx, assistant.ConversationHistoryInput{ConversationID: id})
	if err != nil || history.Data.ID == "" || history.Data.ID == id {
		t.Fatalf("history data.id = %q (err %v), want a response id distinct from %s", history.Data.ID, err, id)
	}

	_, err = restored.LoadConversation(ctx, "00000000-0000-4000-8000-ffffffffffff")
	if !errors.Is(err, assistant.ErrNotFound) {
		t.Fatalf("unknown conversation error = %v", err)
	}
}

func TestPushedConversationResumesInANewEngine(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	set, err := agent.NewToolSet(agent.Tool{
		Resumable:  true,
		Definition: assistant.ClientTool{Name: "echo"},
		Handler: func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
			return agent.ToolResult{Output: "resumed"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The push replays after the live turn's own call: it must return the
	// same id rather than push again.
	script := `id = push_conversation(lambda: say("earlier"), lambda: say("after " + call("echo", {}).output), title="Pending echo")
call("echo", {})
say(id)`
	blocks, _ := runTurn(t, agent.New(f, agent.Config{PermissionsMode: agent.ModeSkipPermissions}), script, set, agent.DenyContinue, nil)
	id := texts(blocks)[0]
	list, err := f.UserConversations(ctx, assistant.UserConversationsInput{})
	if err != nil || len(list.Data.Attributes.Conversations) != 2 || !slices.ContainsFunc(list.Data.Attributes.Conversations, func(c assistant.ConversationSummary) bool {
		return c.ConversationID == id && c.Title == "Pending echo"
	}) {
		t.Fatalf("conversations = %+v, want one pushed %s", list, id)
	}

	engine := agent.New(f, agent.Config{PermissionsMode: agent.ModeSkipPermissions, ConversationID: id})
	for range follow(t, engine, func() (agent.RunID, error) { return engine.Restore(ctx) }) {
	}
	for ev := range follow(t, engine, func() (agent.RunID, error) { return engine.ResumePendingTools(ctx, agent.TurnInput{Tools: set}) }) {
		if ev.Err != nil {
			t.Fatal(ev.Err)
		}
	}
	if got := texts(engine.State().Blocks); !slices.Equal(got, []string{"earlier", "after resumed"}) {
		t.Fatalf("answers = %q", got)
	}
}

func TestSharedByGivesTheConversationAway(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	id, err := f.PushConversation(ctx, "", `say("findings"); shared_by("coworker")`)
	if err != nil {
		t.Fatal(err)
	}
	engine := agent.New(f, agent.Config{})
	conversation, err := engine.LoadConversation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.InstallConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	if state := engine.State(); !state.ReadOnly || state.SharedUntil == nil {
		t.Fatalf("read-only = %v, shared until = %v; want a read-only share", state.ReadOnly, state.SharedUntil)
	}

	if list := engine.ListConversations(ctx, 0); list.Err != nil || len(list.Conversations) != 0 {
		t.Fatalf("list = %+v, want only the user's own conversations", list)
	}
	if _, err := engine.ShareConversation(ctx, assistant.ShareConversationInput{ConversationID: id}); !errors.Is(err, assistant.ErrForbidden) {
		t.Fatalf("share error = %v, want forbidden", err)
	}
	if err := engine.RenameConversation(ctx, id, "Mine"); !errors.Is(err, assistant.ErrForbidden) {
		t.Fatalf("rename error = %v, want forbidden", err)
	}
	// bits run sends without loading the history, so the server refuses it.
	_, err = f.Send(ctx, `say("mine")`, assistant.SendOptions{ConversationID: id}, func(assistant.AssistantResponse) error { return nil })
	if !errors.Is(err, assistant.ErrNotFound) {
		t.Fatalf("send error = %v, want not found", err)
	}
}

func TestStopsCountsStopRequests(t *testing.T) {
	answered := func(ev agent.Event) bool { return slices.Contains(texts(ev.State.Blocks), "long") }
	for _, tc := range []struct {
		name, script string
		stopAt       func(agent.Event) bool
		said         []string
		fails        bool
	}{
		{name: "stopped", script: `say("long"); sleep("20s"); say("should not appear")`, stopAt: answered, said: []string{"long"}},
		{
			name: "stopped before any answer", script: `sleep("20s"); say("should not appear")`,
			stopAt: func(ev agent.Event) bool { return ev.Kind == agent.EventConversation },
		},
		{name: "stop fails", script: `fail_stop(); say("long"); sleep("20s"); say("should not appear")`, stopAt: answered, said: []string{"long"}, fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := agent.New(&Fake{}, agent.Config{})
			defer engine.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events, unsubscribe := engine.Subscribe()
			defer unsubscribe()
			run, err := engine.StartTurn(ctx, agent.TurnInput{Message: tc.script})
			if err != nil {
				t.Fatal(err)
			}
			var last agent.Event
			stopped := false
			for ev := range agent.Follow(events, run) {
				if !stopped && tc.stopAt(ev) {
					stopped = true
					err := engine.StopConversation(context.Background(), run)
					if errors.Is(err, assistant.ErrServer) != tc.fails {
						t.Fatalf("stop = %v, want a failure: %t", err, tc.fails)
					}
					cancel()
				}
				last = ev
			}
			if last.Kind != agent.EventCanceled {
				t.Fatalf("last event = %+v, want a canceled turn", last)
			}
			blocks, _ := runTurn(t, engine, `say("stops: %d" % stops())`, nil, agent.DenyContinue, nil)
			if got, want := texts(blocks), append(tc.said, "stops: 1"); !slices.Equal(got, want) {
				t.Fatalf("answers = %q, want %q", got, want)
			}
		})
	}
}
