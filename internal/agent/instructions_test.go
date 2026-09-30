package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

func TestConversationInstructionsSnapshotAndReset(t *testing.T) {
	backend := &conversationRecordingBackend{messages: make(map[string][]string)}
	dir := t.TempDir()
	writeInstructionTestFile(t, dir, "AGENTS.md", "original instructions")
	manager := NewProjectInstructionsManager(dir)
	engine := New(backend, assistant.SendOptions{}, WithProjectInstructionsManager(manager))
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "visible prompt", UserContext: func(context.Context) string { return "existing" }}))
	if got := backend.opts[0].CustomUserContext; !strings.HasPrefix(got, "existing\n\n# Project-Specific Context") || !strings.Contains(got, "original instructions") {
		t.Fatalf("first context = %q", got)
	}
	writeInstructionTestFile(t, dir, "AGENTS.md", "updated instructions")
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "visible prompt", UserContext: func(context.Context) string { return "existing" }}))
	if got := backend.opts[1].CustomUserContext; got != "existing" {
		t.Fatalf("later context = %q", got)
	}
	for _, message := range backend.messages["new-conversation-1"] {
		if message != "visible prompt" {
			t.Fatalf("visible prompt changed: %q", message)
		}
	}
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "new prompt"}))
	if got := backend.opts[2].CustomUserContext; !strings.Contains(got, "updated instructions") || strings.Contains(got, "original instructions") {
		t.Fatalf("reset context = %q", got)
	}
}

func TestResumedConversationReplacesUnknownInstructions(t *testing.T) {
	backend := &conversationRecordingBackend{messages: make(map[string][]string)}
	dir := t.TempDir()
	writeInstructionTestFile(t, dir, "AGENTS.md", "local instructions")
	manager := NewProjectInstructionsManager(dir)
	engine := New(backend, assistant.SendOptions{ConversationID: "existing"}, WithProjectInstructionsManager(manager))
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "resume"}))
	if !strings.HasPrefix(backend.opts[0].CustomUserContext, instructionsReplacementNotice) || !strings.Contains(backend.opts[0].CustomUserContext, "local instructions") {
		t.Fatal("resumed conversation did not replace unknown prior instructions")
	}
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "new"}))
	if !manager.prepared || !strings.Contains(backend.opts[1].CustomUserContext, "local instructions") {
		t.Fatal("new conversation did not load instructions")
	}
}

func TestInstructionsAreNotResentOnToolContinuations(t *testing.T) {
	backend := &contextRecordingBackend{}
	tools, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "read"},
		Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeInstructionTestFile(t, dir, "AGENTS.md", "local instructions")
	engine := New(backend, assistant.SendOptions{}, WithProjectInstructionsManager(NewProjectInstructionsManager(dir)))
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "inspect", Tools: tools, UserContext: func(context.Context) string { return "environment" }}))
	if len(backend.userContexts) != 2 {
		t.Fatalf("requests = %d", len(backend.userContexts))
	}
	if !strings.HasPrefix(backend.userContexts[0], "environment\n\n") || !strings.Contains(backend.userContexts[0], "local instructions") || backend.userContexts[1] != "" {
		t.Fatalf("request contexts = %q", backend.userContexts)
	}
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "next turn", Tools: tools, UserContext: func(context.Context) string { return "environment" }}))
	if backend.userContexts[2] != "" {
		t.Fatalf("next turn context = %q", backend.userContexts[2])
	}
}

func TestResumedConversationWithdrawsUnknownInstructionsOnce(t *testing.T) {
	backend := &conversationRecordingBackend{messages: make(map[string][]string)}
	engine := New(backend, assistant.SendOptions{ConversationID: "existing"}, WithProjectInstructionsManager(NewProjectInstructionsManager(t.TempDir())))
	for range 2 {
		drain(engine.StartTurn(context.Background(), TurnInput{Message: "continue"}))
	}
	if backend.opts[0].CustomUserContext != instructionsRemovalNotice || backend.opts[1].CustomUserContext != "" {
		t.Fatalf("resume contexts = %q, %q", backend.opts[0].CustomUserContext, backend.opts[1].CustomUserContext)
	}
}

func TestConversationSwitchResetsInstructionsOnlyOnCommit(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprintf("commit=%t", commit), func(t *testing.T) {
			response := &assistant.ConversationHistoryResponse{}
			response.Data.Type = "conversation-history-response"
			backend := &conversationBackend{history: func(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
				return response, nil
			}}
			manager := NewProjectInstructionsManager(t.TempDir())
			manager.apply(context.Background(), assistant.SendOptions{})
			engine := New(backend, assistant.SendOptions{ConversationID: "old"}, WithProjectInstructionsManager(manager))
			conversation, err := engine.LoadConversation(context.Background(), testConversationID)
			if err != nil {
				t.Fatal(err)
			}
			if !manager.prepared {
				t.Fatal("switch reset instructions before commit")
			}
			if commit {
				if err := engine.InstallConversation(context.Background(), conversation); err != nil {
					t.Fatal(err)
				}
				if manager.prepared || manager.known {
					t.Fatal("committed switch retained previous conversation instructions")
				}
			} else {
				if !manager.prepared || !manager.known {
					t.Fatal("discarded switch lost previous conversation instructions")
				}
			}
		})
	}
}
