package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/DataDog/bits-cli/internal/agent"
)

func TestListFilesTool(t *testing.T) {
	ctx := context.Background()

	t.Run("absolute path rejected", func(t *testing.T) {
		tool := newListFilesTool(fstest.MapFS{}, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"path":"/tmp"}`})
		if err != nil {
			t.Fatal(err)
		}
		assertError(t, result, "absolute")
	})

	t.Run("canceled context stops before walking", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		tool := newListFilesTool(fstest.MapFS{"f.go": {Data: []byte("x")}}, "/workspace")
		_, err := tool.Handler(ctx, agent.ToolCall{Input: `{}`})
		if err != context.Canceled {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})

	t.Run("parent traversal rejected by os.Root", func(t *testing.T) {
		dir := t.TempDir()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		tool := newListFilesTool(root.FS(), dir)
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"path":"../../etc"}`})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			t.Fatal("expected error for path traversal")
		}
	})

	t.Run("file as path rejected", func(t *testing.T) {
		fsys := fstest.MapFS{"main.go": {Data: []byte("package main")}}
		tool := newListFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"path":"main.go"}`})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			t.Fatal("expected error for file path")
		}
	})

	t.Run("flat listing is alphabetical with dir suffix", func(t *testing.T) {
		fsys := fstest.MapFS{
			"b.go":     {Data: []byte("x")},
			"a.go":     {Data: []byte("x")},
			"src/f.go": {Data: []byte("x")},
		}
		tool := newListFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		lines := nonEmpty(strings.Split(result.Output, "\n"))
		// src/ should appear; a.go and b.go too; depth=1 so src/f.go not shown
		if !contains(lines, "src/") {
			t.Errorf("expected src/ in output: %v", lines)
		}
		if !contains(lines, "a.go") || !contains(lines, "b.go") {
			t.Errorf("expected a.go and b.go: %v", lines)
		}
		if contains(lines, "src/f.go") {
			t.Errorf("depth=1 must not recurse into src/: %v", lines)
		}
		if idx("a.go", lines) > idx("b.go", lines) {
			t.Error("entries must be alphabetical")
		}
	})

	t.Run("depth=2 recurses one level", func(t *testing.T) {
		fsys := fstest.MapFS{
			"src/main.go":     {Data: []byte("x")},
			"src/sub/deep.go": {Data: []byte("x")},
		}
		tool := newListFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"depth":2}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		lines := nonEmpty(strings.Split(result.Output, "\n"))
		if !contains(lines, "src/main.go") {
			t.Errorf("depth=2 must include src/main.go: %v", lines)
		}
		if contains(lines, "src/sub/deep.go") {
			t.Errorf("depth=2 must not include depth-3 file: %v", lines)
		}
	})

	t.Run("depth below minimum rejected", func(t *testing.T) {
		tool := newListFilesTool(fstest.MapFS{"f.go": {Data: []byte("x")}}, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"depth":0}`})
		if err != nil {
			t.Fatal(err)
		}
		assertError(t, result, "depth must be between")
	})

	t.Run("depth above maximum rejected", func(t *testing.T) {
		tool := newListFilesTool(fstest.MapFS{"f.go": {Data: []byte("x")}}, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"depth":11}`})
		if err != nil {
			t.Fatal(err)
		}
		assertError(t, result, "depth must be between")
	})

	t.Run(".git directory is always skipped", func(t *testing.T) {
		fsys := fstest.MapFS{
			".git/config": {Data: []byte("x")},
			"main.go":     {Data: []byte("x")},
		}
		tool := newListFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"depth":2}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if strings.Contains(result.Output, ".git") {
			t.Errorf(".git must not appear in output: %s", result.Output)
		}
	})

	t.Run("gitignore patterns are respected", func(t *testing.T) {
		fsys := fstest.MapFS{
			".gitignore": {Data: []byte("*.log\nbuild/\n")},
			"main.go":    {Data: []byte("x")},
			"error.log":  {Data: []byte("x")},
			"build/out":  {Data: []byte("x")},
		}
		tool := newListFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"depth":2}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if strings.Contains(result.Output, "error.log") {
			t.Errorf("*.log must be excluded: %s", result.Output)
		}
		if strings.Contains(result.Output, "build") {
			t.Errorf("build/ must be excluded: %s", result.Output)
		}
		if !strings.Contains(result.Output, "main.go") {
			t.Errorf("main.go must be present: %s", result.Output)
		}
	})

	t.Run("root gitignore applies when listing a subdirectory", func(t *testing.T) {
		fsys := fstest.MapFS{
			".gitignore":      {Data: []byte("src/secret.txt\n")},
			"src/secret.txt":  {Data: []byte("secret")},
			"src/visible.txt": {Data: []byte("visible")},
		}
		tool := newListFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"path":"src"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if strings.Contains(result.Output, "secret.txt") || !strings.Contains(result.Output, "visible.txt") {
			t.Errorf("root ignore rules must apply to scoped listings: %s", result.Output)
		}
	})

	t.Run("relative path syntax is normalized", func(t *testing.T) {
		fsys := fstest.MapFS{"src/visible.txt": {Data: []byte("visible")}}
		tool := newListFilesTool(fsys, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{"path":"./src/"}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		if result.Title != "src" || result.Output != "visible.txt" {
			t.Errorf("result = (%q, %q), want (src, visible.txt)", result.Title, result.Output)
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		// MapFS requires a file to represent a directory; use a gitignored file so the dir appears empty.
		fsys2 := fstest.MapFS{
			".gitignore":  {Data: []byte(".keep\n")},
			"empty/.keep": {Data: []byte{}},
		}
		tool := newListFilesTool(fsys2, "/workspace")
		result, err := tool.Handler(ctx, agent.ToolCall{Input: `{}`})
		if err != nil || result.IsError {
			t.Fatalf("unexpected error: %v %s", err, result.Output)
		}
		// empty/ dir itself should appear (depth=1), but .keep is hidden
		_ = result
	})
}

func contains(lines []string, s string) bool {
	for _, l := range lines {
		if l == s {
			return true
		}
	}
	return false
}

func idx(s string, lines []string) int {
	for i, l := range lines {
		if l == s {
			return i
		}
	}
	return -1
}

func nonEmpty(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
