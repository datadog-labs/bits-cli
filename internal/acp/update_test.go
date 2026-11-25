package acp

import (
	"slices"
	"testing"

	"github.com/coder/acp-go-sdk"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func progress(id, text string) agent.Block {
	return agent.Block{
		ID:       agent.BlockID{Scope: agent.ScopeMessage, Key: id, Kind: assistant.KindProgress},
		Kind:     assistant.KindProgress,
		Role:     assistant.RoleAssistant,
		Progress: &assistant.ProgressPayload{Content: text},
	}
}

func withTask(blocks ...agent.Block) agent.State {
	after := make([]agent.BlockID, len(blocks)) // after no shown block: last
	return agent.State{Tasks: []agent.Task{{ID: "t1", Blocks: blocks, After: after}}}
}

// summary names each update by its kind and its card or text.
func summary(updates []acp.SessionUpdate) []string {
	var out []string
	for _, u := range updates {
		switch {
		case u.AgentMessageChunk != nil:
			out = append(out, "message "+u.AgentMessageChunk.Content.Text.Text)
		case u.ToolCall != nil:
			out = append(out, "card "+string(u.ToolCall.ToolCallId)+" "+u.ToolCall.Title)
		case u.ToolCallUpdate != nil:
			out = append(out, "card update "+string(u.ToolCallUpdate.ToolCallId))
		}
	}
	return out
}

func TestMirrorUpdates(t *testing.T) {
	grownPart := progress("u1-part", "one two three")
	grownPart.Rev = 1 // the engine revises a block it changes
	shrunk := progress("u1", "one")
	shrunk.Rev = 1 // a replay revises the block
	for name, tc := range map[string]struct {
		states []agent.State
		want   [][]string // per state
	}{
		"a task replayed under new ids is not sent again": {
			states: []agent.State{
				withTask(progress("u1", "one"), progress("u2", "two")),
				withTask(progress("u1", "one"), progress("u2-part", "two")),
				withTask(progress("u1", "one"), progress("u2-part", "two"), progress("u3", "three")),
			},
			want: [][]string{
				{"message one", "message two"},
				nil,
				{"message three"},
			},
		},
		"a shorter replay sends nothing": {
			states: []agent.State{
				withTask(progress("u1", "one two")),
				withTask(progress("u1-part", "one")),
				withTask(grownPart),
			},
			want: [][]string{{"message one two"}, nil, {"message  three"}},
		},
		"a block that shrinks sends nothing": {
			states: []agent.State{
				{Blocks: []agent.Block{progress("u1", "one two")}},
				{Blocks: []agent.Block{shrunk}},
			},
			want: [][]string{{"message one two"}, nil},
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := newMirror()
			for i, state := range tc.states {
				state.Version = uint64(i + 1)
				if got := summary(m.updates(state)); !slices.Equal(got, tc.want[i]) {
					t.Fatalf("state %d: updates %q, want %q", i, got, tc.want[i])
				}
			}
		})
	}
}
