package fake

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
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
	engine := agent.New(f, assistant.SendOptions{})
	set := workspaceTools(t, agent.ModeSkipPermissions)
	runTurn(t, engine, "random()", set, agent.DenyContinue, nil)
	result, _ := runTurn(t, engine, `r = call("read_file", {"path": "go.mod"}); say(r.output)`, set, agent.DenyContinue, nil)
	id := result.ConversationID

	list := <-engine.ListConversations(ctx)
	if list.Err != nil || len(list.Conversations) != 1 || list.Conversations[0].ConversationID != id {
		t.Fatalf("list = %+v, want only %s", list, id)
	}

	switched := <-agent.New(f, assistant.SendOptions{}).SwitchConversation(ctx, id)
	if switched.Err != nil {
		t.Fatal(switched.Err)
	}
	defer func() { _ = switched.Discard() }()
	if got, want := viewOf(switched.Blocks), viewOf(result.Blocks); !slices.Equal(got, want) {
		t.Fatalf("restored blocks differ\n got %+v\nwant %+v", got, want)
	}

	// Like the real API, the history resource has its own id.
	history, err := f.ConversationHistory(ctx, assistant.ConversationHistoryInput{ConversationID: id})
	if err != nil || history.Data.ID == "" || history.Data.ID == id {
		t.Fatalf("history data.id = %q (err %v), want a response id distinct from %s", history.Data.ID, err, id)
	}

	missing := <-agent.New(f, assistant.SendOptions{}).SwitchConversation(ctx, "00000000-0000-4000-8000-ffffffffffff")
	if !errors.Is(missing.Err, assistant.ErrNotFound) {
		t.Fatalf("unknown conversation error = %v", missing.Err)
	}
}

func TestPushedConversationResumesInANewEngine(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	set, err := agent.NewToolSet(agent.ModeSkipPermissions, agent.Tool{
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
	result, _ := runTurn(t, agent.New(f, assistant.SendOptions{}), script, set, agent.DenyContinue, nil)
	id := texts(result.Blocks)[0]
	list, err := f.UserConversations(ctx)
	if err != nil || len(list.Data.Attributes.Conversations) != 2 || !slices.ContainsFunc(list.Data.Attributes.Conversations, func(c assistant.ConversationSummary) bool {
		return c.ConversationID == id && c.Title == "Pending echo"
	}) {
		t.Fatalf("conversations = %+v, want one pushed %s", list, id)
	}

	engine := agent.New(f, assistant.SendOptions{ConversationID: id})
	for range engine.Restore(ctx) {
	}
	for ev := range engine.ResumePendingTools(ctx, agent.TurnInput{Tools: set}) {
		if ev.Err != nil {
			t.Fatal(ev.Err)
		}
	}
	if got := texts(engine.Snapshot()); !slices.Equal(got, []string{"earlier", "after resumed"}) {
		t.Fatalf("answers = %q", got)
	}
}
