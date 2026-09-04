package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/filediff"
)

func TestRenderEditorToolUsesLocalStructuredState(t *testing.T) {
	diff := filediff.Diff{
		From: "a/f.go", To: "b/f.go",
		Hunks: []filediff.Hunk{{
			FromLine: 1, ToLine: 1, OldCount: 1, NewCount: 1,
			Lines: []filediff.DiffLine{
				{Kind: filediff.LineDelete, OldNumber: 1, Content: "func old() {}"},
				{Kind: filediff.LineAdd, NewNumber: 1, Content: "func new() {}"},
			},
		}},
		Additions: 1, Deletions: 1,
	}
	block := editorToolBlock("edit_file", "ignored", &filediff.State{
		Phase:  filediff.PhaseApplied,
		Change: &filediff.Change{Path: "f.go", Op: filediff.OpEdit, State: filediff.ChangeApplied, Diff: &diff},
	})

	got := ansi.Strip(RenderBlock(block, 80, DefaultStyles(true), 0))
	for _, want := range []string{"edit_file", "f.go", "func old() {}", "func new() {}"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "@@") {
		t.Errorf("render should not emit hunk headers:\n%s", got)
	}
	if strings.Contains(got, "ignored") {
		t.Errorf("local structured state did not take precedence:\n%s", got)
	}
}

func TestRenderEditorToolRestoresRawDisplay(t *testing.T) {
	_, display := filediff.BuildWithDisplay("a/f.txt", "b/f.txt", "before\n", "after\n")
	block := editorToolBlock("edit_file", display, nil)

	got := ansi.Strip(RenderBlock(block, 80, DefaultStyles(false), 0))
	for _, want := range []string{"edit_file", "f.txt", "before", "after"} {
		if !strings.Contains(got, want) {
			t.Errorf("restored render missing %q:\n%s", want, got)
		}
	}
}

func TestRenderEditorToolWithoutDiffFallsBackToGenericTool(t *testing.T) {
	for _, state := range []any{nil, (*filediff.State)(nil)} {
		block := editorToolBlock("write_file", "not a unified diff", state)
		block.Tool.Input = `{"path":"f.txt"}`
		block.Tool.Output = "the tool returned an error"

		got := ansi.Strip(RenderBlock(block, 80, DefaultStyles(false), 0))
		for _, want := range []string{"write_file", `{"path":"f.txt"}`, "the tool returned an error"} {
			if !strings.Contains(got, want) {
				t.Errorf("generic fallback missing %q:\n%s", want, got)
			}
		}
	}
}

func TestRenderEditorToolAppliedChangeUsesActualTail(t *testing.T) {
	var before, after strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&before, "line-%d\n", i)
		fmt.Fprintf(&after, "LINE-%d\n", i)
	}
	diff, display := filediff.BuildWithDisplay("a/f.txt", "b/f.txt", before.String(), after.String())
	diff.BeforeFormat = filediff.TextFormat{BOM: true, LineEnding: "crlf"}
	diff.AfterFormat = filediff.TextFormat{LineEnding: "lf"}
	block := editorToolBlock("edit_file", display, &filediff.State{
		Phase:  filediff.PhaseApplied,
		Change: &filediff.Change{Path: "f.txt", Op: filediff.OpEdit, State: filediff.ChangeApplied, Diff: &diff},
	})

	got := ansi.Strip(RenderBlock(block, 80, DefaultStyles(true), collapsedEditorDiffLines))
	for _, want := range []string{"LINE-30", "format: CRLF + BOM → LF"} {
		if !strings.Contains(got, want) {
			t.Fatalf("render missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "LINE-3\n") {
		t.Fatalf("render did not use the actual diff tail:\n%s", got)
	}
}

func TestRenderEditorToolEscapesSourceControlsAndExplainsFormatChange(t *testing.T) {
	diff := filediff.Diff{
		BeforeFormat: filediff.TextFormat{BOM: true, LineEnding: "crlf"},
		AfterFormat:  filediff.TextFormat{LineEnding: "lf"},
		Hunks: []filediff.Hunk{{
			FromLine: 1, ToLine: 1, OldCount: 0, NewCount: 1,
			Lines: []filediff.DiffLine{{Kind: filediff.LineAdd, NewNumber: 1, Content: "ok\x1b[31m\twide界"}},
		}},
	}
	block := editorToolBlock("write_file", "", &filediff.State{
		Phase:  filediff.PhaseApplied,
		Change: &filediff.Change{Path: "f.txt", Op: filediff.OpOverwrite, State: filediff.ChangeApplied, Diff: &diff},
	})

	rendered := RenderBlock(block, 80, DefaultStyles(true), 0)
	if strings.Contains(rendered, "\x1b[31m") {
		t.Fatalf("source control sequence survived rendering: %q", rendered)
	}
	got := ansi.Strip(rendered)
	if !strings.Contains(got, "format: CRLF + BOM → LF") || !strings.Contains(got, "ok\u241b[31m    wide界") {
		t.Fatalf("render = %q", got)
	}

	unavailable := editorToolBlock("write_file", "", &filediff.State{
		Phase:    filediff.PhaseUnavailable,
		Snapshot: &filediff.Snapshot{Path: "f\x1b[31m.txt"},
		Reason:   "unable to preview \x1b]0;unsafe\x07",
	})
	rendered = RenderBlock(unavailable, 80, DefaultStyles(true), 0)
	if strings.Contains(rendered, "\x1b[31m") || strings.Contains(rendered, "\x1b]0;unsafe\x07") {
		t.Fatalf("path or reason control sequence survived rendering: %q", rendered)
	}
	got = ansi.Strip(rendered)
	for _, want := range []string{"f\u241b[31m.txt", "unable to preview \u241b]0;unsafe\u2407"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q: %q", want, got)
		}
	}
}

func TestRenderStreamingEditorPreviewShowsTailForWritesAndEdits(t *testing.T) {
	for _, test := range []struct {
		name string
		tool string
		kind filediff.PreviewKind
	}{
		{name: "write", tool: "write_file", kind: filediff.PreviewWritePrefix},
		{name: "edit", tool: "edit_file", kind: filediff.PreviewEdit},
	} {
		t.Run(test.name, func(t *testing.T) {
			lines := make([]filediff.DiffLine, 0, collapsedEditorDiffLines+5)
			for number := 1; number <= cap(lines); number++ {
				lines = append(lines, filediff.DiffLine{Kind: filediff.LineAdd, NewNumber: number, Content: fmt.Sprintf("line%02d", number)})
			}
			if test.kind == filediff.PreviewWritePrefix {
				lines = append(lines, filediff.DiffLine{Kind: filediff.LinePending, Content: "awaiting more input"})
			}
			diff := filediff.Diff{Hunks: []filediff.Hunk{{FromLine: 1, ToLine: 1, NewCount: len(lines), Lines: lines}}}
			block := editorToolBlock(test.tool, "", &filediff.State{
				Phase:    filediff.PhaseStreaming,
				Snapshot: &filediff.Snapshot{Path: "f.txt"},
				Preview:  &filediff.Preview{Kind: test.kind, Diff: &diff, Pending: test.kind == filediff.PreviewWritePrefix},
			})
			var renderer blockRenderer
			got := ansi.Strip(renderer.RenderBlock(block, 80, DefaultStyles(true), 0))
			if strings.Contains(got, "line01") || !strings.Contains(got, "line25") || !strings.Contains(got, "hidden") {
				t.Fatalf("streaming tail render = %q", got)
			}
			if test.kind == filediff.PreviewWritePrefix && !strings.Contains(got, "awaiting more input") {
				t.Fatalf("write pending marker missing: %q", got)
			}
		})
	}
}

func editorToolBlock(name, detail string, renderState any) agent.Block {
	return agent.Block{
		Kind: assistant.KindToolResult,
		Tool: &agent.ToolBlock{
			Name:        name,
			Detail:      detail,
			RenderState: renderState,
			Status:      agent.ToolSuccess,
		},
	}
}
