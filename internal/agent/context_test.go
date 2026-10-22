package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent/env"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func TestUserBlocksKeepTheContextTheyWereSentWith(t *testing.T) {
	engine := New(&scriptBackend{}, Config{})
	dashboard := assistant.ContextEntity{Type: assistant.EntityDashboard, ID: "dash-1", Label: "Checkout overview"}
	file, sent := screenshotFile(t)
	turnContext := &assistant.AssistantContext{Entities: []assistant.ContextEntity{dashboard, file}}
	drain(t, engine, TurnInput{Message: "compare", Context: turnContext})

	want := []assistant.ContextEntity{dashboard, sent}
	if got := engine.Snapshot()[0].Context; !reflect.DeepEqual(got, want) {
		t.Fatalf("user block context = %+v, want %+v", got, want)
	}
}

func TestRestoredUserBlocksKeepTheirContext(t *testing.T) {
	user := assistant.Message{
		Role: "user", MessageID: "m1", Content: assistant.TextContent("compare"),
		ContextEntities: []byte(`[{"type":"service","id":"checkout-api"},{"type":"image_url","id":"data:image/png;base64,AAAA"}]`),
	}
	block, _ := NewTranscript().AppendMessage(user)

	want := []assistant.ContextEntity{
		{Type: assistant.EntityService, ID: "checkout-api"},
		{Type: assistant.EntityImageURL, ID: "data:image/png;base64,AAAA"},
	}
	if !reflect.DeepEqual(block.Context, want) {
		t.Fatalf("restored context = %+v, want %+v", block.Context, want)
	}
}

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

// deliverUserRequest prepares a user message on conversation as if the
// backend received it, and returns its custom user context.
func deliverUserRequest(t *testing.T, e *Engine, conversation string) string {
	t.Helper()
	e.opts.ConversationID = conversation
	req, err := e.prepare(context.Background(), TurnInput{}, e.sendOptions(nil), e.delivery, "")
	if err != nil {
		t.Fatal(err)
	}
	e.delivery.pending(req)
	e.delivery.delivered(req)
	return req.opts.CustomUserContext
}

func TestProjectInstructionsEmptySnapshotLoadsOnce(t *testing.T) {
	dir := t.TempDir()
	engine := New(nil, Config{ProjectInstructions: env.NewInstructions(dir)})
	if got := deliverUserRequest(t, engine, ""); got != "" {
		t.Fatalf("empty snapshot = %q", got)
	}
	writeInstructionTestFile(t, dir, "AGENTS.md", "created later")
	if got := deliverUserRequest(t, engine, ""); got != "" {
		t.Fatalf("snapshot was reloaded: %q", got)
	}
	engine.resetContext()
	if got := deliverUserRequest(t, engine, ""); !strings.Contains(got, "created later") {
		t.Fatalf("reset did not reload: %q", got)
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
			engine := New(nil, Config{ProjectInstructions: env.NewInstructions(dir)})
			if !tc.resume {
				if tc.before != "" {
					writeInstructionTestFile(t, dir, "AGENTS.md", tc.before)
				}
				deliverUserRequest(t, engine, "")
				engine.resetContext()
			}
			if tc.after != "" {
				writeInstructionTestFile(t, dir, "AGENTS.md", tc.after)
			} else if tc.before != "" {
				if err := os.Remove(filepath.Join(dir, "AGENTS.md")); err != nil {
					t.Fatal(err)
				}
			}
			got := deliverUserRequest(t, engine, "conversation")
			if !strings.HasPrefix(got, tc.want) {
				t.Fatalf("update = %q, want notice %q", got, tc.want)
			}
			if strings.TrimSpace(tc.after) != "" && !strings.Contains(got, "\n"+tc.after+"\n") {
				t.Fatalf("missing new contents in %q", got)
			}
			if next := deliverUserRequest(t, engine, "conversation"); next != "" {
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
	engine := New(nil, Config{ProjectInstructions: env.NewInstructions(nested)})
	deliverUserRequest(t, engine, "")
	if err := os.Remove(filepath.Join(nested, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	engine.resetContext()
	got := deliverUserRequest(t, engine, "conversation")
	if !strings.HasPrefix(got, instructionsReplacementNotice) || !strings.Contains(got, "root instructions") || strings.Contains(got, "nested instructions") {
		t.Fatalf("remaining cascade = %q", got)
	}
}

func TestConversationInstructionsSnapshotAndReset(t *testing.T) {
	backend := &scriptBackend{convID: "new-conversation-1"}
	dir := t.TempDir()
	writeInstructionTestFile(t, dir, "AGENTS.md", "original instructions")
	engine := New(backend, Config{UserContext: func(context.Context) string { return "existing" }, ProjectInstructions: env.NewInstructions(dir)})
	drain(t, engine, TurnInput{Message: "visible prompt"})
	if got := backend.opts[0].CustomUserContext; !strings.HasPrefix(got, "existing\n\n<project_instructions>\nMake sure to follow these project instructions.") || !strings.Contains(got, "original instructions") {
		t.Fatalf("first context = %q", got)
	}
	writeInstructionTestFile(t, dir, "AGENTS.md", "updated instructions")
	drain(t, engine, TurnInput{Message: "visible prompt"})
	if got := backend.opts[1].CustomUserContext; got != "" {
		t.Fatalf("later context = %q", got)
	}
	for _, message := range backend.userMessages("new-conversation-1") {
		if message != "visible prompt" {
			t.Fatalf("visible prompt changed: %q", message)
		}
	}
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	drain(t, engine, TurnInput{Message: "new prompt"})
	if got := backend.opts[2].CustomUserContext; !strings.Contains(got, "updated instructions") || strings.Contains(got, "original instructions") {
		t.Fatalf("reset context = %q", got)
	}
}

func TestResumedConversationReplacesUnknownInstructions(t *testing.T) {
	backend := &scriptBackend{convID: "new-conversation-1"}
	dir := t.TempDir()
	writeInstructionTestFile(t, dir, "AGENTS.md", "local instructions")
	engine := New(backend, Config{ConversationID: "existing", ProjectInstructions: env.NewInstructions(dir)})
	drain(t, engine, TurnInput{Message: "resume"})
	if !strings.HasPrefix(backend.opts[0].CustomUserContext, instructionsReplacementNotice) || !strings.Contains(backend.opts[0].CustomUserContext, "local instructions") {
		t.Fatal("resumed conversation did not replace unknown prior instructions")
	}
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	drain(t, engine, TurnInput{Message: "new"})
	if !engine.delivery.instructionsDelivered || !strings.Contains(backend.opts[1].CustomUserContext, "local instructions") {
		t.Fatal("new conversation did not load instructions")
	}
}

func TestInstructionsAreNotResentOnToolContinuations(t *testing.T) {
	backend := &scriptBackend{rounds: toolRounds("read"), convID: "conversation-1"}
	tools, err := NewToolSet(Tool{
		Definition: assistant.ClientTool{Name: "read"},
		Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeInstructionTestFile(t, dir, "AGENTS.md", "local instructions")
	engine := New(backend, Config{PermissionsMode: ModeSkipPermissions, UserContext: func(context.Context) string { return "environment" }, ProjectInstructions: env.NewInstructions(dir)})
	drain(t, engine, TurnInput{Message: "inspect", Tools: tools})
	if len(backend.userContexts()) != 2 {
		t.Fatalf("requests = %d", len(backend.userContexts()))
	}
	if !strings.HasPrefix(backend.userContexts()[0], "environment\n\n") || !strings.Contains(backend.userContexts()[0], "local instructions") || backend.userContexts()[1] != "" {
		t.Fatalf("request contexts = %q", backend.userContexts())
	}
	drain(t, engine, TurnInput{Message: "next turn", Tools: tools})
	if backend.userContexts()[2] != "" {
		t.Fatalf("next turn context = %q", backend.userContexts()[2])
	}
}

func TestResumedConversationWithdrawsUnknownInstructionsOnce(t *testing.T) {
	backend := &scriptBackend{convID: "new-conversation-1"}
	engine := New(backend, Config{ConversationID: "existing", ProjectInstructions: env.NewInstructions(t.TempDir())})
	for range 2 {
		drain(t, engine, TurnInput{Message: "continue"})
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
			backend := &historyBackend{resp: response}
			engine := New(backend, Config{ConversationID: "old", ProjectInstructions: env.NewInstructions(t.TempDir())})
			deliverUserRequest(t, engine, "old")
			conversation, err := engine.LoadConversation(context.Background(), testConversationID)
			if err != nil {
				t.Fatal(err)
			}
			if !engine.delivery.instructionsDelivered {
				t.Fatal("switch reset instructions before commit")
			}
			if commit {
				if err := engine.InstallConversation(context.Background(), conversation); err != nil {
					t.Fatal(err)
				}
				if engine.delivery != (contextDelivery{}) {
					t.Fatal("committed switch retained previous conversation instructions")
				}
			} else {
				if !engine.delivery.instructionsDelivered {
					t.Fatal("discarded switch lost previous conversation instructions")
				}
			}
		})
	}
}

// failingFirstRequest assigns a conversation ID to a first request that fails,
// after streaming a response when streamed is set. Later requests answer.
func failingFirstRequest(streamed bool) *scriptBackend {
	answer := []assistant.Message{textMsg("answer", "hello")}
	first := answer
	if !streamed {
		first = nil
	}
	return &scriptBackend{rounds: [][]assistant.Message{first, answer}, convID: "conversation", err: errors.New("request failed")}
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
				backend := failingFirstRequest(streamFailure)
				engine := New(backend, Config{ConversationID: tc.conversationID, ProjectInstructions: env.NewInstructions(dir)})
				drain(t, engine, TurnInput{Message: "first"})
				if !strings.HasPrefix(backend.userContexts()[0], tc.notice) {
					t.Fatalf("first update = %q", backend.userContexts()[0])
				}
				// A retry must reuse the prepared snapshot and notice, even after an ID
				// was assigned or files changed while the failed turn was in flight.
				writeInstructionTestFile(t, dir, "AGENTS.md", "changed after failure")
				drain(t, engine, TurnInput{Message: "retry"})
				want := backend.userContexts()[0]
				if streamFailure {
					want = ""
				}
				if backend.userContexts()[1] != want {
					t.Fatalf("retry update = %q, want %q", backend.userContexts()[1], want)
				}
				drain(t, engine, TurnInput{Message: "later"})
				if backend.userContexts()[2] != "" {
					t.Fatalf("delivered update repeated: %q", backend.userContexts()[2])
				}
			})
		}
	}
}

const instructionsReplacementNotice = "<project_instructions>\nThis block replaces all earlier provided <project_instructions> blocks."

var instructionsRemovalNotice = env.ReplaceInstructions("")

const clientSkillsRemovalNotice = env.SkillsRemovalNotice

func writeSkill(t *testing.T, root, path, name, description string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstructionTestFile(t, root, filepath.Join(path, "SKILL.md"), fmt.Sprintf("---\nname: %s\ndescription: %s\n---\nBODY MUST NOT APPEAR IN CATALOG", name, description))
}

func writeSkillFile(t *testing.T, root, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstructionTestFile(t, root, filepath.Join(path, "SKILL.md"), content)
}

func TestClientSkillConversationSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/example", "example", "original description")
	backend := failingFirstRequest(false)
	engine := New(backend, Config{ClientSkills: env.NewSkills(dir, nil)})
	drain(t, engine, TurnInput{Message: "first"})
	writeSkill(t, dir, ".agents/skills/example", "example", "changed description")
	for range 2 {
		drain(t, engine, TurnInput{Message: "retry or continue"})
	}
	if !strings.Contains(backend.userContexts()[0], "original description") || backend.userContexts()[0] != backend.userContexts()[1] || backend.userContexts()[1] != backend.userContexts()[2] {
		t.Fatalf("catalog not retained: %q", backend.userContexts())
	}
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	drain(t, engine, TurnInput{Message: "new"})
	if !strings.Contains(backend.userContexts()[3], "changed description") {
		t.Fatal("new conversation did not rediscover skills")
	}
}

func TestClientSkillsOnlySentWithUserMessages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/example", "example", "example description")
	backend := &scriptBackend{rounds: toolRounds("read"), convID: "conversation-1"}
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, Config{PermissionsMode: ModeSkipPermissions, UserContext: func(context.Context) string { return "environment" }, ClientSkills: env.NewSkills(dir, nil)})
	drain(t, engine, TurnInput{Message: "inspect", Tools: tools})
	if len(backend.userContexts()) != 2 || !strings.HasPrefix(backend.userContexts()[0], "environment\n\n") || !strings.Contains(backend.userContexts()[0], "example description") || backend.userContexts()[1] != "" {
		t.Fatalf("contexts = %q", backend.userContexts())
	}
	drain(t, engine, TurnInput{Message: "next", Tools: tools})
	if !strings.Contains(backend.userContexts()[2], "example description") || strings.Contains(backend.userContexts()[2], "environment") {
		t.Fatal("catalog missing or environment context repeated")
	}
}

func TestClientSkillsResumeAndEmptySnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	engine := New(nil, Config{ClientSkills: env.NewSkills(dir, nil)})
	if got := deliverUserRequest(t, engine, ""); got != "" {
		t.Fatal("unexpected empty catalog")
	}
	writeSkill(t, dir, ".agents/skills/example", "example", "created later")
	if got := deliverUserRequest(t, engine, ""); got != "" {
		t.Fatal("empty snapshot was rediscovered")
	}
	engine.resetContext()
	if got := deliverUserRequest(t, engine, "existing"); !strings.Contains(got, "created later") {
		t.Fatalf("resumed conversation did not rescan skills: %q", got)
	}
	engine.resetContext()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = engine.prepare(ctx, TurnInput{}, engine.sendOptions(nil), engine.delivery, "")

	if got := deliverUserRequest(t, engine, ""); !strings.Contains(got, "created later") {
		t.Fatal("reset did not discover skills")
	}
}

func TestClientSkillsResumeWithoutSkillsSupersedesOlderCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	engine := New(nil, Config{ClientSkills: env.NewSkills(t.TempDir(), nil)})
	for range 2 {
		if got := deliverUserRequest(t, engine, "existing"); got != clientSkillsRemovalNotice {
			t.Fatalf("resume without skills = %q", got)
		}
	}
	engine.resetContext()
	if got := deliverUserRequest(t, engine, ""); got != "" {
		t.Fatalf("new conversation without skills = %q", got)
	}
}

func TestClientSkillsRescannedOnConversationSwitch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/example", "example", "example description")
	backend := failingFirstRequest(false)
	engine := New(backend, Config{ClientSkills: env.NewSkills(dir, nil)})
	drain(t, engine, TurnInput{Message: "first"})
	if err := engine.InstallConversation(context.Background(), &Conversation{id: "existing", transcript: NewTranscript()}); err != nil {
		t.Fatal(err)
	}
	drain(t, engine, TurnInput{Message: "resumed"})
	if !strings.Contains(backend.userContexts()[1], "example description") {
		t.Fatalf("restored conversation did not rescan skills: %q", backend.userContexts()[1])
	}
}

func TestClientSkillsDeferredUntilUserMessageAfterToolResume(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/example", "example", "example description")
	tools, err := NewToolSet(Tool{
		Definition: assistant.ClientTool{Name: "input"}, Resumable: true,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &scriptBackend{}
	skills := env.NewSkills(dir, nil)
	renders := 0
	engine := New(backend, Config{
		PermissionsMode: ModeSkipPermissions,
		ConversationID:  "conversation",
		ClientSkills:    skills,
		UserContext: func(context.Context) string {
			renders++
			return "environment"
		},
	})
	engine.continuation = continuationFromHistory([]assistant.Message{clientCallMsg("one", "input", `{}`)})
	collect(resumeEvents(engine, t.Context(), TurnInput{Tools: tools}))
	if backend.last().CustomUserContext != "" || renders != 0 {
		t.Fatal("resumed client tool response prepared or sent the catalog")
	}
	writeSkill(t, dir, ".agents/skills/example", "example", "updated after tool resume")
	drain(t, engine, TurnInput{Message: "continue", Tools: tools})
	if renders != 1 || !strings.Contains(backend.last().CustomUserContext, "environment") || !strings.Contains(backend.last().CustomUserContext, "updated after tool resume") {
		t.Fatal("next user message did not include the catalog")
	}
}

func TestSkillRegistrySharedByMenuAndCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/review", "review", "original description")
	writeSkillFile(t, dir, ".agents/skills/manual", "---\nname: manual\ndescription: User only\nmodel-invocable: false\n---\n")
	backend := failingFirstRequest(false)
	engine := New(backend, Config{ClientSkills: env.NewSkills(dir, nil)})
	menu := mustClientSkills(t, engine)
	if len(menu) != 2 || menu[0].Name != "manual" {
		t.Fatalf("menu = %+v", menu)
	}
	menu[1].Description = "mutated by consumer"
	writeSkill(t, dir, ".agents/skills/review", "review", "changed description")
	drain(t, engine, TurnInput{Message: "first"})
	if !strings.Contains(backend.userContexts()[0], "original description") || strings.Contains(backend.userContexts()[0], "User only") {
		t.Fatalf("catalog differs from menu: %q", backend.userContexts()[0])
	}
	if got := mustClientSkills(t, engine)[1].Description; got != "original description" {
		t.Fatalf("registry mutated: %s", got)
	}
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	// Headless turns can populate the same cache before any menu requests it.
	drain(t, engine, TurnInput{Message: "new"})
	writeSkill(t, dir, ".agents/skills/review", "review", "resumed description")
	if got := mustClientSkills(t, engine)[1].Description; got != "changed description" {
		t.Fatalf("menu rescanned engine registry: %s", got)
	}
	if err := engine.InstallConversation(context.Background(), &Conversation{id: "existing", transcript: NewTranscript()}); err != nil {
		t.Fatal(err)
	}
	if got := mustClientSkills(t, engine)[1].Description; got != "resumed description" {
		t.Fatalf("resume retained old registry: %s", got)
	}
}

func mustClientSkills(t *testing.T, engine *Engine) []SkillSummary {
	t.Helper()
	skills, err := engine.ClientSkills(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return skills
}

func TestClientSkillsReturnsDiscoveryError(t *testing.T) {
	engine := New(nil, Config{ClientSkills: env.NewSkills(t.TempDir(), nil)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	summaries, err := engine.ClientSkills(ctx)
	if !errors.Is(err, context.Canceled) || summaries != nil {
		t.Fatalf("canceled load = (%v, %v)", summaries, err)
	}
}

func TestClientSkillInvocationLoadsCurrentBody(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeSkill(t, root, ".agents/skills/review", "review", "Review")
	provider := env.NewSkills(root, nil)
	summaries, err := provider.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	skill := summaries[0]
	writeSkillFile(t, root, ".agents/skills/review", "\xef\xbb\xbf---\r\nname: review\r\ndescription: Updated\r\n---\r\nCurrent <instructions> & body\r\n\t ")
	backend := &scriptBackend{convID: "new-conversation-1"}
	engine := New(backend, Config{ClientSkills: provider})
	arguments := "  Preserve CASE\n\t and trailing space "
	for range 2 {
		drain(t, engine, TurnInput{Message: "/skill:review" + arguments, Skill: &SkillInvocation{Name: skill.Name, Arguments: arguments}})
	}
	for _, opts := range backend.opts {
		userContext := opts.CustomUserContext
		if !strings.Contains(userContext, "<instructions>Current &lt;instructions&gt; &amp; body\r\n\t </instructions>") || !strings.Contains(userContext, "<arguments>"+arguments+"</arguments>") || strings.Contains(userContext, "BODY MUST") {
			t.Fatalf("skill context = %q", userContext)
		}
	}
}

func TestClientSkillInvocationReadFailuresNeverSend(t *testing.T) {
	for _, failure := range []string{"removed", "oversized", "invalid utf8", "invalid metadata", "renamed", "directory", "escaping symlink"} {
		t.Run(failure, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			writeSkill(t, root, ".agents/skills/review", "review", "Review")
			provider := env.NewSkills(root, nil)
			summaries, err := provider.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			skill := summaries[0]
			if err := os.Remove(filepath.Join(root, ".agents/skills/review/SKILL.md")); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "removed":
			case "directory":
				if err := os.Mkdir(filepath.Join(root, ".agents/skills/review/SKILL.md"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "escaping symlink":
				outside := filepath.Join(t.TempDir(), "SKILL.md")
				if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, ".agents/skills/review/SKILL.md")); err != nil {
					t.Fatal(err)
				}
			default:
				content := "---\nname: review\ndescription: Review\n---\n"
				switch failure {
				case "oversized":
					content += strings.Repeat("x", 64*1024)
				case "invalid utf8":
					content += "\xff"
				case "invalid metadata":
					content = "---\nname: [\n---\n"
				case "renamed":
					content = strings.ReplaceAll(content, "name: review", "name: another")
				}
				if err := os.WriteFile(filepath.Join(root, ".agents/skills/review/SKILL.md"), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			backend := &scriptBackend{convID: "new-conversation-1"}
			engine := New(backend, Config{ClientSkills: provider})
			events := drain(t, engine, TurnInput{Message: "/skill:review", Skill: &SkillInvocation{Name: skill.Name}})
			// The failed turn keeps its user block, like a backend failure.
			if failureErr := events[len(events)-1].Err; failureErr == nil || len(backend.opts) != 0 || len(engine.Snapshot()) != 1 {
				t.Fatalf("failure = %v, backend requests = %d, transcript = %+v", failureErr, len(backend.opts), engine.Snapshot())
			}
		})
	}
}

func TestClientSkillInvocationResolvesCurrentRegistry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeSkillFile(t, root, ".agents/skills/review", "---\nname: review\ndescription: Project\n---\nPROJECT BODY")
	writeSkillFile(t, root, "extras/review", "---\nname: review\ndescription: Extra\n---\nEXTRA BODY")
	backend := &scriptBackend{convID: "new-conversation-1"}
	engine := New(backend, Config{ClientSkills: env.NewSkills(root, []string{"extras"})})
	summaries := mustClientSkills(t, engine)
	summaries[0].Name = "tampered"
	drain(t, engine, TurnInput{Message: "/skill:review", Skill: &SkillInvocation{Name: "review"}})
	if !strings.Contains(backend.opts[0].CustomUserContext, "PROJECT BODY") {
		t.Fatal("invocation did not use registry precedence")
	}
	if err := os.Remove(filepath.Join(root, ".agents/skills/review/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	// No menu refresh is needed: the engine resolves the new conversation's registry.
	drain(t, engine, TurnInput{Message: "/skill:review", Skill: &SkillInvocation{Name: "review"}})
	if !strings.Contains(backend.opts[1].CustomUserContext, "EXTRA BODY") {
		t.Fatal("invocation retained the previous conversation's path")
	}
	events := drain(t, engine, TurnInput{Message: "/skill:tampered", Skill: &SkillInvocation{Name: "tampered"}})
	if events[len(events)-1].Err == nil || len(backend.opts) != 2 {
		t.Fatal("unregistered skill reached backend")
	}
}

func TestClientSkillInvocationKeepsDiscoveredDefaultName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeSkillFile(t, root, ".agents/skills/shared", "---\ndescription: Unnamed\n---\nSHARED BODY")
	if err := os.MkdirAll(filepath.Join(root, ".agents/skills/review"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".agents/skills/shared/SKILL.md"), filepath.Join(root, ".agents/skills/review/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	backend := &scriptBackend{convID: "new-conversation-1"}
	engine := New(backend, Config{ClientSkills: env.NewSkills(root, nil)})
	if last := drain(t, engine, TurnInput{Message: "/skill:review", Skill: &SkillInvocation{Name: "review"}}); last[len(last)-1].Kind != EventDone {
		t.Fatal(last[len(last)-1].Err)
	}
	if len(backend.opts) != 1 || !strings.Contains(backend.opts[0].CustomUserContext, "SHARED BODY") {
		t.Fatal("symlinked skill did not resolve under its discovered name")
	}
}
