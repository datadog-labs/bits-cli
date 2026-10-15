package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/workspace"
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
	got := NewInstructions(nested).Snapshot(context.Background())
	absoluteRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf(`<project_instructions>
Make sure to follow these project instructions.

  <file path="%s/AGENTS.md">
root instruction
  </file>
  <file path="%s/nested/AGENTS.md">
nested instruction
  </file>
  <file path="%s/nested/active/AGENTS.md">
active instruction
  </file>
</project_instructions>
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
	ws := NewInstructions(active)
	if got := ws.Snapshot(context.Background()); got != "" {
		t.Fatalf("missing file = %q", got)
	}
	source := filepath.Join(active, "AGENTS.md")
	if err := os.Symlink(filepath.Join(outside, "AGENTS.md"), source); err != nil {
		t.Fatal(err)
	}
	if got := ws.Snapshot(context.Background()); !strings.Contains(got, "outside instruction") {
		t.Fatalf("outside symlink = %q", got)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	writeInstructionTestFile(t, active, "instructions.txt", "inside instruction")
	if err := os.Symlink(filepath.Join(active, "instructions.txt"), source); err != nil {
		t.Fatal(err)
	}
	if got := ws.Snapshot(context.Background()); !strings.Contains(got, "inside instruction") || strings.Contains(got, "outside instruction") {
		t.Fatalf("safe symlink = %q", got)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ws.Snapshot(context.Background()); got != "" {
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
	if got := ws.Snapshot(context.Background()); got != "" {
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
	got := NewInstructions(dir).Snapshot(context.Background())
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
	got := NewInstructions(linked).Snapshot(context.Background())
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
				instructions := NewInstructions(dir)
				got := instructions.Snapshot(context.Background())
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
		got := NewInstructions(dir).Snapshot(context.Background())
		if !strings.Contains(got, "fallback") {
			t.Fatalf("fallback = %q", got)
		}
	})
	t.Run("empty override suppresses alternatives", func(t *testing.T) {
		dir := t.TempDir()
		writeInstructionTestFile(t, dir, "AGENTS.override.md", "")
		writeInstructionTestFile(t, dir, "AGENTS.md", "ignored")
		if got := NewInstructions(dir).Snapshot(context.Background()); got != "" {
			t.Fatalf("empty override = %q", got)
		}
	})
	t.Run("BOM stripped", func(t *testing.T) {
		dir := t.TempDir()
		writeInstructionTestFile(t, dir, "CLAUDE.MD", "\ufeffuse the formatter")
		got := NewInstructions(dir).Snapshot(context.Background())
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

func TestInstructionsStopAtProjectRoot(t *testing.T) {
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
			instructions := NewInstructions(active)
			got := instructions.Snapshot(context.Background())
			repository := strings.Index(got, "repository content")
			if strings.Contains(got, "ancestor content") || (repository >= 0) != git || repository > strings.Index(got, "local content") {
				t.Fatalf("project instructions = %q", got)
			}
		})
	}
}

func TestNestedWorktreeInstructionsStopAtWorktreeRoot(t *testing.T) {
	for _, tc := range []struct {
		name, mainFile, worktreeFile string
	}{
		{"duplicate", "AGENTS.md", "AGENTS.md"},
		{"no worktree file", "AGENTS.md", ""},
		{"different filename", "CLAUDE.md", "AGENTS.md"},
		{"override", "AGENTS.override.md", "AGENTS.override.md"},
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
			got := NewInstructions(nested).Snapshot(context.Background())
			if strings.Contains(got, "main content") || strings.Contains(got, "outer content") || strings.Contains(got, "linked content") != (tc.worktreeFile != "") {
				t.Fatalf("worktree context = %q", got)
			}
		})
	}
}

func TestBareWorktreeExcludesContainerInstructions(t *testing.T) {
	outer := t.TempDir()
	bare := filepath.Join(outer, ".bare")
	runInstructionsGit(t, outer, "init", "--bare", bare)
	linked := filepath.Join(outer, "main")
	runInstructionsGit(t, outer, "--git-dir="+bare, "worktree", "add", "--orphan", "-b", "main", linked)
	writeInstructionTestFile(t, outer, "AGENTS.md", "container content")
	writeInstructionTestFile(t, linked, "AGENTS.md", "linked content")
	got := NewInstructions(linked).Snapshot(context.Background())
	if strings.Contains(got, "container content") || !strings.Contains(got, "linked content") {
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
	got := NewInstructions(project).Snapshot(context.Background())
	if strings.Contains(got, "unofficial global instructions") || !strings.Contains(got, "project instructions") {
		t.Fatalf("context = %q", got)
	}
}

func TestRenderLocalEnvironment(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-b", "feature/<x>").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })

	shell := ""
	if path := ws.DefaultShellPath(); path != "" {
		shell = "\n  <shell>" + filepath.Base(path) + "</shell>"
	}
	want := `<local_environment>
  <workspace>` + dir + `</workspace>
  <git>
    <root>` + dir + `</root>
    <branch>feature/&lt;x&gt;</branch>
  </git>` + shell + `
  <platform>` + runtime.GOOS + "/" + runtime.GOARCH + `</platform>
</local_environment>`
	if got := renderLocalEnvironment(context.Background(), ws); got != want {
		t.Fatalf("rendered:\n%s\nwant:\n%s", got, want)
	}
}
