package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/workspace"
)

// halfStreamedWriteBackend streams half of a write_file input on its first
// Send and then waits for cancellation, never sending the final call. Later
// Sends answer with plain text.
type halfStreamedWriteBackend struct{ sends int }

func (b *halfStreamedWriteBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.sends++
	if b.sends > 1 {
		return "conversation", emitMessages(emit, assistant.AssistantMessage("answer", assistant.TextContent("still alive")))
	}
	err := emitMessages(emit,
		assistant.AssistantMessage("write", assistant.Content{
			Type: assistant.ContentToolCallStarted,
			Tool: &assistant.ToolPayload{ToolCallID: "write", ToolName: "write_file", IsClientSide: true},
		}),
		assistant.AssistantMessage("write", assistant.Content{
			Type: assistant.ContentToolCallInputDelta,
			Tool: &assistant.ToolPayload{ToolCallID: "write", PartialJSON: `{"path":"half.txt","content":"alpha\nbravo\nch`},
		}),
	)
	if err != nil {
		return "conversation", err
	}
	<-ctx.Done()
	return "conversation", ctx.Err()
}

func emitMessages(emit func(assistant.AssistantResponse) error, messages ...assistant.Message) error {
	for _, message := range messages {
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = message
		if err := emit(response); err != nil {
			return err
		}
	}
	return nil
}

// Esc while a client tool's input is half-streamed must leave a stopped
// block, not a frozen spinner: the settled state has to reach the TUI even
// though the turn's context is already cancelled.
func TestCancelledTurnSettlesStreamedClientToolInTUI(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	toolSet, err := agent.NewToolSet(agent.ModeAllowAll, tools.NewClientTools(ws)...)
	if err != nil {
		t.Fatal(err)
	}
	m := New(agent.New(&halfStreamedWriteBackend{}, assistant.SendOptions{}), Config{Tools: toolSet, Workspace: ws})
	m.resize(100, 30)

	setConversationInput(m, "write half.txt")
	_, _ = m.submit()
	// The backend waits right after its last delta, so once the TUI shows
	// the partial input, the turn is parked mid-input.
	for !hasPartialTool(m.blocks, "write_file", "bravo") {
		if m.turnEvents == nil {
			t.Fatal("turn ended before the write_file input was half-streamed")
		}
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.turnGen, m.turnEvents)))
	}

	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	drainConversationRemote(t, m)

	tool := findToolBlock(m.blocks, "write_file")
	if tool == nil || !tool.Cancelled || tool.Status == agent.ToolRunning {
		t.Fatalf("write_file block = %+v, want cancelled and not running", tool)
	}
	if _, err := os.Stat(filepath.Join(ws.Path(), "half.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("half.txt stat error = %v, want not exist", err)
	}

	setConversationInput(m, "are you there?")
	_, _ = m.submit()
	drainConversationRemote(t, m)
	if m.chatPhase != chat.PhaseIdle || m.turnEvents != nil {
		t.Fatalf("follow-up turn did not complete: phase=%v active=%t", m.chatPhase, m.turnEvents != nil)
	}
	if !hasTextBlock(m.blocks, "still alive") {
		t.Fatalf("follow-up transcript = %+v, want normal answer", m.blocks)
	}
}

func hasPartialTool(blocks []agent.Block, name, fragment string) bool {
	tool := findToolBlock(blocks, name)
	return tool != nil && strings.Contains(tool.InputPartial, fragment)
}

func findToolBlock(blocks []agent.Block, name string) *agent.ToolBlock {
	for _, block := range blocks {
		if block.Tool != nil && block.Tool.Name == name {
			return block.Tool
		}
	}
	return nil
}

func hasTextBlock(blocks []agent.Block, text string) bool {
	for _, block := range blocks {
		if block.Kind == assistant.KindText && block.Markdown != nil && strings.Contains(block.Markdown.Content, text) {
			return true
		}
	}
	return false
}
