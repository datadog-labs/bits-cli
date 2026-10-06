package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/filediff"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
)

type editSpec struct {
	Old string `json:"old_text"`
	New string `json:"new_text"`
}

func newEditTool(t *testing.T, dir string) agent.Tool {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return newEditFileTool(r, dir, newMutationLocker())
}

func invokeEdit(t *testing.T, tool agent.Tool, path string, edits []editSpec) agent.ToolResult {
	t.Helper()
	data, err := json.Marshal(map[string]any{"path": path, "edits": edits})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Handler(context.Background(), agent.ToolCall{ID: "e", Name: spec.EditFile, Input: string(data)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// editInDir seeds path with content, runs edits, and returns the result plus
// the resulting raw file bytes.
func editInDir(t *testing.T, path, content string, edits []editSpec) (agent.ToolResult, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newEditTool(t, dir)
	result := invokeEdit(t, tool, path, edits)
	got, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	return result, string(got)
}

func TestEditFileReplacements(t *testing.T) {
	t.Run("single exact replacement", func(t *testing.T) {
		r, got := editInDir(t, "f.go", "package main\n\nfunc main() {}\n", []editSpec{{Old: "func main() {}", New: "func main() { println(1) }"}})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		if got != "package main\n\nfunc main() { println(1) }\n" {
			t.Errorf("content = %q", got)
		}
	})

	t.Run("returns handler-authoritative rendered state", func(t *testing.T) {
		r, _ := editInDir(t, "f.txt", "before\n", []editSpec{{Old: "before", New: "after"}})
		if r.RenderState == nil {
			t.Fatal("RenderState = nil, want applied state")
		}
		state, ok := r.RenderState.State.(*filediff.State)
		if !ok || state.Phase != filediff.PhaseApplied || state.Change == nil || state.Change.Op != filediff.OpEdit || state.Change.Diff == nil || state.Change.Diff.Additions != 1 || state.Change.Diff.Deletions != 1 {
			t.Fatalf("RenderState = %#v, want applied edit", r.RenderState.State)
		}
	})

	t.Run("multiple disjoint replacements in one call", func(t *testing.T) {
		r, got := editInDir(t, "f.txt", "alpha\nbeta\ngamma\n", []editSpec{
			{Old: "alpha", New: "ALPHA"},
			{Old: "gamma", New: "GAMMA"},
		})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		if got != "ALPHA\nbeta\nGAMMA\n" {
			t.Errorf("content = %q", got)
		}
	})

	t.Run("missing old_text fails without modifying the file", func(t *testing.T) {
		r, got := editInDir(t, "f.txt", "hello world\n", []editSpec{{Old: "absent", New: "x"}})
		assertError(t, r, "not found")
		if got != "hello world\n" {
			t.Errorf("file was modified: %q", got)
		}
	})

	t.Run("ambiguous old_text fails without modifying the file", func(t *testing.T) {
		r, got := editInDir(t, "f.txt", "x = 1\nx = 1\n", []editSpec{{Old: "x = 1", New: "x = 2"}})
		assertError(t, r, "matched 2 regions")
		if got != "x = 1\nx = 1\n" {
			t.Errorf("file was modified: %q", got)
		}
	})

	t.Run("self-overlapping old_text is ambiguous", func(t *testing.T) {
		// "\n\n" matches at offsets 0 and 1 of three consecutive newlines;
		// strings.Count would report 1, so this guards the overlap-aware count.
		r, got := editInDir(t, "f.txt", "a\n\n\nb\n", []editSpec{{Old: "\n\n", New: "\n"}})
		assertError(t, r, "matched 2 regions")
		if got != "a\n\n\nb\n" {
			t.Errorf("file was modified: %q", got)
		}
	})

	t.Run("absent new_text is rejected without modifying the file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		tool := newEditTool(t, dir)
		// Raw input: old_text present, new_text absent. Must not be treated as a
		// deletion of the matched text.
		result, err := tool.Handler(context.Background(), agent.ToolCall{Name: spec.EditFile, Input: `{"path":"f.txt","edits":[{"old_text":"alpha"}]}`})
		if err != nil {
			t.Fatal(err)
		}
		assertError(t, result, "new_text is required")
		if got, _ := os.ReadFile(filepath.Join(dir, "f.txt")); string(got) != "alpha\n" {
			t.Errorf("file was modified: %q", got)
		}
	})

	t.Run("explicit empty new_text deletes the matched text", func(t *testing.T) {
		r, got := editInDir(t, "f.txt", "keepDROPMEkeep\n", []editSpec{{Old: "DROPME", New: ""}})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		if got != "keepkeep\n" {
			t.Errorf("content = %q, want the match deleted", got)
		}
	})

	t.Run("empty old_text fails", func(t *testing.T) {
		r, got := editInDir(t, "f.txt", "content\n", []editSpec{{Old: "", New: "x"}})
		assertError(t, r, "must not be empty")
		if got != "content\n" {
			t.Errorf("file was modified: %q", got)
		}
	})

	t.Run("overlapping replacements fail without modifying the file", func(t *testing.T) {
		r, got := editInDir(t, "f.txt", "abcdef\n", []editSpec{
			{Old: "abcd", New: "X"},
			{Old: "cdef", New: "Y"},
		})
		assertError(t, r, "overlap")
		if got != "abcdef\n" {
			t.Errorf("file was modified: %q", got)
		}
	})

	t.Run("nested replacements fail without modifying the file", func(t *testing.T) {
		r, got := editInDir(t, "f.txt", "outer inner text\n", []editSpec{
			{Old: "outer inner text", New: "A"},
			{Old: "inner", New: "B"},
		})
		assertError(t, r, "overlap")
		if got != "outer inner text\n" {
			t.Errorf("file was modified: %q", got)
		}
	})

	t.Run("edits match the original snapshot, not sequential output", func(t *testing.T) {
		// The first edit rewrites "one" -> "two". If edits were applied
		// sequentially, the second edit's "two" would then match twice and the
		// call would be ambiguous. Matching the single original snapshot keeps
		// each edit's target unique.
		r, got := editInDir(t, "f.txt", "one\ntwo\n", []editSpec{
			{Old: "one", New: "two"},
			{Old: "two", New: "three"},
		})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		if got != "two\nthree\n" {
			t.Errorf("content = %q, want %q", got, "two\nthree\n")
		}
	})

	t.Run("identical replacement is rejected as a no-op", func(t *testing.T) {
		r, got := editInDir(t, "f.txt", "same\n", []editSpec{{Old: "same", New: "same"}})
		assertError(t, r, "no changes")
		if got != "same\n" {
			t.Errorf("file was modified: %q", got)
		}
	})

	t.Run("missing file fails", func(t *testing.T) {
		tool := newEditTool(t, t.TempDir())
		r := invokeEdit(t, tool, "absent.txt", []editSpec{{Old: "a", New: "b"}})
		assertError(t, r, "cannot access path")
	})

	t.Run("empty edits list fails", func(t *testing.T) {
		r, _ := editInDir(t, "f.txt", "x\n", nil)
		assertError(t, r, "at least one replacement")
	})

	t.Run("binary file rejected", func(t *testing.T) {
		r, _ := editInDir(t, "data.bin", "a\x00b", []editSpec{{Old: "a", New: "c"}})
		assertError(t, r, "binary")
	})

	t.Run("file exceeding the size limit is rejected", func(t *testing.T) {
		// The size cap is enforced on the opened file and the read is bounded, so
		// an over-limit file is refused rather than read unbounded.
		big := strings.Repeat("a", maxFileSize+1)
		r, got := editInDir(t, "big.txt", big, []editSpec{{Old: "needle", New: "x"}})
		assertError(t, r, "exceeds 10 MB")
		if got != big {
			t.Error("oversized file was modified")
		}
	})

	t.Run("cancelled context edits nothing", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		tool := newEditTool(t, dir)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := tool.Handler(ctx, agent.ToolCall{Name: spec.EditFile, Input: `{"path":"f.txt","edits":[{"old_text":"alpha","new_text":"beta"}]}`})
		if err != context.Canceled {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "f.txt")); string(got) != "alpha\n" {
			t.Errorf("file changed after cancellation: %q", got)
		}
	})

	t.Run("output is a concise summary; full diff is out of band", func(t *testing.T) {
		r, _ := editInDir(t, "f.txt", "one\ntwo\nthree\n", []editSpec{{Old: "two", New: "TWO"}})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		// The model-visible Output is a small summary with no diff.
		if r.Output != "Applied 1 replacement to f.txt" {
			t.Errorf("Output = %q, want a concise summary", r.Output)
		}
		if strings.Contains(r.Output, "@@") || strings.Contains(r.Output, "---") {
			t.Errorf("Output must not carry the diff: %q", r.Output)
		}
		if !strings.HasPrefix(r.Display, "--- a/f.txt\n+++ b/f.txt\n") {
			t.Fatalf("Display = %q, want a raw unified diff", r.Display)
		}
		if !strings.Contains(r.Display, "\n-two") || !strings.Contains(r.Display, "\n+TWO") {
			t.Errorf("Display diff = %q, want -two and +TWO", r.Display)
		}
	})

	t.Run("full diff is returned out of band without a cap", func(t *testing.T) {
		var sb strings.Builder
		for i := range 20000 {
			fmt.Fprintf(&sb, "original line %d\n", i)
		}
		original := sb.String()
		// One unique edit that rewrites the whole (well over 100 KB) file.
		r, _ := editInDir(t, "big.txt", original, []editSpec{
			{Old: original, New: strings.ReplaceAll(original, "original", "updated")},
		})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		// The model Output stays tiny regardless of file size.
		if r.Output != "Applied 1 replacement to big.txt" {
			t.Errorf("Output = %q, want a concise summary", r.Output)
		}
		// The Display diff is uncapped and carries the whole change.
		if len(r.Display) < 200*1024 {
			t.Errorf("Display diff = %d bytes, want the full (uncapped) diff", len(r.Display))
		}
		if strings.Contains(r.Display, "diff truncated") {
			t.Errorf("Display diff must not be truncated")
		}
	})

	t.Run("distant edits render as separate diff hunks", func(t *testing.T) {
		var sb strings.Builder
		for i := 1; i <= 14; i++ {
			fmt.Fprintf(&sb, "line%02d\n", i)
		}
		r, _ := editInDir(t, "f.txt", sb.String(), []editSpec{
			{Old: "line02", New: "LINE02"},
			{Old: "line13", New: "LINE13"},
		})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		if hunks := strings.Count(r.Display, "@@ -"); hunks != 2 {
			t.Errorf("hunk count = %d, want 2", hunks)
		}
		for _, want := range []string{"-line02", "+LINE02", "-line13", "+LINE13"} {
			if !strings.Contains(r.Display, "\n"+want) {
				t.Errorf("diff missing %q", want)
			}
		}
	})
}

func TestEditFilePreservation(t *testing.T) {
	t.Run("preserves a UTF-8 BOM", func(t *testing.T) {
		r, got := editInDir(t, "f.txt", bom+"alpha\nbeta\n", []editSpec{{Old: "alpha", New: "ALPHA"}})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		if !strings.HasPrefix(got, bom) {
			t.Errorf("BOM was dropped: %q", got)
		}
		if got != bom+"ALPHA\nbeta\n" {
			t.Errorf("content = %q", got)
		}
	})

	t.Run("preserves CRLF line endings", func(t *testing.T) {
		r, got := editInDir(t, "f.txt", "alpha\r\nbeta\r\ngamma\r\n", []editSpec{{Old: "beta", New: "BETA"}})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		if got != "alpha\r\nBETA\r\ngamma\r\n" {
			t.Errorf("content = %q, want CRLF preserved", got)
		}
	})

	t.Run("mixed CRLF and LF line endings are rejected", func(t *testing.T) {
		r, got := editInDir(t, "f.txt", "a\nb\r\nc\n", []editSpec{{Old: "b", New: "B"}})
		assertError(t, r, "inconsistent line endings")
		if got != "a\nb\r\nc\n" {
			t.Errorf("file was modified: %q", got)
		}
	})

	t.Run("CR-only line endings are rejected", func(t *testing.T) {
		r, got := editInDir(t, "f.txt", "a\rb\rc", []editSpec{{Old: "a", New: "A"}})
		assertError(t, r, "inconsistent line endings")
		if got != "a\rb\rc" {
			t.Errorf("file was modified: %q", got)
		}
	})

	t.Run("file without a trailing newline stays without one", func(t *testing.T) {
		r, got := editInDir(t, "f.txt", "a\nb\nc", []editSpec{{Old: "b", New: "B"}})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		if got != "a\nB\nc" {
			t.Errorf("content = %q, want no trailing newline added", got)
		}
	})

	t.Run("matches old_text supplied with LF against a CRLF file", func(t *testing.T) {
		// The model supplies LF; the file uses CRLF. Normalizing both to LF for
		// matching lets the edit land, and CRLF is restored on write.
		r, got := editInDir(t, "f.txt", "a\r\nb\r\nc\r\n", []editSpec{{Old: "a\nb", New: "a\nB"}})
		if r.IsError {
			t.Fatalf("unexpected error: %s", r.Output)
		}
		if got != "a\r\nB\r\nc\r\n" {
			t.Errorf("content = %q", got)
		}
	})
}
