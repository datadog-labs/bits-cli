package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/filediff"
)

// newWriteTool opens dir as a workspace root and returns the write_file tool
// plus the underlying root for on-disk assertions.
func newWriteTool(t *testing.T, dir string) (agent.Tool, *os.Root) {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return newWriteFileTool(r, dir, newMutationLocker()), r
}

func invokeWrite(t *testing.T, tool agent.Tool, input any) agent.ToolResult {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Handler(context.Background(), agent.ToolCall{ID: "w", Name: toolWriteFile, Input: string(data)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestWriteFileTool(t *testing.T) {
	t.Run("creates a new file", func(t *testing.T) {
		dir := t.TempDir()
		tool, _ := newWriteTool(t, dir)
		r := invokeWrite(t, tool, map[string]any{"path": "hello.txt", "content": "hi there"})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		got, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "hi there" {
			t.Errorf("content = %q, want %q", got, "hi there")
		}
		if !strings.Contains(r.Output, "8 bytes") {
			t.Errorf("output missing byte count: %s", r.Output)
		}
	})

	t.Run("returns handler-authoritative rendered state", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("before\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		tool, _ := newWriteTool(t, dir)
		result := invokeWrite(t, tool, map[string]any{"path": "f.txt", "content": "after\n"})
		if result.RenderState == nil {
			t.Fatal("RenderState = nil, want applied state")
		}
		state, ok := result.RenderState.State.(*filediff.State)
		if !ok || state.Phase != filediff.PhaseApplied || state.Change == nil || state.Change.Op != filediff.OpOverwrite || state.Change.Diff == nil || state.Change.Diff.Additions != 1 || state.Change.Diff.Deletions != 1 {
			t.Fatalf("RenderState = %#v, want applied overwrite", result.RenderState.State)
		}
		if !strings.HasPrefix(result.Display, "--- a/f.txt\n+++ b/f.txt\n") {
			t.Fatalf("Display = %q, want a raw unified diff", result.Display)
		}
	})

	t.Run("overwrites an existing file completely", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("old long content"), 0o600); err != nil {
			t.Fatal(err)
		}
		tool, _ := newWriteTool(t, dir)
		if r := invokeWrite(t, tool, map[string]any{"path": "f.txt", "content": "new"}); r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
		if string(got) != "new" {
			t.Errorf("content = %q, want %q", got, "new")
		}
	})

	t.Run("overwrite preserves file mode", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "script.sh")
		if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		tool, _ := newWriteTool(t, dir)
		if r := invokeWrite(t, tool, map[string]any{"path": "script.sh", "content": "#!/bin/sh\necho hi\n"}); r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("mode = %o, want 0755", info.Mode().Perm())
		}
	})

	t.Run("creates missing parent directories", func(t *testing.T) {
		dir := t.TempDir()
		tool, _ := newWriteTool(t, dir)
		if r := invokeWrite(t, tool, map[string]any{"path": "a/b/c/deep.txt", "content": "x"}); r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		if _, err := os.Stat(filepath.Join(dir, "a", "b", "c", "deep.txt")); err != nil {
			t.Fatalf("nested file not created: %v", err)
		}
	})

	t.Run("empty path rejected", func(t *testing.T) {
		tool, _ := newWriteTool(t, t.TempDir())
		assertError(t, invokeWrite(t, tool, map[string]any{"path": "", "content": "x"}), "must not be empty")
	})

	t.Run("absent content is rejected and leaves the file intact", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "important.txt"), []byte("precious"), 0o600); err != nil {
			t.Fatal(err)
		}
		tool, _ := newWriteTool(t, dir)
		// No "content" key: a malformed call must not truncate the file to empty.
		assertError(t, invokeWrite(t, tool, map[string]any{"path": "important.txt"}), "content is required")
		if got, _ := os.ReadFile(filepath.Join(dir, "important.txt")); string(got) != "precious" {
			t.Errorf("file was modified: %q", got)
		}
	})

	t.Run("explicit empty content writes an empty file", func(t *testing.T) {
		dir := t.TempDir()
		tool, _ := newWriteTool(t, dir)
		if r := invokeWrite(t, tool, map[string]any{"path": "empty.txt", "content": ""}); r.IsError {
			t.Fatalf("empty content should be a valid write: %s", r.Output)
		}
		info, err := os.Stat(filepath.Join(dir, "empty.txt"))
		if err != nil {
			t.Fatalf("empty file not created: %v", err)
		}
		if info.Size() != 0 {
			t.Errorf("size = %d, want 0", info.Size())
		}
	})

	t.Run("absolute path rejected", func(t *testing.T) {
		tool, _ := newWriteTool(t, t.TempDir())
		assertError(t, invokeWrite(t, tool, map[string]any{"path": "/etc/passwd", "content": "x"}), "absolute")
	})

	t.Run("directory target rejected", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		tool, _ := newWriteTool(t, dir)
		assertError(t, invokeWrite(t, tool, map[string]any{"path": "sub", "content": "x"}), "is a directory")
	})

	t.Run("parent traversal rejected by os.Root", func(t *testing.T) {
		tool, _ := newWriteTool(t, t.TempDir())
		r := invokeWrite(t, tool, map[string]any{"path": "../escape.txt", "content": "x"})
		if !r.IsError {
			t.Fatal("expected error for parent traversal")
		}
	})

	t.Run("symlink escaping the root is rejected", func(t *testing.T) {
		dir := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
		tool, _ := newWriteTool(t, dir)
		r := invokeWrite(t, tool, map[string]any{"path": "link/pwned.txt", "content": "x"})
		if !r.IsError {
			t.Fatal("expected error writing through an escaping symlink")
		}
		if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); err == nil {
			t.Fatal("write escaped the workspace root")
		}
	})

	t.Run("error-safe: failed write leaves the original intact", func(t *testing.T) {
		dir := t.TempDir()
		// "a" is a regular file, so writing "a/b.txt" must fail at MkdirAll and
		// must not disturb the existing file.
		if err := os.WriteFile(filepath.Join(dir, "a"), []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
		tool, _ := newWriteTool(t, dir)
		r := invokeWrite(t, tool, map[string]any{"path": "a/b.txt", "content": "nope"})
		if !r.IsError {
			t.Fatal("expected error writing under a file path")
		}
		got, _ := os.ReadFile(filepath.Join(dir, "a"))
		if string(got) != "original" {
			t.Errorf("original file mutated: %q", got)
		}
	})

	t.Run("cancelled context writes nothing", func(t *testing.T) {
		dir := t.TempDir()
		tool, _ := newWriteTool(t, dir)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := tool.Handler(ctx, agent.ToolCall{Name: toolWriteFile, Input: `{"path":"out.txt","content":"x"}`})
		if err != context.Canceled {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "out.txt")); !os.IsNotExist(err) {
			t.Fatalf("file exists after cancellation (err %v)", err)
		}
	})

	t.Run("no temp files remain after a write", func(t *testing.T) {
		dir := t.TempDir()
		tool, _ := newWriteTool(t, dir)
		if r := invokeWrite(t, tool, map[string]any{"path": "f.txt", "content": "x"}); r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".bits-tmp.") {
				t.Errorf("leftover temp file: %s", e.Name())
			}
		}
	})
}

func TestWriteFileDisplayPreservesExactBOMAndLineEndingChanges(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(bom+"before\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool, _ := newWriteTool(t, dir)
	result := invokeWrite(t, tool, map[string]any{"path": "f.txt", "content": "after\n"})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Output)
	}
	state, ok := result.RenderState.State.(*filediff.State)
	if !ok || state.Change == nil || state.Change.Diff == nil || state.Change.Diff.Additions != 1 || state.Change.Diff.Deletions != 1 {
		t.Fatalf("state = %#v, want exact changed diff", result.RenderState.State)
	}
	if !state.Change.Diff.BeforeFormat.BOM || state.Change.Diff.BeforeFormat.LineEnding != "crlf" || state.Change.Diff.AfterFormat.LineEnding != "lf" {
		t.Fatalf("diff format = %#v → %#v, want BOM CRLF → LF", state.Change.Diff.BeforeFormat, state.Change.Diff.AfterFormat)
	}
	foundBOM, foundCR := false, false
	for _, line := range state.Change.Diff.AllLines() {
		foundBOM = foundBOM || strings.Contains(line.Content, bom)
		foundCR = foundCR || strings.Contains(line.Content, "before")
	}
	if !foundBOM || !foundCR {
		t.Fatalf("Change.Diff = %#v, want exact preimage markers", state.Change.Diff)
	}
}

func TestWriteFileAppliedDiffKeepsAllLines(t *testing.T) {
	dir := t.TempDir()
	tool, _ := newWriteTool(t, dir)
	content := strings.Repeat("line\n", 2_001)
	result := invokeWrite(t, tool, map[string]any{"path": "f.txt", "content": content})
	if result.IsError || result.RenderState == nil {
		t.Fatalf("result = %#v, want applied render state", result)
	}
	state, ok := result.RenderState.State.(*filediff.State)
	if !ok || state.Change == nil || state.Change.Diff == nil {
		t.Fatalf("state = %#v, want applied change diff", result.RenderState.State)
	}
	if got, want := state.Change.Diff.LineCount(), 2_001; got != want {
		t.Fatalf("applied diff has %d lines, want %d", got, want)
	}
}

func TestWriteFileRendersMixedEndingsAndBareCRPreimages(t *testing.T) {
	for _, test := range []struct {
		name       string
		before     string
		after      string
		lineEnding string
		wantOld    string
	}{
		{name: "mixed endings", before: "one\ntwo\r\n", after: "after\n", lineEnding: "mixed", wantOld: "two"},
		{name: "bare CR", before: "before\r", after: "after\r", lineEnding: "mixed", wantOld: "before\r"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(test.before), 0o600); err != nil {
				t.Fatal(err)
			}
			tool, _ := newWriteTool(t, dir)
			result := invokeWrite(t, tool, map[string]any{"path": "f.txt", "content": test.after})
			if result.IsError || result.RenderState == nil {
				t.Fatalf("result = %#v, want applied render state", result)
			}
			state, ok := result.RenderState.State.(*filediff.State)
			if !ok || state.Phase != filediff.PhaseApplied || state.Change == nil || state.Change.State != filediff.ChangeApplied || state.Change.Diff == nil {
				t.Fatalf("state = %#v, want applied change", result.RenderState.State)
			}
			if state.Change.Diff.BeforeFormat.LineEnding != test.lineEnding {
				t.Fatalf("diff = %#v, want %q line ending", state.Change.Diff, test.lineEnding)
			}
			found := false
			for _, line := range state.Change.Diff.AllLines() {
				found = found || line.Kind == filediff.LineDelete && line.Content == test.wantOld
			}
			if !found {
				t.Fatalf("diff lines = %#v, want deleted %q", state.Change.Diff.AllLines(), test.wantOld)
			}
		})
	}
}
