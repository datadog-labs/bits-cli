package tools

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

func newWriteFileTool(r *os.Root, root string, locker *mutationLocker) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        toolWriteFile,
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
		Approval: workspaceWriteApproval(root),
		Handler:  writeFileHandler(r, locker),
	}
}

func writeFileHandler(r *os.Root, locker *mutationLocker) agent.ToolHandler {
	return func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		var args struct {
			Path    string  `json:"path"`
			Content *string `json:"content"`
		}
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
		if info, err := r.Lstat(filePath); err == nil {
			switch {
			case info.Mode()&fs.ModeSymlink != 0:
				return errorResult("%s is a symbolic link", args.Path), nil
			case info.IsDir():
				return errorResult("%s is a directory", args.Path), nil
			case !info.Mode().IsRegular():
				return errorResult("%s is not a regular file", args.Path), nil
			}
		}

		data := []byte(*args.Content)
		if err := safeReplace(ctx, r, filePath, data); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return agent.ToolResult{}, ctxErr
			}
			return errorResult("write failed: %s", err.Error()), nil
		}

		return agent.ToolResult{
			Title:  filePath,
			Output: pluralizeBytes(filePath, len(data)),
		}, nil
	}
}

func pluralizeBytes(filePath string, n int) string {
	unit := "bytes"
	if n == 1 {
		unit = "byte"
	}
	return "Wrote " + strconv.Itoa(n) + " " + unit + " to " + filePath
}
