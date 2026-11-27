package acp

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/filediff"
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
	copied := agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeCopied, Key: "tool:c1"},
		Kind: assistant.KindToolCall,
		Tool: &agent.ToolBlock{Name: "search_logs", Status: agent.ToolSuccess},
	}
	serverRead := agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: "s1"},
		Kind: assistant.KindToolCall,
		Tool: &agent.ToolBlock{Name: "read_file", Input: `{"path": "a.go"}`, HasFinalInput: true, Status: agent.ToolSuccess},
	}
	revised := serverRead
	revised.Rev = 1 // as when the engine records state no card shows
	shown := []agent.Block{
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "w", Kind: assistant.KindWidget}, Kind: assistant.KindWidget, Widget: &assistant.LegacyWidget{Title: "Errors"}},
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "a", Kind: assistant.KindApp}, Kind: assistant.KindApp, App: &assistant.AppPayload{ToolName: "show_ui"}},
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "d", Kind: assistant.KindDashboard}, Kind: assistant.KindDashboard, Dashboard: &assistant.DashboardPayload{}},
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "m", Kind: assistant.KindMemory}, Kind: assistant.KindMemory, Memory: &assistant.MemoryContextPayload{Memories: make([]assistant.RetrievedMemory, 2)}},
	}
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
		"a revision the card does not show is not sent": {
			states: []agent.State{{Blocks: []agent.Block{serverRead}}, {Blocks: []agent.Block{revised}}},
			want:   [][]string{{"card s1 read_file"}, nil},
		},
		"a server tool named like a client tool is not one": {
			states: []agent.State{{Blocks: []agent.Block{serverRead}}},
			want:   [][]string{{"card s1 read_file"}},
		},
		"a forked conversation's tool call is a card": {
			states: []agent.State{{Blocks: []agent.Block{copied}}},
			want:   [][]string{{"card copied:tool:c1 search_logs"}},
		},
		"what an editor cannot render is named on a card": {
			states: []agent.State{{Blocks: shown}},
			want: [][]string{{
				"card msg:w#widget Widget: Errors",
				"card msg:a#app View",
				"card msg:d#dashboard Dashboard",
				"card msg:m#memory 2 memories found",
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := newMirror(t.TempDir())
			for i, state := range tc.states {
				state.Version = uint64(i + 1)
				if got := summary(m.updates(state)); !slices.Equal(got, tc.want[i]) {
					t.Fatalf("state %d: updates %q, want %q", i, got, tc.want[i])
				}
			}
		})
	}
}

func TestFileDiff(t *testing.T) {
	const root = "/work"
	long := strings.Repeat("line\n", 100)
	ready := func(snapshot filediff.SnapshotState, before, after string) *filediff.State {
		diff := filediff.Build("a/a.txt", "b/a.txt", before, after)
		return &filediff.State{Phase: filediff.PhaseReady, Snapshot: &filediff.Snapshot{Path: "a.txt", Raw: before, State: snapshot}, Preview: &filediff.Preview{Diff: &diff}}
	}
	applied := func(op filediff.Op, before, after string) *filediff.State {
		diff := filediff.Build("a/a.txt", "b/a.txt", before, after)
		return &filediff.State{Phase: filediff.PhaseApplied, Change: &filediff.Change{Path: "a.txt", Op: op, State: filediff.ChangeApplied, Diff: &diff}}
	}
	for name, tc := range map[string]struct {
		state any
		want  []string // @line old -> new, per hunk
	}{
		"a preview":               {ready(filediff.SnapshotReady, "one\ntwo\n", "one\n2\n"), []string{"@1 one\ntwo\n -> one\n2\n"}},
		"a removed final newline": {ready(filediff.SnapshotReady, "a\nb\n", "a\nb"), []string{"@1 a\nb\n -> a\nb"}},
		// Line content has no line endings, so the card names the change.
		"a line-ending change":       {ready(filediff.SnapshotReady, "a\r\n", "a\n"), []string{"@1 a\n -> a\n", "format: CRLF → LF"}},
		"a new file has no old text": {ready(filediff.SnapshotMissing, "", "new\n"), []string{"@1 <nil> -> new\n"}},
		// The file changed while the call awaited approval.
		"the applied change, not the preview": {applied(filediff.OpEdit, "one\ntwo\nthree\n", "one\n2\nthree\n"), []string{"@1 one\ntwo\nthree\n -> one\n2\nthree\n"}},
		"a large file sends its hunks only": {
			applied(filediff.OpEdit, "first\n"+long+"last\n", "FIRST\n"+long+"LAST\n"),
			[]string{"@1 first\nline\nline\nline\n -> FIRST\nline\nline\nline\n", "@99 line\nline\nline\nlast\n -> line\nline\nline\nLAST\n"},
		},
		"a change the tool could not render": {&filediff.State{Phase: filediff.PhaseApplied, Change: &filediff.Change{Path: "a.txt", State: filediff.ChangeUnavailable}}, nil},
		"history":                            {nil, nil},
	} {
		t.Run(name, func(t *testing.T) {
			var got []string
			diffs, locations := fileDiff(root, &agent.ToolBlock{RenderState: tc.state})
			for i, content := range diffs {
				if content.Content != nil {
					got = append(got, content.Content.Content.Text.Text)
					continue
				}
				d := content.Diff
				if d.Path != filepath.Join(root, "a.txt") {
					t.Fatalf("path %q", d.Path)
				}
				old := "<nil>"
				if d.OldText != nil {
					old = *d.OldText
				}
				got = append(got, fmt.Sprintf("@%d %s -> %s", *locations[i].Line, old, d.NewText))
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("diffs %q, want %q", got, tc.want)
			}
		})
	}
}

func TestToolCards(t *testing.T) {
	server := func(status agent.ToolStatus, detail, output string) agent.Block {
		return agent.Block{
			ID:   agent.BlockID{Scope: agent.ScopeTool, Key: "s1"},
			Kind: assistant.KindToolCall,
			Tool: &agent.ToolBlock{Name: "search_logs", Status: status, Detail: detail, Output: output},
		}
	}
	approved := agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: "s1"},
		Kind: assistant.KindToolCall,
		Tool: &agent.ToolBlock{
			Name: "approval_request", IsClientSide: true, Status: agent.ToolSuccess, Output: "approved",
			Input: `{"tool_name": "delete_monitor", "tool_args": {"id": 1}, "tool_call_id": "s1"}`,
		},
	}
	for name, tc := range map[string]struct {
		block  agent.Block
		status acp.ToolCallStatus
		want   []string
	}{
		// As the multi-agent backend explains a server tool's result.
		"a detail alone":          {server(agent.ToolSuccess, "Found 3 logs", ""), acp.ToolCallStatusCompleted, []string{"Found 3 logs"}},
		"a detail and its output": {server(agent.ToolError, "Timed out", "deadline exceeded"), acp.ToolCallStatusFailed, []string{"Timed out", "deadline exceeded"}},
		"a detail repeating it":   {server(agent.ToolSuccess, "done", "done"), acp.ToolCallStatusCompleted, []string{"done"}},
		"a running call":          {server(agent.ToolRunning, "Searching", ""), acp.ToolCallStatusInProgress, nil},
		// The server runs the call once the request is approved.
		"an approved request": {approved, acp.ToolCallStatusPending, nil},
	} {
		t.Run(name, func(t *testing.T) {
			card := toolUpdate(t.TempDir(), "s1", tc.block, false).ToolCall
			if card.Status != tc.status {
				t.Fatalf("status %q, want %q", card.Status, tc.status)
			}
			var got []string
			for _, item := range card.Content {
				if item.Content != nil && item.Content.Content.Text != nil {
					got = append(got, item.Content.Content.Text.Text)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("texts %q, want %q", got, tc.want)
			}
		})
	}
}
