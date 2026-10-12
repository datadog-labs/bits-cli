package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent/env"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func runInstructionsGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	// Test repositories must not inherit the developer or machine's hooks.
	hooks := t.TempDir()
	command := exec.Command("git", append([]string{
		"-c", "core.hooksPath=" + hooks,
		"-c", "commit.gpgsign=false",
		"-c", "user.name=Bits Test",
		"-c", "user.email=bits@example.com",
	}, args...)...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func writeInstructionTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProjectInstructionsEmptySnapshotLoadsOnce(t *testing.T) {
	dir := t.TempDir()
	instructions := newProjectInstructions(dir)
	opts := assistant.SendOptions{CustomUserContext: "existing"}
	first := instructions.apply(context.Background(), opts)
	instructions.acknowledge()
	if first.CustomUserContext != "existing" {
		t.Fatalf("empty snapshot = %q", first.CustomUserContext)
	}
	writeInstructionTestFile(t, dir, "AGENTS.md", "created later")
	second := instructions.apply(context.Background(), opts)
	if second.CustomUserContext != "existing" {
		t.Fatalf("snapshot was reloaded: %q", second.CustomUserContext)
	}
	instructions.reset()
	third := instructions.apply(context.Background(), opts)
	if !strings.Contains(third.CustomUserContext, "created later") {
		t.Fatalf("reset did not reload: %q", third.CustomUserContext)
	}
	if opts.CustomUserContext != "existing" {
		t.Fatalf("default context mutated: %q", opts.CustomUserContext)
	}
}

func TestProjectInstructionsResumeUpdates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before string
		after  string
		resume bool
		want   string
	}{
		{name: "unchanged on resume", before: "old", after: "old", want: instructionsReplacementNotice},
		{name: "replaced", before: "old", after: "new", want: instructionsReplacementNotice},
		{name: "removed", before: "old", want: instructionsRemovalNotice},
		{name: "cleared", before: "old", after: " \n\t", want: instructionsRemovalNotice},
		{name: "added on resume", after: "new", want: instructionsReplacementNotice},
		{name: "still absent on resume", want: instructionsRemovalNotice},
		{name: "resumed unknown with instructions", resume: true, after: "new", want: instructionsReplacementNotice},
		{name: "resumed unknown without instructions", resume: true, want: instructionsRemovalNotice},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			instructions := newProjectInstructions(dir)
			ctx := context.Background()
			if !tc.resume {
				if tc.before != "" {
					writeInstructionTestFile(t, dir, "AGENTS.md", tc.before)
				}
				instructions.apply(ctx, assistant.SendOptions{})
				instructions.acknowledge()
				instructions.reset()
			}
			if tc.after != "" {
				writeInstructionTestFile(t, dir, "AGENTS.md", tc.after)
			} else if tc.before != "" {
				if err := os.Remove(filepath.Join(dir, "AGENTS.md")); err != nil {
					t.Fatal(err)
				}
			}
			opts := assistant.SendOptions{ConversationID: "conversation", CustomUserContext: "existing"}
			got := instructions.apply(ctx, opts).CustomUserContext
			instructions.acknowledge()
			if !strings.HasPrefix(got, "existing\n\n"+tc.want) {
				t.Fatalf("update = %q, want notice %q", got, tc.want)
			}
			if strings.TrimSpace(tc.after) != "" && !strings.Contains(got, "\n"+tc.after+"\n") {
				t.Fatalf("missing new contents in %q", got)
			}
			if next := instructions.apply(ctx, opts).CustomUserContext; next != "existing" {
				t.Fatalf("continuation repeated update: %q", next)
			}
		})
	}
}

func TestProjectInstructionsCascadingReplacement(t *testing.T) {
	repo := t.TempDir()
	runInstructionsGit(t, repo, "init", "-b", "main")
	nested := filepath.Join(repo, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstructionTestFile(t, repo, "AGENTS.md", "root instructions")
	writeInstructionTestFile(t, nested, "AGENTS.md", "nested instructions")
	instructions := newProjectInstructions(nested)
	instructions.apply(context.Background(), assistant.SendOptions{})
	instructions.acknowledge()
	if err := os.Remove(filepath.Join(nested, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	instructions.reset()
	got := instructions.apply(context.Background(), assistant.SendOptions{ConversationID: "conversation"}).CustomUserContext
	if !strings.HasPrefix(got, instructionsReplacementNotice) || !strings.Contains(got, "root instructions") || strings.Contains(got, "nested instructions") {
		t.Fatalf("remaining cascade = %q", got)
	}
}

func TestConversationInstructionsSnapshotAndReset(t *testing.T) {
	backend := &conversationRecordingBackend{messages: make(map[string][]string)}
	dir := t.TempDir()
	writeInstructionTestFile(t, dir, "AGENTS.md", "original instructions")
	engine := New(backend, assistant.SendOptions{}, WithProjectInstructions(env.NewInstructions(dir)))
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "visible prompt", UserContext: func(context.Context) string { return "existing" }}))
	if got := backend.opts[0].CustomUserContext; !strings.HasPrefix(got, "existing\n\n<project_instructions>\nMake sure to follow these project instructions.") || !strings.Contains(got, "original instructions") {
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
	engine := New(backend, assistant.SendOptions{ConversationID: "existing"}, WithProjectInstructions(env.NewInstructions(dir)))
	instructions := engine.projectInstructions
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "resume"}))
	if !strings.HasPrefix(backend.opts[0].CustomUserContext, instructionsReplacementNotice) || !strings.Contains(backend.opts[0].CustomUserContext, "local instructions") {
		t.Fatal("resumed conversation did not replace unknown prior instructions")
	}
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "new"}))
	if instructions.phase != instructionsDelivered || !strings.Contains(backend.opts[1].CustomUserContext, "local instructions") {
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
	engine := New(backend, assistant.SendOptions{}, WithProjectInstructions(env.NewInstructions(dir)))
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
	engine := New(backend, assistant.SendOptions{ConversationID: "existing"}, WithProjectInstructions(env.NewInstructions(t.TempDir())))
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
			engine := New(backend, assistant.SendOptions{ConversationID: "old"}, WithProjectInstructions(env.NewInstructions(t.TempDir())))
			instructions := engine.projectInstructions
			instructions.apply(context.Background(), assistant.SendOptions{})
			instructions.acknowledge()
			conversation, err := engine.LoadConversation(context.Background(), testConversationID)
			if err != nil {
				t.Fatal(err)
			}
			if instructions.phase != instructionsDelivered {
				t.Fatal("switch reset instructions before commit")
			}
			if commit {
				if err := engine.InstallConversation(context.Background(), conversation); err != nil {
					t.Fatal(err)
				}
				if instructions.phase != instructionsUnprepared {
					t.Fatal("committed switch retained previous conversation instructions")
				}
			} else {
				if instructions.phase != instructionsDelivered {
					t.Fatal("discarded switch lost previous conversation instructions")
				}
			}
		})
	}
}

// instructionDeliveryBackend can assign an ID before a request fails, without
// necessarily streaming a response that confirms the context was received.
type instructionDeliveryBackend struct {
	contexts      []string
	streamFailure bool
}

func (b *instructionDeliveryBackend) Send(_ context.Context, _ any, opts assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.contexts = append(b.contexts, opts.CustomUserContext)
	if len(b.contexts) > 1 || b.streamFailure {
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("hello"))
		if err := emit(response); err != nil {
			return "conversation", err
		}
	}
	if len(b.contexts) == 1 {
		return "conversation", errors.New("request failed")
	}
	return "conversation", nil
}

func TestProjectInstructionsRetryUntilDelivered(t *testing.T) {
	for _, tc := range []struct{ name, conversationID, contents, notice string }{
		{"insertion", "", "original instructions", "<project_instructions>\nMake sure to follow these project instructions."},
		{"replacement", "conversation", "original instructions", instructionsReplacementNotice},
		{"removal", "conversation", "", instructionsRemovalNotice},
	} {
		for _, streamFailure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/streamed=%t", tc.name, streamFailure), func(t *testing.T) {
				dir := t.TempDir()
				if tc.contents != "" {
					writeInstructionTestFile(t, dir, "AGENTS.md", tc.contents)
				}
				backend := &instructionDeliveryBackend{streamFailure: streamFailure}
				engine := New(backend, assistant.SendOptions{ConversationID: tc.conversationID}, WithProjectInstructions(env.NewInstructions(dir)))
				drain(engine.StartTurn(context.Background(), TurnInput{Message: "first"}))
				if !strings.HasPrefix(backend.contexts[0], tc.notice) {
					t.Fatalf("first update = %q", backend.contexts[0])
				}
				// A retry must reuse the prepared snapshot and notice, even after an ID
				// was assigned or files changed while the failed turn was in flight.
				writeInstructionTestFile(t, dir, "AGENTS.md", "changed after failure")
				drain(engine.StartTurn(context.Background(), TurnInput{Message: "retry"}))
				want := backend.contexts[0]
				if streamFailure {
					want = ""
				}
				if backend.contexts[1] != want {
					t.Fatalf("retry update = %q, want %q", backend.contexts[1], want)
				}
				drain(engine.StartTurn(context.Background(), TurnInput{Message: "later"}))
				if backend.contexts[2] != "" {
					t.Fatalf("delivered update repeated: %q", backend.contexts[2])
				}
			})
		}
	}
}

func newProjectInstructions(path string) *instructionDelivery {
	return &instructionDelivery{source: env.NewInstructions(path)}
}

const instructionsReplacementNotice = "<project_instructions>\nThis block replaces all earlier provided <project_instructions> blocks."

var instructionsRemovalNotice = env.ReplaceInstructions("")

const clientSkillsRemovalNotice = env.SkillsRemovalNotice
