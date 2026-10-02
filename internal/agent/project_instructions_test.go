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
	if supportsCaseSensitiveNames(t, nested) {
		writeInstructionTestFile(t, nested, "agents.md", "wrong case")
	}
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got := NewProjectInstructions(nested).snapshot(context.Background())
	absoluteRepo, err := filepath.Abs(repo)
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
`, filepath.ToSlash(absoluteRepo), filepath.ToSlash(absoluteRepo), filepath.ToSlash(absoluteRepo))
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
	ws := NewProjectInstructions(active)
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
	got := NewProjectInstructions(dir).snapshot(context.Background())
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
	got := NewProjectInstructions(linked).snapshot(context.Background())
	if !strings.Contains(got, "linked instruction") || strings.Contains(got, "main instruction") {
		t.Fatalf("linked instructions = %q", got)
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

func TestProjectInstructionsEmptySnapshotLoadsOnce(t *testing.T) {
	dir := t.TempDir()
	instructions := NewProjectInstructions(dir)
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
			instructions := NewProjectInstructions(dir)
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
	instructions := NewProjectInstructions(nested)
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

func TestInstructionFilenamePrecedence(t *testing.T) {
	t.Run("case-sensitive filename precedence", func(t *testing.T) {
		if !supportsCaseSensitiveNames(t, t.TempDir()) {
			t.Skip("filename precedence includes case-only variants, unavailable on this filesystem")
		}
		for i, name := range instructionFilenames {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				for _, candidate := range instructionFilenames[i:] {
					writeInstructionTestFile(t, dir, candidate, "selected "+candidate)
				}
				instructions := NewProjectInstructions(dir)
				got := instructions.snapshot(context.Background())
				if strings.Count(got, "<file path=") != 1 || !strings.Contains(got, "selected "+name+"\n") {
					t.Fatalf("selected file = %q", got)
				}
			})
		}
	})
	t.Run("directory candidates fall back", func(t *testing.T) {
		dir := t.TempDir()
		// These are distinct on both case-sensitive and case-insensitive filesystems.
		for _, name := range []string{"AGENTS.override.md", "AGENTS.md"} {
			if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeInstructionTestFile(t, dir, "CLAUDE.md", "fallback")
		got := NewProjectInstructions(dir).snapshot(context.Background())
		if !strings.Contains(got, "fallback") {
			t.Fatalf("fallback = %q", got)
		}
	})
	t.Run("empty override suppresses alternatives", func(t *testing.T) {
		dir := t.TempDir()
		writeInstructionTestFile(t, dir, "AGENTS.override.md", "")
		writeInstructionTestFile(t, dir, "AGENTS.md", "ignored")
		if got := NewProjectInstructions(dir).snapshot(context.Background()); got != "" {
			t.Fatalf("empty override = %q", got)
		}
	})
	t.Run("BOM stripped", func(t *testing.T) {
		dir := t.TempDir()
		writeInstructionTestFile(t, dir, "CLAUDE.MD", "\ufeffuse the formatter")
		got := NewProjectInstructions(dir).snapshot(context.Background())
		if strings.Contains(got, "\ufeff") || !strings.Contains(got, "use the formatter") {
			t.Fatalf("BOM context = %q", got)
		}
	})
}

// supportsCaseSensitiveNames detects whether a test directory can hold two
// distinct files whose names differ only by case. Some macOS volumes are
// case-insensitive, so tests of filename precedence must be gated by the
// actual filesystem rather than GOOS.
func supportsCaseSensitiveNames(t *testing.T, dir string) bool {
	t.Helper()
	upper := filepath.Join(dir, "case-probe")
	lower := filepath.Join(dir, "CASE-PROBE")
	if err := os.WriteFile(upper, []byte("upper"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lower, []byte("lower"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(upper)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(lower)
	if err != nil {
		t.Fatal(err)
	}
	return string(first) == "upper" && string(second) == "lower"
}

func TestAncestorInstructions(t *testing.T) {
	for _, git := range []bool{false, true} {
		t.Run(fmt.Sprintf("git=%t", git), func(t *testing.T) {
			outer := t.TempDir()
			repo := filepath.Join(outer, "repo")
			active := filepath.Join(repo, "nested")
			if err := os.MkdirAll(active, 0o755); err != nil {
				t.Fatal(err)
			}
			if git {
				runInstructionsGit(t, repo, "init", "-b", "main")
			}
			writeInstructionTestFile(t, outer, "AGENTS.md", "ancestor content")
			writeInstructionTestFile(t, repo, "AGENTS.MD", "repository content")
			writeInstructionTestFile(t, active, "AGENTS.override.md", "local content")
			instructions := NewProjectInstructions(active)
			got := instructions.snapshot(context.Background())
			previous := -1
			for _, content := range []string{"ancestor content", "repository content", "local content"} {
				index := strings.Index(got, content)
				if index <= previous {
					t.Fatalf("incorrect ordering for %q: %q", content, got)
				}
				previous = index
			}
		})
	}
}

func TestNestedWorktreeInstructionShadowing(t *testing.T) {
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
			got := NewProjectInstructions(nested).snapshot(context.Background())
			if strings.Contains(got, "main content") != tc.keepMain || !strings.Contains(got, "outer content") || (tc.worktreeFile != "" && !strings.Contains(got, "linked content")) {
				t.Fatalf("worktree context = %q", got)
			}
		})
	}
}

func TestBareWorktreeKeepsContainerInstructions(t *testing.T) {
	outer := t.TempDir()
	bare := filepath.Join(outer, ".bare")
	runInstructionsGit(t, outer, "init", "--bare", bare)
	linked := filepath.Join(outer, "main")
	runInstructionsGit(t, outer, "--git-dir="+bare, "worktree", "add", "--orphan", "-b", "main", linked)
	writeInstructionTestFile(t, outer, "AGENTS.md", "container content")
	writeInstructionTestFile(t, linked, "AGENTS.md", "linked content")
	got := NewProjectInstructions(linked).snapshot(context.Background())
	if !strings.Contains(got, "container content") || !strings.Contains(got, "linked content") {
		t.Fatalf("bare layout context = %q", got)
	}
}

func TestInstructionsDoNotDiscoverBitsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	config := filepath.Join(home, ".bits-cli")
	project := filepath.Join(home, "project")
	for _, dir := range []string{config, project} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeInstructionTestFile(t, config, "AGENTS.md", "unofficial global instructions")
	writeInstructionTestFile(t, project, "AGENTS.md", "project instructions")
	got := NewProjectInstructions(project).snapshot(context.Background())
	if strings.Contains(got, "unofficial global instructions") || !strings.Contains(got, "project instructions") {
		t.Fatalf("context = %q", got)
	}
}
