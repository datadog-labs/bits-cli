package chat

import (
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

func TestResetInvalidatesCacheAcrossConversationIdentityDomains(t *testing.T) {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(4)
	id := agent.BlockID{Scope: agent.ScopeLocal, Key: "1"}
	list.SetItems([]agent.Block{{ID: id, Rev: 0, Role: assistant.RoleUser, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: "OLD-CONTENT"}}})
	if got := list.Render(); !strings.Contains(got, "OLD-CONTENT") {
		t.Fatalf("old render missing content: %q", got)
	}

	list.Reset()
	list.SetItems([]agent.Block{{ID: id, Rev: 0, Role: assistant.RoleUser, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: "NEW-CONTENT"}}})
	got := list.Render()
	if !strings.Contains(got, "NEW-CONTENT") || strings.Contains(got, "OLD-CONTENT") {
		t.Fatalf("reset reused stale cached render: %q", got)
	}
}

// listWithTool returns a sized list holding one tool block in the given status.
func listWithTool(status agent.ToolStatus) *List {
	list := NewList()
	list.SetStyles(DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(8)
	block := toolBlockOf(status)
	block.ID = agent.BlockID{Scope: agent.ScopeTool, Key: "call-1"}
	list.SetItems([]agent.Block{block})
	return list
}

// TestSetFrameAnimatesInFlightBlocks is the whole point of the frame counter:
// advancing it must change what an in-flight tool block renders, even though
// the block's revision and the list width are unchanged. That means the render
// cache has to be bypassed for these blocks.
func TestSetFrameAnimatesInFlightBlocks(t *testing.T) {
	for _, test := range []struct {
		name   string
		status agent.ToolStatus
	}{
		{"running", agent.ToolRunning},
		{"awaiting approval", agent.ToolAwaitingApproval},
	} {
		t.Run(test.name, func(t *testing.T) {
			list := listWithTool(test.status)
			seen := map[string]bool{}
			for frame := range DefaultStyles(true).StatusRunningLabel.Len() {
				list.SetFrame(frame)
				seen[list.Render()] = true
			}
			if len(seen) < 2 {
				t.Errorf("rendered %d distinct views across a sweep, want at least 2", len(seen))
			}
		})
	}
}

// TestSetFrameLeavesSettledBlocksCached keeps the per-frame cost proportional
// to the number of in-flight tools rather than the length of the transcript.
func TestSetFrameLeavesSettledBlocksCached(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	want := list.Render()
	for _, frame := range []int{1, 9, 40} {
		list.SetFrame(frame)
		if got := list.Render(); got != want {
			t.Fatalf("frame %d changed a settled block", frame)
		}
	}
}

// TestSettledBlocksStayCachedAcrossFrames asserts the cache is actually doing
// the work: a settled block rendered once must not be re-rendered when the
// frame advances. Mutating the block's payload behind the list's back is
// visible only if the cache was bypassed.
func TestSettledBlocksStayCachedAcrossFrames(t *testing.T) {
	list := listWithTool(agent.ToolSuccess)
	first := list.Render()
	if !strings.Contains(first, "search_logs") {
		t.Fatalf("expected the tool name in %q", first)
	}

	list.items[0].Tool.Name = "MUTATED"
	list.SetFrame(5)
	if got := list.Render(); strings.Contains(got, "MUTATED") {
		t.Error("settled block was re-rendered when the frame advanced; it should have been served from cache")
	}
}

// TestInFlightBlocksBypassCache is the same probe inverted: a running block
// must pick the mutation up, proving it is re-rendered every frame.
func TestInFlightBlocksBypassCache(t *testing.T) {
	list := listWithTool(agent.ToolRunning)
	list.Render()

	list.items[0].Tool.Name = "MUTATED"
	list.SetFrame(5)
	if got := list.Render(); !strings.Contains(got, "MUTATED") {
		t.Error("in-flight block was served from cache; it must re-render each frame")
	}
}
