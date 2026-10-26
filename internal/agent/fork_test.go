package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

type forkBackend struct {
	historyBackend
	result *assistant.ConversationSummary
	err    error
	calls  int
	cancel context.CancelFunc
}

func (b *forkBackend) ForkConversation(context.Context, string) (*assistant.ConversationSummary, error) {
	b.calls++
	if b.cancel != nil {
		b.cancel()
	}
	return b.result, b.err
}

func TestForkConversationPreservesSource(t *testing.T) {
	const target = "87654321-4321-4321-4321-210987654321"
	b := &forkBackend{result: &assistant.ConversationSummary{ID: target, ConversationID: target}}
	e := New(b, Config{ConversationID: testConversationID})
	defer e.Close()
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	result, err := e.ForkConversation(ctx, testConversationID)
	if !errors.Is(err, context.Canceled) || result == nil || result.ConversationID != target {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if e.State().ConversationID != testConversationID {
		t.Fatal("fork changed source state")
	}
}

func TestForkConversationValidation(t *testing.T) {
	for _, id := range []string{"bad", testConversationID} {
		b := &forkBackend{result: &assistant.ConversationSummary{ID: id, ConversationID: id}}
		e := New(b, Config{ConversationID: testConversationID})
		_, err := e.ForkConversation(context.Background(), testConversationID)
		e.Close()
		if err == nil {
			t.Fatalf("accepted target %q", id)
		}
	}
	b := &forkBackend{}
	e := New(b, Config{})
	defer e.Close()
	if _, err := e.ForkConversation(context.Background(), ""); err == nil || b.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, b.calls)
	}
}

func TestForkBoundaryRestoredFromHistory(t *testing.T) {
	for _, boundary := range []string{"copy", "control", "", "missing"} {
		t.Run(boundary, func(t *testing.T) {
			response := &assistant.ConversationHistoryResponse{}
			response.Data.Type = "conversation-history-response"
			response.Data.Attributes.Messages = []assistant.Message{assistant.AssistantMessage("copy", assistant.TextContent("copied")), assistant.AssistantMessage("control", assistant.Content{Type: assistant.ContentUserStop, Stop: &assistant.StopPayload{}}), assistant.AssistantMessage("later", assistant.TextContent("new"))}
			response.Data.Attributes.ForkedFrom = &assistant.ForkedFrom{ConversationID: testConversationID, ForkMessageID: boundary, SourceTitle: "Original title"}
			e := New(&historyBackend{resp: response}, Config{})
			defer e.Close()
			conversation, err := e.LoadConversation(context.Background(), "87654321-4321-4321-4321-210987654321")
			if err != nil {
				t.Fatal(err)
			}
			if err = e.InstallConversation(context.Background(), conversation); err != nil {
				t.Fatal(err)
			}
			state := e.State()
			if boundary == "copy" || boundary == "control" {
				if state.Fork.After != state.Blocks[0].ID || state.Fork.SourceTitle != "Original title" {
					t.Fatalf("fork=%+v", state.Fork)
				}
			} else if state.Fork.After != (BlockID{}) {
				t.Fatalf("invented boundary %+v", state.Fork)
			}
			if err = e.NewConversation(); err != nil {
				t.Fatal(err)
			}
			if e.State().Fork != (ForkBoundary{}) {
				t.Fatal("new conversation retained fork")
			}
		})
	}
}

func TestForkHistorySeparatesCopiedWork(t *testing.T) {
	for name, later := range map[string]bool{"copied_only": false, "continued": true} {
		t.Run(name, func(t *testing.T) {
			messages := startTaskMsgs("start", testTaskID)
			messages = append(messages, clientCallMsg("pending", "input", "{}"), assistant.AssistantMessage("answer", assistant.TextContent("copied answer")), updateMsg("progress", "progress", "copied progress"))
			if later {
				messages = append(messages, assistant.AssistantMessage("result", assistant.ToolResultContent("pending", "input", assistant.ToolStatusSuccess, "new result")), updateMsg("later", "final", "new progress"))
			}
			response := &assistant.ConversationHistoryResponse{}
			response.Data.Type = "conversation-history-response"
			response.Data.Attributes.Messages = messages
			response.Data.Attributes.ForkedFrom = &assistant.ForkedFrom{ConversationID: testConversationID, ForkMessageID: "progress"}
			b := &taskBackend{historyBackend: historyBackend{resp: response}}
			e := New(b, Config{})
			t.Cleanup(e.Close)
			conversation, err := e.LoadConversation(context.Background(), "87654321-4321-4321-4321-210987654321")
			if err != nil {
				t.Fatal(err)
			}
			if err = e.InstallConversation(context.Background(), conversation); err != nil {
				t.Fatal(err)
			}
			state := e.State()
			e.Close()
			if len(b.attached()) != 0 {
				t.Fatal("subscribed to copied task")
			}
			blocks := BlocksWithTasks(state.Blocks, state.Tasks)
			boundary := -1
			for i, block := range blocks {
				if block.ID == state.Fork.After {
					boundary = i
				}
				if block.Tool != nil && !block.Tool.Status.IsTerminal() {
					t.Fatal("copied tool still running")
				}
			}
			if boundary < 0 {
				t.Fatal("missing boundary")
			}
			if blocks[boundary].MessageID != "answer" {
				t.Fatal("boundary ignores displayed copied task order")
			}
			if len(conversation.continuation) != 0 {
				t.Fatal("copied tool is resumable")
			}
			for i, block := range blocks {
				if (block.Tool != nil && block.Tool.Output == "new result") || block.MessageID == "later" {
					if i <= boundary {
						t.Fatal("new output precedes fork marker")
					}
				}
			}
			if later && len(blocks) != 6 {
				t.Fatalf("got %d blocks, want separate copied and new tool blocks", len(blocks))
			}
		})
	}
}

func TestForkHistoryKeepsNewWorkLive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		messages := startTaskMsgs("copied-start", testTaskID)
		boundary := messages[len(messages)-1].MessageID
		messages = append(messages, startTaskMsgs("new-start", "new-task")...)
		messages = append(messages, clientCallMsg("new-call", "input", "{}"))
		response := &assistant.ConversationHistoryResponse{}
		response.Data.Type = "conversation-history-response"
		response.Data.Attributes.Messages = messages
		response.Data.Attributes.ForkedFrom = &assistant.ForkedFrom{ConversationID: testConversationID, ForkMessageID: boundary}
		b := &taskBackend{historyBackend: historyBackend{resp: response}}
		e := New(b, Config{})
		defer e.Close()
		const forkID = "87654321-4321-4321-4321-210987654321"
		conversation, err := e.LoadConversation(context.Background(), forkID)
		if err != nil {
			t.Fatal(err)
		}
		if err = e.InstallConversation(context.Background(), conversation); err != nil {
			t.Fatal(err)
		}
		e.do(e.discoverTasks)
		synctest.Wait()
		if got := b.attached(); len(got) != 1 || got[0] != forkID+"/new-task" {
			t.Fatalf("subscriptions=%v", got)
		}
		if len(conversation.continuation) != 1 || conversation.continuation[0].Tool.ToolCallID != "new-call" {
			t.Fatal("new tool lost continuation")
		}
		before := e.State().Fork
		e.do(func() {
			e.transcript.AppendMessage(assistant.AssistantMessage("live", assistant.ToolResultContent("copied-start", "start_task", assistant.ToolStatusSuccess, "new output")))
		})
		state := e.State()
		if state.Fork != before || state.Blocks[len(state.Blocks)-1].Tool.Output != "new output" {
			t.Fatal("live result changed copied prefix")
		}
	})
}
