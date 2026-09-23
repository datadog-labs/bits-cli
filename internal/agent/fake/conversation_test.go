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
	set := workspaceTools(t, agent.ModeAllowAll)
	runTurn(t, engine, "why is latency high?", set, agent.DenyContinue, nil)
	result, _ := runTurn(t, engine, `:: r = call("read_file", {"path": "go.mod"}); say(r.output)`, set, agent.DenyContinue, nil)
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

	missing := <-agent.New(f, assistant.SendOptions{}).SwitchConversation(ctx, "00000000-0000-4000-8000-ffffffffffff")
	if !errors.Is(missing.Err, assistant.ErrNotFound) {
		t.Fatalf("unknown conversation error = %v", missing.Err)
	}
}
