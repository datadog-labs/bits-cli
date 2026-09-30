package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

func TestInstructionsHierarchy(t *testing.T) {
	repo := t.TempDir()
	runInstructionsGit(t, repo, "init", "-b", "main")
	nested := filepath.Join(repo, "nested", "active")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstructionTestFile(t, repo, "AGENTS.md", "root instruction")
	writeInstructionTestFile(t, filepath.Dir(nested), "AGENTS.md", "nested instruction")
	writeInstructionTestFile(t, nested, "AGENTS.md", "active instruction")
	writeInstructionTestFile(t, nested, "agents.md", "wrong case")
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got := NewProjectInstructionsManager(nested).snapshot(context.Background())
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf(`# Project-Specific Context
Make sure to follow the instructions in the context below

<project_context>
  <file path="%s/AGENTS.md">
root instruction
  </file>
  <file path="%s/nested/AGENTS.md">
nested instruction
  </file>
  <file path="%s/nested/active/AGENTS.md">
active instruction
  </file>
</project_context>
`, filepath.ToSlash(resolvedRepo), filepath.ToSlash(resolvedRepo), filepath.ToSlash(resolvedRepo))
	if got != expected {
		t.Fatalf("instructions = %q, want %q", got, expected)
	}
	after, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("working directory changed: %q -> %q", before, after)
	}
}

func TestInstructionsNonGitAndUnsafeFiles(t *testing.T) {
	parent := t.TempDir()
	active := filepath.Join(parent, "active")
	if err := os.Mkdir(active, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeInstructionTestFile(t, outside, "AGENTS.md", "outside instruction")
	ws := NewProjectInstructionsManager(active)
	if got := ws.snapshot(context.Background()); got != "" {
		t.Fatalf("missing file = %q", got)
	}
	source := filepath.Join(active, "AGENTS.md")
	if err := os.Symlink(filepath.Join(outside, "AGENTS.md"), source); err != nil {
		t.Fatal(err)
	}
	if got := ws.snapshot(context.Background()); got != "" {
		t.Fatalf("escaping symlink = %q", got)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	writeInstructionTestFile(t, active, "instructions.txt", "inside instruction")
	if err := os.Symlink(filepath.Join(active, "instructions.txt"), source); err != nil {
		t.Fatal(err)
	}
	if got := ws.snapshot(context.Background()); !strings.Contains(got, "inside instruction") || strings.Contains(got, "outside instruction") {
		t.Fatalf("safe symlink = %q", got)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ws.snapshot(context.Background()); got != "" {
		t.Fatalf("directory = %q", got)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("unreadable instruction"), 0o000); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(source); err == nil {
		_ = f.Close()
		t.Skip("process can read mode 0000 files")
	}
	if got := ws.snapshot(context.Background()); got != "" {
		t.Fatalf("unreadable file = %q", got)
	}
}

func TestInstructionsLimits(t *testing.T) {
	repo := t.TempDir()
	runInstructionsGit(t, repo, "init", "-b", "main")
	dir := repo
	for range 5 {
		writeInstructionTestFile(t, dir, "AGENTS.md", strings.Repeat("x", instructionFileLimit+1))
		dir = filepath.Join(dir, "nested")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := NewProjectInstructionsManager(dir).snapshot(context.Background())
	if strings.Count(got, strings.Repeat("x", instructionFileLimit)) != instructionTotalLimit/instructionFileLimit {
		t.Fatal("unexpected number of bounded file contents")
	}
	if strings.Count(got, "[Truncated:") != 4 || !strings.Contains(got, "aggregate byte limit reached") {
		t.Fatalf("missing truncation notices")
	}
}

func TestInstructionsLinkedWorktree(t *testing.T) {
	repo := t.TempDir()
	runInstructionsGit(t, repo, "init", "-b", "main")
	writeInstructionTestFile(t, repo, "AGENTS.md", "main instruction")
	runInstructionsGit(t, repo, "add", ".")
	runInstructionsGit(t, repo, "-c", "user.name=Bits Test", "-c", "user.email=bits@example.com", "commit", "-m", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	runInstructionsGit(t, repo, "worktree", "add", "-b", "linked", linked)
	writeInstructionTestFile(t, linked, "AGENTS.md", "linked instruction")
	got := NewProjectInstructionsManager(linked).snapshot(context.Background())
	if !strings.Contains(got, "linked instruction") || strings.Contains(got, "main instruction") {
		t.Fatalf("linked instructions = %q", got)
	}
}

func runInstructionsGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
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

func TestProjectInstructionsManagerEmptySnapshotLoadsOnce(t *testing.T) {
	dir := t.TempDir()
	manager := NewProjectInstructionsManager(dir)
	opts := assistant.SendOptions{CustomUserContext: "existing"}
	first := manager.apply(context.Background(), opts)
	if first.CustomUserContext != "existing" {
		t.Fatalf("empty snapshot = %q", first.CustomUserContext)
	}
	writeInstructionTestFile(t, dir, "AGENTS.md", "created later")
	second := manager.apply(context.Background(), opts)
	if second.CustomUserContext != "existing" {
		t.Fatalf("snapshot was reloaded: %q", second.CustomUserContext)
	}
	manager.reset()
	third := manager.apply(context.Background(), opts)
	if !strings.Contains(third.CustomUserContext, "created later") {
		t.Fatalf("reset did not reload: %q", third.CustomUserContext)
	}
	if opts.CustomUserContext != "existing" {
		t.Fatalf("default context mutated: %q", opts.CustomUserContext)
	}
}

func TestProjectInstructionsManagerInstructionUpdates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before string
		after  string
		resume bool
		want   string
	}{
		{name: "unchanged", before: "old", after: "old"},
		{name: "replaced", before: "old", after: "new", want: instructionsReplacementNotice},
		{name: "removed", before: "old", want: instructionsRemovalNotice},
		{name: "cleared", before: "old", after: " \n\t", want: instructionsRemovalNotice},
		{name: "added", after: "new", want: "# Project-Specific Context"},
		{name: "still absent"},
		{name: "resumed unknown with instructions", resume: true, after: "new", want: instructionsReplacementNotice},
		{name: "resumed unknown without instructions", resume: true, want: instructionsRemovalNotice},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			manager := NewProjectInstructionsManager(dir)
			ctx := context.Background()
			if !tc.resume {
				if tc.before != "" {
					writeInstructionTestFile(t, dir, "AGENTS.md", tc.before)
				}
				manager.apply(ctx, assistant.SendOptions{})
				manager.refresh()
			}
			if tc.after != "" {
				writeInstructionTestFile(t, dir, "AGENTS.md", tc.after)
			} else if tc.before != "" {
				if err := os.Remove(filepath.Join(dir, "AGENTS.md")); err != nil {
					t.Fatal(err)
				}
			}
			opts := assistant.SendOptions{ConversationID: "conversation", CustomUserContext: "existing"}
			got := manager.apply(ctx, opts).CustomUserContext
			if tc.want == "" {
				if got != "existing" {
					t.Fatalf("unexpected update = %q", got)
				}
			} else if !strings.HasPrefix(got, "existing\n\n"+tc.want) {
				t.Fatalf("update = %q, want notice %q", got, tc.want)
			}
			if tc.want != "" && strings.TrimSpace(tc.after) != "" && !strings.Contains(got, "\n"+tc.after+"\n") {
				t.Fatalf("missing new contents in %q", got)
			}
			if next := manager.apply(ctx, opts).CustomUserContext; next != "existing" {
				t.Fatalf("continuation repeated update: %q", next)
			}
			manager.refresh()
			if next := manager.apply(ctx, opts).CustomUserContext; next != "existing" {
				t.Fatalf("unchanged snapshot repeated update: %q", next)
			}
		})
	}
}

func TestProjectInstructionsManagerCascadingReplacement(t *testing.T) {
	repo := t.TempDir()
	runInstructionsGit(t, repo, "init", "-b", "main")
	nested := filepath.Join(repo, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstructionTestFile(t, repo, "AGENTS.md", "root instructions")
	writeInstructionTestFile(t, nested, "AGENTS.md", "nested instructions")
	manager := NewProjectInstructionsManager(nested)
	manager.apply(context.Background(), assistant.SendOptions{})
	if err := os.Remove(filepath.Join(nested, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	manager.refresh()
	got := manager.apply(context.Background(), assistant.SendOptions{ConversationID: "conversation"}).CustomUserContext
	if !strings.HasPrefix(got, instructionsReplacementNotice) || !strings.Contains(got, "root instructions") || strings.Contains(got, "nested instructions") {
		t.Fatalf("remaining cascade = %q", got)
	}
}

func TestPiInstructionFilenamePrecedence(t *testing.T) {
	for i, name := range instructionFilenames {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for _, candidate := range instructionFilenames[i:] {
				writeInstructionTestFile(t, dir, candidate, "selected "+candidate)
			}
			manager := NewProjectInstructionsManager(dir)
			manager.globalDirectory = ""
			got := manager.snapshot(context.Background())
			if strings.Count(got, "<file path=") != 1 || !strings.Contains(got, "selected "+name+"\n") {
				t.Fatalf("selected file = %q", got)
			}
		})
	}
	t.Run("directory candidates fall back", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range instructionFilenames[:3] {
			if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeInstructionTestFile(t, dir, "CLAUDE.md", "fallback")
		got := NewProjectInstructionsManager(dir).snapshot(context.Background())
		if !strings.Contains(got, "fallback") {
			t.Fatalf("fallback = %q", got)
		}
	})
	t.Run("empty override suppresses alternatives", func(t *testing.T) {
		dir := t.TempDir()
		writeInstructionTestFile(t, dir, "AGENTS.override.md", "")
		writeInstructionTestFile(t, dir, "AGENTS.md", "ignored")
		if got := NewProjectInstructionsManager(dir).snapshot(context.Background()); got != "" {
			t.Fatalf("empty override = %q", got)
		}
	})
	t.Run("BOM stripped", func(t *testing.T) {
		dir := t.TempDir()
		writeInstructionTestFile(t, dir, "CLAUDE.MD", "\ufeffuse the formatter")
		got := NewProjectInstructionsManager(dir).snapshot(context.Background())
		if strings.Contains(got, "\ufeff") || !strings.Contains(got, "use the formatter") {
			t.Fatalf("BOM context = %q", got)
		}
	})
}

func TestPiGlobalAndAncestorInstructions(t *testing.T) {
	for _, git := range []bool{false, true} {
		t.Run(fmt.Sprintf("git=%t", git), func(t *testing.T) {
			global := t.TempDir()
			outer := t.TempDir()
			repo := filepath.Join(outer, "repo")
			active := filepath.Join(repo, "nested")
			if err := os.MkdirAll(active, 0o755); err != nil {
				t.Fatal(err)
			}
			if git {
				runInstructionsGit(t, repo, "init", "-b", "main")
			}
			writeInstructionTestFile(t, global, "CLAUDE.md", "global content")
			writeInstructionTestFile(t, outer, "AGENTS.md", "ancestor content")
			writeInstructionTestFile(t, repo, "AGENTS.MD", "repository content")
			writeInstructionTestFile(t, active, "AGENTS.override.md", "local content")
			manager := NewProjectInstructionsManager(active)
			manager.globalDirectory = global
			got := manager.snapshot(context.Background())
			previous := -1
			for _, content := range []string{"global content", "ancestor content", "repository content", "local content"} {
				index := strings.Index(got, content)
				if index <= previous {
					t.Fatalf("incorrect ordering for %q: %q", content, got)
				}
				previous = index
			}
			// A global directory that is also an ancestor is included only once.
			manager.globalDirectory = outer
			got = manager.snapshot(context.Background())
			if strings.Count(got, "ancestor content") != 1 {
				t.Fatalf("duplicate global context = %q", got)
			}
		})
	}
}

func TestPiNestedWorktreeInstructionShadowing(t *testing.T) {
	for _, tc := range []struct {
		name, mainFile, worktreeFile string
		keepMain                     bool
	}{
		{"duplicate", "AGENTS.md", "AGENTS.md", false},
		{"no worktree file", "AGENTS.md", "", true},
		{"different filename", "CLAUDE.md", "AGENTS.md", true},
		{"override", "AGENTS.override.md", "AGENTS.override.md", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outer := t.TempDir()
			main := filepath.Join(outer, "main")
			if err := os.Mkdir(main, 0o755); err != nil {
				t.Fatal(err)
			}
			runInstructionsGit(t, main, "init", "-b", "main")
			runInstructionsGit(t, main, "-c", "user.name=Bits Test", "-c", "user.email=bits@example.com", "commit", "--allow-empty", "-m", "initial")
			linked := filepath.Join(main, "worktrees", "linked")
			runInstructionsGit(t, main, "worktree", "add", "-b", "linked", linked)
			writeInstructionTestFile(t, outer, "AGENTS.md", "outer content")
			writeInstructionTestFile(t, main, tc.mainFile, "main content")
			if tc.worktreeFile != "" {
				writeInstructionTestFile(t, linked, tc.worktreeFile, "linked content")
			}
			nested := filepath.Join(linked, "nested")
			if err := os.Mkdir(nested, 0o755); err != nil {
				t.Fatal(err)
			}
			got := NewProjectInstructionsManager(nested).snapshot(context.Background())
			if strings.Contains(got, "main content") != tc.keepMain || !strings.Contains(got, "outer content") || (tc.worktreeFile != "" && !strings.Contains(got, "linked content")) {
				t.Fatalf("worktree context = %q", got)
			}
		})
	}
}

func TestPiBareWorktreeKeepsContainerInstructions(t *testing.T) {
	outer := t.TempDir()
	bare := filepath.Join(outer, ".bare")
	runInstructionsGit(t, outer, "init", "--bare", bare)
	linked := filepath.Join(outer, "main")
	runInstructionsGit(t, outer, "--git-dir="+bare, "worktree", "add", "--orphan", "-b", "main", linked)
	writeInstructionTestFile(t, outer, "AGENTS.md", "container content")
	writeInstructionTestFile(t, linked, "AGENTS.md", "linked content")
	got := NewProjectInstructionsManager(linked).snapshot(context.Background())
	if !strings.Contains(got, "container content") || !strings.Contains(got, "linked content") {
		t.Fatalf("bare layout context = %q", got)
	}
}

func TestPiGlobalDirectoryUsesBitsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	global := filepath.Join(home, ".bits-cli")
	if err := os.Mkdir(global, 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstructionTestFile(t, global, "AGENTS.override.md", "global override content")
	writeInstructionTestFile(t, global, "AGENTS.md", "unused global content")
	manager := NewProjectInstructionsManager(t.TempDir())
	if manager.globalDirectory != global {
		t.Fatalf("global directory = %q", manager.globalDirectory)
	}
	got := manager.snapshot(context.Background())
	if !strings.Contains(got, "global override content") || strings.Contains(got, "unused global content") {
		t.Fatalf("global instructions = %q", got)
	}
}
