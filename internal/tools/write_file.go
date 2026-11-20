package tools

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/filediff"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

func newWriteFileTool(r *os.Root, root string, locker *mutationLocker) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        spec.WriteFile,
			Description: "Create a new file or fully overwrite an existing one in the workspace. Missing parent directories are created. An overwrite keeps the file's encoding (UTF-8 or UTF-16) and byte order mark. Use edit_file for targeted changes to an existing file.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "Workspace-relative file path."},
					"content": map[string]any{"type": "string", "description": "Full file contents to write."},
				},
				"required":             []string{"path", "content"},
				"additionalProperties": false,
			},
		},
		Approval:     workspaceWriteApproval(root),
		Handler:      writeFileHandler(r, root, locker),
		InputReducer: writeFileInputReducer(r, root),
	}
}

func writeFileHandler(r *os.Root, root string, locker *mutationLocker) agent.ToolHandler {
	return func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		var args spec.WriteFileInput
		if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
			return errorResult("invalid input: %s", err.Error()), nil
		}
		filePath, err := workspace.LocalPath(root, args.Path)
		if err != nil {
			return errorResult("%s", err.Error()), nil
		}
		if args.Content == nil {
			return errorResult("content is required"), nil
		}

		unlock, err := locker.lock(ctx)
		if err != nil {
			return agent.ToolResult{}, err
		}
		defer unlock()

		// Lstat, not Stat: refuse to overwrite through a symlink or clobber a
		// non-regular file. A missing target (err != nil) is the create path.
		operation := filediff.OpCreate
		if info, err := r.Lstat(filePath); err == nil {
			operation = filediff.OpOverwrite
			switch {
			case info.Mode()&fs.ModeSymlink != 0:
				return errorResult("%s is a symbolic link", filePath), nil
			case info.IsDir():
				return errorResult("%s is a directory", filePath), nil
			case !info.Mode().IsRegular():
				return errorResult("%s is not a regular file", filePath), nil
			}
		}
		before, enc := captureEditorSnapshot(ctx, r, filePath)

		// An overwrite keeps the file's encoding, which the content cannot
		// express.
		// NOTE: a file over the 10 MB preview limit is not read, so it is
		// rewritten as UTF-8; read_file cannot read it either.
		content := overwriteText(before.Raw, *args.Content)
		data, err := encodeText(content, enc)
		if err != nil {
			return errorResult("encode %s: %s", filePath, err.Error()), nil
		}
		if err := safeReplace(ctx, r, filePath, data); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return agent.ToolResult{}, ctxErr
			}
			return errorResult("write failed: %s", err.Error()), nil
		}

		result := agent.ToolResult{
			Title:  filePath,
			Output: pluralizeBytes(filePath, len(data)),
		}
		if before.State == filediff.SnapshotReady || before.State == filediff.SnapshotMissing {
			previous := before.Raw
			renderDiff, display := filediff.BuildWithDisplay("a/"+filePath, "b/"+filePath, previous, content)
			result.Display = display
			result.RenderState = agent.ReplaceRenderState(appliedEditorState(filePath, operation, renderDiff))
		} else {
			result.RenderState = agent.ReplaceRenderState(&filediff.State{Phase: filediff.PhaseApplied, Snapshot: before, Change: &filediff.Change{Path: filePath, Op: operation, State: filediff.ChangeUnavailable}, Reason: before.Reason})
		}
		return result, nil
	}
}

// overwriteText keeps the byte order mark of the file text it replaces:
// read_file does not show one, so the content need not repeat it.
func overwriteText(before, content string) string {
	if strings.HasPrefix(before, bom) && !strings.HasPrefix(content, bom) {
		return bom + content
	}
	return content
}

func pluralizeBytes(filePath string, n int) string {
	unit := "bytes"
	if n == 1 {
		unit = "byte"
	}
	return "Wrote " + strconv.Itoa(n) + " " + unit + " to " + filePath
}
