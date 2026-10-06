package tools

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/filediff"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
)

func newWriteFileTool(r *os.Root, root string, locker *mutationLocker) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        spec.WriteFile,
			Description: "Create a new file or fully overwrite an existing one in the workspace. Missing parent directories are created. Use edit_file for targeted changes to an existing file.",
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
		Handler:      writeFileHandler(r, locker),
		InputReducer: writeFileInputReducer(r),
	}
}

func writeFileHandler(r *os.Root, locker *mutationLocker) agent.ToolHandler {
	return func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		var args spec.WriteFileInput
		if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
			return errorResult("invalid input: %s", err.Error()), nil
		}
		if args.Path == "" {
			return errorResult("path must not be empty"), nil
		}
		if strings.HasPrefix(args.Path, "/") {
			return errorResult("path must be workspace-relative, not absolute"), nil
		}
		if args.Content == nil {
			return errorResult("content is required"), nil
		}

		filePath := path.Clean(args.Path)

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
				return errorResult("%s is a symbolic link", args.Path), nil
			case info.IsDir():
				return errorResult("%s is a directory", args.Path), nil
			case !info.Mode().IsRegular():
				return errorResult("%s is not a regular file", args.Path), nil
			}
		}
		before := captureEditorSnapshot(ctx, r, filePath)

		data := []byte(*args.Content)
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
			renderDiff, display := filediff.BuildWithDisplay("a/"+filePath, "b/"+filePath, previous, *args.Content)
			result.Display = display
			result.RenderState = agent.ReplaceRenderState(appliedEditorState(filePath, operation, renderDiff))
		} else {
			result.RenderState = agent.ReplaceRenderState(&filediff.State{Phase: filediff.PhaseApplied, Snapshot: before, Change: &filediff.Change{Path: filePath, Op: operation, State: filediff.ChangeUnavailable}, Reason: before.Reason})
		}
		return result, nil
	}
}

func pluralizeBytes(filePath string, n int) string {
	unit := "bytes"
	if n == 1 {
		unit = "byte"
	}
	return "Wrote " + strconv.Itoa(n) + " " + unit + " to " + filePath
}
