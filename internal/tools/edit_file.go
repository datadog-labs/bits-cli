package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/filediff"
	"github.com/DataDog/bits-cli/internal/tools/spec"
)

const bom = "\ufeff"

func newEditFileTool(r *os.Root, root string, locker *mutationLocker) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        spec.EditFile,
			Description: "Apply one or more exact text replacements to an existing workspace file. Each edit's old_text must match exactly one region of the current file. Edits are matched against the original file, not against each other; overlapping or ambiguous edits fail without modifying the file.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Workspace-relative file path."},
					"edits": map[string]any{
						"type":        "array",
						"description": "Replacements applied to the original file. Keep each old_text minimal but unique; do not overlap or nest edits.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"old_text": map[string]any{"type": "string", "description": "Exact text to replace. Must occur exactly once."},
								"new_text": map[string]any{"type": "string", "description": "Replacement text."},
							},
							"required":             []string{"old_text", "new_text"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"path", "edits"},
				"additionalProperties": false,
			},
		},
		Approval:     workspaceWriteApproval(root),
		Handler:      editFileHandler(r, locker),
		InputReducer: editFileInputReducer(r),
	}
}

func editFileHandler(r *os.Root, locker *mutationLocker) agent.ToolHandler {
	return func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		var args spec.EditFileInput
		if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
			return errorResult("invalid input: %s", err.Error()), nil
		}
		if args.Path == "" {
			return errorResult("path must not be empty"), nil
		}
		if strings.HasPrefix(args.Path, "/") {
			return errorResult("path must be workspace-relative, not absolute"), nil
		}
		if len(args.Edits) == 0 {
			return errorResult("edits must contain at least one replacement"), nil
		}

		edits := make([]filediff.Edit, len(args.Edits))
		for i, edit := range args.Edits {
			edits[i] = filediff.Edit{OldText: edit.OldText, NewText: edit.NewText}
		}
		filePath := path.Clean(args.Path)

		unlock, err := locker.lock(ctx)
		if err != nil {
			return agent.ToolResult{}, err
		}
		defer unlock()

		// Lstat, not Stat: an edit must never follow a symlink and silently
		// detach it (a rename over the link would replace the link with a new
		// regular file, leaving the real target untouched).
		if info, err := r.Lstat(filePath); err != nil {
			return errorResult("cannot access path: %s", err.Error()), nil
		} else if info.Mode()&fs.ModeSymlink != 0 {
			return errorResult("%s is a symbolic link", args.Path), nil
		} else if info.IsDir() {
			return errorResult("%s is a directory", args.Path), nil
		}

		// Open once, then validate and read the opened file through a bounded,
		// context-aware reader. Checking the size on an Lstat snapshot and then
		// doing a separate unbounded read would let an external writer grow or
		// swap the file between the two calls, bypassing the cap.
		f, err := r.Open(filePath)
		if err != nil {
			return errorResult("%s", err.Error()), nil
		}
		defer func() { _ = f.Close() }()
		stopClose := context.AfterFunc(ctx, func() { _ = f.Close() })
		defer stopClose()

		info, err := f.Stat()
		if err != nil {
			return errorResult("%s", err.Error()), nil
		}
		if !info.Mode().IsRegular() {
			return errorResult("%s is not a regular file", args.Path), nil
		}
		if info.Size() > maxFileSize {
			return errorResult("file exceeds 10 MB limit (%d bytes)", info.Size()), nil
		}

		data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, r: f}, maxFileSize+1))
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return agent.ToolResult{}, ctxErr
			}
			return errorResult("%s", err.Error()), nil
		}
		if len(data) > maxFileSize {
			return errorResult("file exceeds 10 MB limit"), nil
		}
		if isBinary(data) {
			return errorResult("binary file, cannot edit as text"), nil
		}

		raw := string(data)
		hasBOM := strings.HasPrefix(raw, bom)
		body := strings.TrimPrefix(raw, bom)
		ending, ok := filediff.LineEndingStyle(body)
		if !ok {
			return errorResult("%s has inconsistent line endings; normalize them before editing", args.Path), nil
		}
		base := filediff.NormalizeToLF(body)

		repls, err := filediff.MatchEdits(base, edits)
		if err != nil {
			return editMatchError(err, filePath), nil
		}

		newBase := filediff.ApplyReplacements(base, repls)
		if newBase == base {
			return errorResult("no changes: the replacements produced identical content"), nil
		}

		final := filediff.RestoreLineEndings(newBase, ending)
		if hasBOM {
			final = bom + final
		}
		if err := safeReplace(ctx, r, filePath, []byte(final)); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return agent.ToolResult{}, ctxErr
			}
			return errorResult("write failed: %s", err.Error()), nil
		}

		// The model sees only a concise summary; the complete raw diff travels out
		// of its token band in Display for durable history.
		renderDiff, display := filediff.BuildWithDisplay("a/"+filePath, "b/"+filePath, raw, final)
		noun := "replacement"
		if len(repls) != 1 {
			noun = "replacements"
		}
		return agent.ToolResult{
			Title:       filePath,
			Output:      fmt.Sprintf("Applied %d %s to %s", len(repls), noun, filePath),
			Display:     display,
			RenderState: agent.ReplaceRenderState(appliedEditorState(filePath, filediff.OpEdit, renderDiff)),
		}, nil
	}
}

// editMatchError restores the tool's stable, path-aware diagnostics from the
// pure matching error. The filediff package deliberately has no tool result or
// workspace-path dependency.
func editMatchError(err error, filePath string) agent.ToolResult {
	var matchErr *filediff.MatchError
	if !errors.As(err, &matchErr) {
		return errorResult("%s", err)
	}
	if matchErr.Kind == filediff.MatchOverlap {
		return errorResult("edits in %s overlap; each edit must target a disjoint region of the original file", filePath)
	}
	message := matchErr.Error()
	if matchErr.Kind == filediff.MatchAmbiguous {
		message = fmt.Sprintf("old_text matched %d regions; add surrounding context so it matches exactly one", matchErr.Count)
	}
	if matchErr.Total == 1 {
		return errorResult("%s in %s", message, filePath)
	}
	return errorResult("edits[%d]: %s in %s", matchErr.Index, message, filePath)
}
