package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/filediff"
)

func TestParsePartialEditorInput(t *testing.T) {
	t.Run("write prefix and escapes", func(t *testing.T) {
		input := parseWriteFileInput(`{"path":"a.txt","content":"one\n\u03bb`)
		if input.Path == nil || *input.Path != "a.txt" || input.Content == nil || *input.Content != "one\n" {
			t.Fatalf("input = %#v", input)
		}
	})
	t.Run("closed content flushes its final unterminated line", func(t *testing.T) {
		input := parseWriteFileInput(`{"path":"a.txt","content":"last"`)
		if input.Content == nil || *input.Content != "last" {
			t.Fatalf("input = %#v", input)
		}
	})
	t.Run("closed edits only", func(t *testing.T) {
		input := parseEditFileInput(`{"path":"a.txt","edits":[{"old_text":"old","new_text":"new"},{"old_text":"unfinished"`)
		if input.Path == nil || len(input.Edits) != 1 || input.Edits[0].NewText == nil || *input.Edits[0].NewText != "new" || input.PendingEdit != nil {
			t.Fatalf("input = %#v", input)
		}
	})
	t.Run("streams a pending replacement after a complete target", func(t *testing.T) {
		input := parseEditFileInput(`{"path":"a.txt","edits":[{"old_text":"old","new_text":"new\npartial`)
		if len(input.Edits) != 0 || input.PendingEdit == nil || input.PendingEdit.OldText != "old" || input.PendingEdit.NewText != "new\n" || input.PendingEdit.NewTextComplete {
			t.Fatalf("input = %#v", input)
		}
	})
	t.Run("closed replacement flushes its final unterminated line", func(t *testing.T) {
		input := parseEditFileInput(`{"path":"a.txt","edits":[{"old_text":"old","new_text":"last"`)
		if input.PendingEdit == nil || input.PendingEdit.NewText != "last" || !input.PendingEdit.NewTextComplete {
			t.Fatalf("input = %#v", input)
		}
	})
	t.Run("retains completed edits before one pending replacement", func(t *testing.T) {
		input := parseEditFileInput(`{"path":"a.txt","edits":[{"old_text":"first","new_text":"FIRST"},{"old_text":"second","new_text":"SECOND\npartial`)
		if len(input.Edits) != 1 || input.PendingEdit == nil || input.PendingEdit.OldText != "second" || input.PendingEdit.NewText != "SECOND\n" {
			t.Fatalf("input = %#v", input)
		}
	})
	t.Run("partial duplicate target clears the pending replacement", func(t *testing.T) {
		input := parseEditFileInput(`{"path":"a.txt","edits":[{"old_text":"old","new_text":"new","old_text":"replacement`)
		if input.PendingEdit != nil {
			t.Fatalf("input = %#v, want duplicate target to invalidate preview", input)
		}
	})
	t.Run("duplicate replacement invalidates prior value", func(t *testing.T) {
		input := parseWriteFileInput(`{"path":"old.txt","path":"new`)
		if input.Path != nil {
			t.Fatalf("input = %#v", input)
		}
	})
	t.Run("null is not a string", func(t *testing.T) {
		input := parseWriteFileInput(`{"path":null,"content":null}`)
		if input.Path != nil || input.Content != nil {
			t.Fatalf("input = %#v", input)
		}
	})
	t.Run("escaped keys", func(t *testing.T) {
		input := parseWriteFileInput(`{"pa\u0074h":"a.txt","con\u0074ent":"ok`)
		if input.Path == nil || *input.Path != "a.txt" || input.Content == nil || *input.Content != "" {
			t.Fatalf("input = %#v", input)
		}
	})
}

func TestParsePartialEditorInputWritePrefixMonotonic(t *testing.T) {
	full := `{"path":"a.txt","content":"hello\\nworld"}`
	previous := ""
	for i := range len(full) + 1 {
		input := parseWriteFileInput(full[:i])
		if input.Content != nil && len(*input.Content) < len(previous) {
			t.Fatalf("prefix %d content regressed from %q", i, previous)
		}
		if input.Content != nil {
			previous = *input.Content
		}
	}
}

func FuzzParsePartialEditorInputNeverPanics(f *testing.F) {
	for _, seed := range []string{`{"path":"a.txt","content":"hello"}`, `{"path":"a.txt","edits":[{"old_text":"old","new_text":"new"}]}`, `{"path":"a\\u`} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, input string) {
		_ = parseWriteFileInput(input)
		_ = parseEditFileInput(input)
	})
}

func TestEditorInputReducerDoesNotRetainRawInput(t *testing.T) {
	dir := t.TempDir()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	state := writeFileInputReducer(r)(context.Background(), agent.ToolInputUpdate{
		RawPrefix: `{"path":"a.txt","content":"` + strings.Repeat("x", 50_000) + `"}`,
	}, nil).(*filediff.State)
	if state.Preview == nil || state.Preview.Diff == nil || len(state.Preview.Diff.AllLines()) > maxEditorDiffLines+1 {
		t.Fatalf("preview lines = %d, want bounded", len(state.Preview.Diff.AllLines()))
	}
}

func TestEditorInputReducerWriteKeepsPartialContentPending(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	reduce := writeFileInputReducer(r)

	state, ok := reduce(context.Background(), agent.ToolInputUpdate{
		Name: toolWriteFile, RawPrefix: `{"path":"note.txt","content":"received`,
	}, nil).(*filediff.State)
	if !ok || state.Snapshot == nil || state.Snapshot.Text != "before\n" {
		t.Fatalf("first reducer state = %#v, want captured text snapshot", state)
	}
	if state.Preview != nil {
		t.Fatalf("partial line state = %#v, want no visible preview before LF", state)
	}
	firstLine, ok := reduce(context.Background(), agent.ToolInputUpdate{
		Name: toolWriteFile, RawPrefix: `{"path":"note.txt","content":"received\npartial`,
	}, state).(*filediff.State)
	if !ok || firstLine.Preview == nil || firstLine.Preview.Diff == nil || !firstLine.Preview.Pending || len(firstLine.Preview.Diff.AllLines()) == 0 || firstLine.Preview.Diff.AllLines()[len(firstLine.Preview.Diff.AllLines())-1].Kind != filediff.LinePending {
		t.Fatalf("first complete line preview = %#v, want pending prefix", firstLine.Preview)
	}
	stable, ok := reduce(context.Background(), agent.ToolInputUpdate{
		Name: toolWriteFile, RawPrefix: `{"path":"note.txt","content":"received\npartial text`,
	}, firstLine).(*filediff.State)
	if !ok || stable != firstLine {
		t.Fatalf("partial line update = %#v, want unchanged preview state", stable)
	}

	// A stream retains its original display snapshot. This is intentional: the
	// handler later re-reads beneath the mutation lock and replaces this preview.
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	final, ok := reduce(context.Background(), agent.ToolInputUpdate{
		Name: toolWriteFile, HasFinalInput: true, FinalInput: `{"path":"note.txt","content":"after\n"}`,
	}, stable).(*filediff.State)
	if !ok || final.Snapshot == nil || final.Snapshot.Text != "before\n" {
		t.Fatalf("final reducer snapshot = %#v, want original snapshot", final)
	}
	if final.Phase != filediff.PhaseReady || final.Preview == nil || final.Preview.Pending {
		t.Fatalf("final reducer state = %#v, want ready non-pending diff", final)
	}
}

func TestWritePrefixDiffMatchesFinalCRLFLineContent(t *testing.T) {
	content := "one\r\ntwo\r\n"
	prefix := writePrefixDiff("note.txt", content, 0)
	final := filediff.Build("a/note.txt", "b/note.txt", "", content, 0)
	for _, diff := range []*filediff.Diff{&prefix, &final} {
		var additions []string
		for _, line := range diff.AllLines() {
			if line.Kind == filediff.LineAdd {
				additions = append(additions, line.Content)
			}
		}
		if strings.Join(additions, ",") != "one,two" {
			t.Fatalf("additions = %#v, want CRLF-free logical lines", additions)
		}
	}
}

func TestEditorInputReducerEditStreamsOnlyAfterCompleteOldText(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	reduce := editFileInputReducer(r)

	partialOld, ok := reduce(context.Background(), agent.ToolInputUpdate{
		Name: toolEditFile, RawPrefix: `{"path":"note.txt","edits":[{"old_text":"ol`,
	}, nil).(*filediff.State)
	if !ok || partialOld.Preview != nil {
		t.Fatalf("partial old_text state = %#v, want no speculative edit", partialOld)
	}

	partialNew, ok := reduce(context.Background(), agent.ToolInputUpdate{
		Name: toolEditFile, RawPrefix: `{"path":"note.txt","edits":[{"old_text":"old","new_text":"new\npartial`,
	}, partialOld).(*filediff.State)
	if !ok || partialNew.Phase != filediff.PhaseStreaming || partialNew.Preview == nil || !partialNew.Preview.Pending || partialNew.Preview.Diff == nil {
		t.Fatalf("partial new_text state = %#v, want pending speculative edit", partialNew)
	}
	var deletedOld, addedNew, pending bool
	for _, line := range partialNew.Preview.Diff.AllLines() {
		deletedOld = deletedOld || line.Kind == filediff.LineDelete && line.Content == "old"
		addedNew = addedNew || line.Kind == filediff.LineAdd && line.Content == "new"
		pending = pending || line.Kind == filediff.LinePending
	}
	if !deletedOld || !addedNew || !pending {
		t.Fatalf("partial new_text diff = %#v, want -old +new and pending tail", partialNew.Preview.Diff.AllLines())
	}
	stable, ok := reduce(context.Background(), agent.ToolInputUpdate{
		Name: toolEditFile, RawPrefix: `{"path":"note.txt","edits":[{"old_text":"old","new_text":"new\nsecond`,
	}, partialNew).(*filediff.State)
	if !ok || stable != partialNew {
		t.Fatalf("partial line update = %#v, want unchanged preview state", stable)
	}
	grown, ok := reduce(context.Background(), agent.ToolInputUpdate{
		Name: toolEditFile, RawPrefix: `{"path":"note.txt","edits":[{"old_text":"old","new_text":"new\nsecond\n`,
	}, stable).(*filediff.State)
	if !ok || grown.Preview == nil || grown.Preview.Diff == nil || !strings.Contains(strings.Join(diffContents(grown.Preview.Diff), "\n"), "second") {
		t.Fatalf("completed second line diff = %#v, want streamed second line", grown)
	}
	ready, ok := reduce(context.Background(), agent.ToolInputUpdate{
		Name: toolEditFile, HasFinalInput: true, FinalInput: `{"path":"note.txt","edits":[{"old_text":"old","new_text":"new\nsecond"}]}`,
	}, grown).(*filediff.State)
	if !ok || ready.Phase != filediff.PhaseReady || ready.Preview == nil || ready.Preview.Kind != filediff.PreviewEdit || ready.Preview.Pending {
		t.Fatalf("complete edit state = %#v, want ready edit preview", ready)
	}
}

func diffContents(diff *filediff.Diff) []string {
	lines := diff.AllLines()
	contents := make([]string, 0, len(lines))
	for _, line := range lines {
		contents = append(contents, line.Content)
	}
	return contents
}

func TestParsePartialEditorInputStopsAtCompleteInvalidEdit(t *testing.T) {
	input := parseEditFileInput(`{"path":"a.txt","edits":[{"old_text":"old"},{"old_text":"later","new_text":"replacement"}]}`)
	if len(input.Edits) != 0 {
		t.Fatalf("edits = %#v, want invalid set discarded", input.Edits)
	}
}
