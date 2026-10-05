package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

func writeSkill(t *testing.T, root, path, name, description string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstructionTestFile(t, root, filepath.Join(path, "SKILL.md"), fmt.Sprintf("---\nname: %s\ndescription: %s\n---\nBODY MUST NOT APPEAR IN CATALOG", name, description))
}

func TestClientSkillFrontmatter(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"plain", "---\nname: test-skill\ndescription: A useful skill\n---\nbody", true},
		{"folded", "---\nname: test\ndescription: >-\n  A multiline\n  description\nmetadata:\n  author: test\n---\nbody", true},
		{"crlf", "---\r\nname: test\r\ndescription: 'quoted: value'\r\n---\r\n", true},
		{"no frontmatter", "name: test\ndescription: test", false},
		{"missing description", "---\nname: test\n---", false},
		{"blank description", "---\nname: test\ndescription: '  '\n---", false},
		{"invalid name", "---\nname: Bad_Name\ndescription: test\n---", false},
		{"consecutive hyphens", "---\nname: bad--name\ndescription: test\n---", false},
		{"long name", "---\nname: " + strings.Repeat("a", 65) + "\ndescription: test\n---", false},
		{"long description", "---\nname: test\ndescription: " + strings.Repeat("a", 1025) + "\n---", true},
		{"scalar type", "---\nname: test\ndescription: 42\n---", false},
		{"duplicate key", "---\nname: test\nname: duplicate\ndescription: test\n---", false},
		{"unterminated", "---\nname: test\ndescription: test", false},
		{"invalid yaml", "---\nname: [\ndescription: test\n---", false},
		{"disable model invocation", "---\nname: test\ndescription: test\ndisable-model-invocation: true\n---", true},
		{"non-bool disable model invocation", "---\nname: test\ndescription: test\ndisable-model-invocation: maybe\n---", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := parseSkillDocument([]byte(tc.input), "fallback")
			if ok != tc.valid {
				t.Fatalf("valid = %t, want %t", ok, tc.valid)
			}
		})
	}
}

func TestClientSkillDiscoveryPrecedenceAndBoundaries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	runInstructionsGit(t, repo, "init", "-b", "main")
	active := filepath.Join(repo, "nested", "active")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, repo, ".agents/skills/deep/group/root", "root", "root description")
	writeSkill(t, repo, ".agents/skills/duplicate", "duplicate", "root version")
	writeSkill(t, filepath.Dir(active), ".agents/skills/duplicate", "duplicate", "parent version")
	writeSkill(t, active, ".agents/skills/duplicate", "duplicate", "nearest version")
	writeSkill(t, home, ".agents/skills/user", "user", "user description")
	writeSkill(t, home, ".agents/skills/duplicate", "duplicate", "home version")
	extra := t.TempDir()
	writeSkill(t, extra, "custom", "custom", "'quoted <tag> & text'")
	writeSkill(t, extra, "duplicate", "duplicate", "extra version")
	outside := t.TempDir()
	writeSkill(t, outside, "escape", "escape", "outside")
	link := filepath.Join(active, ".agents", "skills", "escape")
	if err := os.Symlink(filepath.Join(outside, "escape"), link); err != nil {
		t.Fatal(err)
	}
	// A directory link within the repository is valid, and a cycle terminates.
	if err := os.Symlink(filepath.Join(repo, ".agents/skills"), filepath.Join(active, ".agents/skills/linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(active, ".agents/skills"), filepath.Join(active, ".agents/skills/cycle")); err != nil {
		t.Fatal(err)
	}
	catalog := NewClientSkills(active, []string{extra, extra}).snapshot(context.Background())
	for _, want := range []string{"nearest version", "root description", "user description", "&lt;tag&gt; &amp; text"} {
		if !strings.Contains(catalog, want) {
			t.Fatalf("missing %q in %s", want, catalog)
		}
	}
	for _, unwanted := range []string{"root version", "parent version", "home version", "extra version", "BODY MUST", `name="escape"`} {
		if strings.Contains(catalog, unwanted) {
			t.Fatalf("unexpected %q in %s", unwanted, catalog)
		}
	}
	if strings.Count(catalog, `name="custom"`) != 1 {
		t.Fatal("duplicate roots produced duplicate skills")
	}
	if got := NewClientSkills(active, []string{extra, extra}).snapshot(context.Background()); got != catalog {
		t.Fatal("catalog is not deterministic")
	}
}

func writeSkillFile(t *testing.T, root, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstructionTestFile(t, root, filepath.Join(path, "SKILL.md"), content)
}

func TestClientSkillDisableModelInvocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	active := t.TempDir()
	writeSkill(t, active, ".agents/skills/visible", "visible", "shown to the model")
	writeSkillFile(t, active, ".agents/skills/hidden", "---\nname: hidden\ndescription: user only\ndisable-model-invocation: true\n---\n")
	writeSkill(t, active, "extras/hidden", "hidden", "shadowed duplicate")
	skills := NewClientSkills(active, []string{"extras"})
	engine := New(nil, assistant.SendOptions{}, WithClientSkills(skills))
	visible := mustClientSkills(t, engine)
	if len(visible) != 2 || visible[0].Name != "hidden" || visible[0].Description != "user only" {
		t.Fatalf("explicitly invokable skills = %+v", visible)
	}
	got := skills.snapshot(context.Background())
	if !strings.Contains(got, `name="visible"`) {
		t.Fatal("missing visible skill")
	}
	if strings.Contains(got, `name="hidden"`) || strings.Contains(got, "user only") || strings.Contains(got, "shadowed duplicate") {
		t.Fatalf("disabled skill reached the catalog: %s", got)
	}

	onlyHidden := t.TempDir()
	writeSkillFile(t, onlyHidden, ".agents/skills/hidden", "---\nname: hidden\ndescription: user only\ndisable-model-invocation: true\n---\n")
	if got := NewClientSkills(onlyHidden, nil).snapshot(context.Background()); got != "" {
		t.Fatalf("catalog with only disabled skills = %q, want empty", got)
	}
}

func TestClientSkillOutsideGitAndExplicitPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	outer := t.TempDir()
	active := filepath.Join(outer, "active")
	if err := os.MkdirAll(active, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, outer, ".agents/skills/parent", "parent", "not in workspace")
	writeSkill(t, active, ".agents/skills/local", "local", "local description")
	writeSkill(t, active, "extras/custom", "custom", "relative explicit directory")
	writeSkill(t, home, "extras/user", "user", "expanded home path")
	got := NewClientSkills(active, []string{"extras", "~/extras"}).snapshot(context.Background())
	if strings.Contains(got, `name="parent"`) {
		t.Fatal("discovered outside non-git workspace")
	}
	for _, name := range []string{"local", "custom", "user"} {
		if !strings.Contains(got, `name="`+name+`"`) {
			t.Fatalf("missing %s", name)
		}
	}
}

func TestClientSkillLimits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/oversize", "oversize", "too large")
	path := filepath.Join(dir, ".agents/skills/oversize/SKILL.md")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(strings.Repeat("x", skillFileLimit)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if got := NewClientSkills(dir, nil).snapshot(context.Background()); got != "" {
		t.Fatal("oversized skill accepted")
	}
	// Long escaped descriptions hit the catalog budget before the read budget.
	for i := range 100 {
		writeSkill(t, dir, fmt.Sprintf(".agents/skills/skill-%03d", i), fmt.Sprintf("skill-%03d", i), "'"+strings.Repeat("&", 1000)+"'")
	}
	got := NewClientSkills(dir, nil).snapshot(context.Background())
	if len(got) > skillCatalogLimit || !strings.HasPrefix(got, "<available-local-client-skills>") || !strings.HasSuffix(got, "</available-local-client-skills>") || !strings.Contains(got, "Do not load these local client skills with the Skill tool.") || strings.Contains(got, "<CONTEXT") || strings.Count(got, "<skill ") >= 100 {
		t.Fatalf("catalog limit not enforced: %d bytes", len(got))
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	scan := skillScan{ctx: context.Background(), remainingEntries: 2, remainingBytes: skillReadLimit, skills: map[string]registeredSkill{}}
	scan.walk(root, dir, filepath.Join(dir, ".agents/skills"), 0, map[string]bool{})
	if len(scan.skills) != 0 || scan.remainingEntries != 0 {
		t.Fatal("entry budget not enforced")
	}
	scan = skillScan{ctx: context.Background(), remainingEntries: skillEntryLimit, remainingBytes: 10, skills: map[string]registeredSkill{}}
	scan.walk(root, dir, filepath.Join(dir, ".agents/skills"), 0, map[string]bool{})
	if len(scan.skills) != 0 || scan.remainingBytes != 0 {
		t.Fatal("aggregate read budget not enforced")
	}
}

func TestClientSkillConversationSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/example", "example", "original description")
	backend := &instructionDeliveryBackend{}
	engine := New(backend, assistant.SendOptions{}, WithClientSkills(NewClientSkills(dir, nil)))
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "first"}))
	writeSkill(t, dir, ".agents/skills/example", "example", "changed description")
	for range 2 {
		drain(engine.StartTurn(context.Background(), TurnInput{Message: "retry or continue"}))
	}
	if !strings.Contains(backend.contexts[0], "original description") || backend.contexts[0] != backend.contexts[1] || backend.contexts[1] != backend.contexts[2] {
		t.Fatalf("catalog not retained: %q", backend.contexts)
	}
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "new"}))
	if !strings.Contains(backend.contexts[3], "changed description") {
		t.Fatal("new conversation did not rediscover skills")
	}
}

func TestClientSkillsOnlySentWithUserMessages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/example", "example", "example description")
	backend := &contextRecordingBackend{}
	tools, err := NewToolSet(ModeSkipPermissions, Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, assistant.SendOptions{}, WithClientSkills(NewClientSkills(dir, nil)))
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "inspect", Tools: tools, UserContext: func(context.Context) string { return "environment" }}))
	if len(backend.userContexts) != 2 || !strings.HasPrefix(backend.userContexts[0], "environment\n\n") || !strings.Contains(backend.userContexts[0], "example description") || backend.userContexts[1] != "" {
		t.Fatalf("contexts = %q", backend.userContexts)
	}
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "next", Tools: tools, UserContext: func(context.Context) string { return "environment" }}))
	if !strings.Contains(backend.userContexts[2], "example description") || strings.Contains(backend.userContexts[2], "environment") {
		t.Fatal("catalog missing or environment context repeated")
	}
}

func TestClientSkillsResumeAndEmptySnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	skills := NewClientSkills(dir, nil)
	opts := skills.apply(context.Background(), assistant.SendOptions{})
	if opts.CustomUserContext != "" {
		t.Fatal("unexpected empty catalog")
	}
	writeSkill(t, dir, ".agents/skills/example", "example", "created later")
	if got := skills.apply(context.Background(), opts); got.CustomUserContext != "" {
		t.Fatal("empty snapshot was rediscovered")
	}
	skills.reset()
	if got := skills.apply(context.Background(), assistant.SendOptions{ConversationID: "existing"}); !strings.Contains(got.CustomUserContext, "created later") {
		t.Fatalf("resumed conversation did not rescan skills: %q", got.CustomUserContext)
	}
	skills.reset()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	skills.apply(ctx, assistant.SendOptions{})
	if skills.cache.current != nil {
		t.Fatal("canceled discovery cached")
	}
	if got := skills.apply(context.Background(), assistant.SendOptions{}); !strings.Contains(got.CustomUserContext, "created later") {
		t.Fatal("reset did not discover skills")
	}
}

func TestClientSkillsResumeWithoutSkillsSupersedesOlderCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	skills := NewClientSkills(t.TempDir(), nil)
	for range 2 {
		got := skills.apply(context.Background(), assistant.SendOptions{ConversationID: "existing"})
		if got.CustomUserContext != clientSkillsRemovalNotice {
			t.Fatalf("resume without skills = %q", got.CustomUserContext)
		}
	}
	skills.reset()
	if got := skills.apply(context.Background(), assistant.SendOptions{}); got.CustomUserContext != "" {
		t.Fatalf("new conversation without skills = %q", got.CustomUserContext)
	}
}

func TestClientSkillsLinkedWorktree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	runInstructionsGit(t, repo, "init", "-b", "main")
	writeInstructionTestFile(t, repo, "README.md", "initial")
	runInstructionsGit(t, repo, "add", ".")
	runInstructionsGit(t, repo, "commit", "-m", "initial")
	linked := filepath.Join(repo, "worktrees", "linked")
	runInstructionsGit(t, repo, "worktree", "add", "-b", "linked", linked)
	writeSkill(t, repo, ".agents/skills/main", "main", "main checkout only")
	writeSkill(t, linked, ".agents/skills/linked", "linked", "linked checkout")
	active := filepath.Join(linked, "nested")
	if err := os.Mkdir(active, 0o755); err != nil {
		t.Fatal(err)
	}
	got := NewClientSkills(active, nil).snapshot(context.Background())
	if !strings.Contains(got, "linked checkout") || strings.Contains(got, "main checkout only") {
		t.Fatalf("linked catalog = %s", got)
	}
}

func TestClientSkillFileSymlinksAndDepth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	outside := t.TempDir()
	writeSkill(t, outside, "external", "external", "outside boundary")
	root := filepath.Join(dir, ".agents/skills")
	if err := os.MkdirAll(filepath.Join(root, "escape"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "external/SKILL.md"), filepath.Join(root, "escape/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, dir, "source", "internal", "inside boundary")
	if err := os.MkdirAll(filepath.Join(root, "safe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "source/SKILL.md"), filepath.Join(root, "safe/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, root, strings.Repeat("nested/", skillDepthLimit+1), "deep", "past depth limit")
	got := NewClientSkills(dir, nil).snapshot(context.Background())
	if !strings.Contains(got, "inside boundary") || strings.Contains(got, "outside boundary") || strings.Contains(got, "past depth limit") {
		t.Fatalf("catalog = %s", got)
	}
	// Explicit roots cannot follow child links out of their own boundary either.
	if got := NewClientSkills(t.TempDir(), []string{root}).snapshot(context.Background()); got != "" {
		t.Fatalf("explicit root escaped: %s", got)
	}
}

func TestClientSkillsSwitchAndStoppedToolDrain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/example", "example", "example description")
	backend := &instructionDeliveryBackend{}
	engine := New(backend, assistant.SendOptions{}, WithClientSkills(NewClientSkills(dir, nil)))
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "first"}))
	if _, _, err := engine.drainStoppedToolCalls(context.Background(), nil, assistant.SendOptions{ConversationID: engine.ConversationID()}); err != nil {
		t.Fatal(err)
	}
	if backend.contexts[1] != "" {
		t.Fatal("stopped tool drain included catalog")
	}
	if err := engine.InstallConversation(context.Background(), &Conversation{id: "existing", transcript: NewTranscript()}); err != nil {
		t.Fatal(err)
	}
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "resumed"}))
	if !strings.Contains(backend.contexts[2], "example description") {
		t.Fatalf("restored conversation did not rescan skills: %q", backend.contexts[2])
	}
}

func TestClientSkillSymlinkTargetVisitedFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".agents", "skills", "example")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstructionTestFile(t, skillDir, "INSTRUCTIONS.md", "---\nname: example\ndescription: a valid symlinked skill\n---\nInstructions")
	if err := os.Symlink("INSTRUCTIONS.md", filepath.Join(skillDir, "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	got := NewClientSkills(dir, nil).snapshot(context.Background())
	if !strings.Contains(got, `name="example"`) {
		t.Fatalf("valid skill within boundary was omitted: %q", got)
	}
}

func TestClientSkillsDeferredUntilUserMessageAfterToolResume(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/example", "example", "example description")
	tools, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "input"}, Resumable: true,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &optionsBackend{}
	skills := NewClientSkills(dir, nil)
	engine := New(backend, assistant.SendOptions{ConversationID: "conversation"}, WithClientSkills(skills))
	engine.continuation = continuationFromHistory([]assistant.Message{savedCall("one", "input")})
	drain(engine.ResumePendingTools(t.Context(), TurnInput{Tools: tools}))
	if backend.opts.CustomUserContext != "" || skills.cache.current != nil {
		t.Fatal("resumed client tool response prepared or sent the catalog")
	}
	drain(engine.StartTurn(t.Context(), TurnInput{Message: "continue", Tools: tools}))
	if !strings.Contains(backend.opts.CustomUserContext, "example description") {
		t.Fatal("next user message did not include the catalog")
	}
}

func TestClientSkillNameFallback(t *testing.T) {
	for _, tc := range []struct {
		name, frontmatter, directory, want string
		valid                              bool
	}{
		{"missing", "", "directory-name", "directory-name", true},
		{"empty", "name: ''\n", "directory-name", "directory-name", true},
		{"blank", "name: '  '\n", "directory-name", "directory-name", true},
		{"explicit", "name: explicit\n", "directory-name", "explicit", true},
		{"invalid explicit", "name: Bad_Name\n", "directory-name", "", false},
		{"invalid fallback", "", "Bad_Directory", "", false},
		{"long fallback", "", strings.Repeat("a", 65), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skill, ok := parseSkillDocument([]byte("---\n"+tc.frontmatter+"description: useful skill\n---\n"), tc.directory)
			if ok != tc.valid || ok && skill.Name != tc.want {
				t.Fatalf("name = %q, valid = %t; want %q, %t", skill.Name, ok, tc.want, tc.valid)
			}
		})
	}
}

func TestClientSkillDescriptionNormalization(t *testing.T) {
	content := "---\nname: example\ndescription: |\n  First   line\n\n  Second\tline\u00a0with\u2003spaces\n---\n"
	skill, ok := parseSkillDocument([]byte(content), "fallback")
	if !ok || skill.Description != "First line Second line with spaces" {
		t.Fatalf("description = %q, valid = %t", skill.Description, ok)
	}
}

func TestClientSkillCatalogWithFallbackAndLongDescription(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	description := strings.Repeat("word ", 300) + "end"
	writeSkillFile(t, dir, ".agents/skills/directory-name", "---\ndescription: |\n  "+description+"\n---\nbody")
	got := NewClientSkills(dir, nil).snapshot(context.Background())
	if !strings.Contains(got, `name="directory-name"`) || !strings.Contains(got, description) {
		t.Fatalf("catalog omitted fallback name or long description: %s", got)
	}
}

func TestClientSkillCatalogSkipsEntriesThatDoNotFit(t *testing.T) {
	for _, tc := range []struct {
		name             string
		firstDescription string
		wantFirst        bool
	}{
		{"larger than entire catalog", strings.Repeat("&", skillCatalogLimit/5), false},
		{"larger than remaining budget", strings.Repeat("&", 8000), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			dir := t.TempDir()
			writeSkill(t, dir, ".agents/skills/a-first", "a-first", "'"+tc.firstDescription+"'")
			writeSkill(t, dir, ".agents/skills/b-large", "b-large", "'"+strings.Repeat("&", 8000)+"'")
			writeSkill(t, dir, ".agents/skills/c-small", "c-small", "small description")
			got := NewClientSkills(dir, nil).snapshot(context.Background())
			if strings.Contains(got, `name="a-first"`) != tc.wantFirst {
				t.Fatalf("unexpected first skill inclusion: %t", !tc.wantFirst)
			}
			if !strings.Contains(got, `name="c-small"`) {
				t.Fatal("entry that did not fit suppressed the later small skill")
			}
			if tc.wantFirst && strings.Contains(got, `name="b-large"`) {
				t.Fatal("entry exceeding remaining budget was included")
			}
			if len(got) > skillCatalogLimit || !strings.HasSuffix(got, "</available-local-client-skills>") {
				t.Fatalf("invalid catalog: %d bytes", len(got))
			}
		})
	}
}

func TestClientSkillNamespacedNames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{"figma:something", true},
		{"figma-tools:some-action", true},
		{"org:figma:something", true},
		{":something", false},
		{"figma:", false},
		{"figma::something", false},
		{"figma-:something", false},
		{"figma:-something", false},
		{"figma:some--thing", false},
		{"Figma:something", false},
		{"figma:some_thing", false},
		{"figma:" + strings.Repeat("a", 59), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, fallback := range []bool{false, true} {
				content := "---\ndescription: Test skill\n"
				if !fallback {
					content += "name: " + tc.name + "\n"
				}
				skill, valid := parseSkillDocument([]byte(content+"---\nInstructions"), tc.name)
				if valid != tc.valid || valid && skill.Name != tc.name {
					t.Fatalf("fallback=%t: parsed name=%q valid=%t, want valid=%t", fallback, skill.Name, valid, tc.valid)
				}
			}
		})
	}
}

func (s *ClientSkills) snapshot(ctx context.Context) string {
	return renderClientSkills(s.discover(ctx))
}
