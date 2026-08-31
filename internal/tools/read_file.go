package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

const maxFileSize = 10 * 1024 * 1024

type readFileArgs struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

func newReadFileTool(fsys fs.FS, root string) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        "read_file",
			Description: "Read the contents of a file in the workspace.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":   map[string]any{"type": "string"},
					"offset": map[string]any{"type": "integer", "description": "1-based start line. Default: 1."},
					"limit":  map[string]any{"type": "integer", "description": "Max lines to return."},
				},
				"required":             []string{"path"},
				"additionalProperties": false,
			},
		},
		Handler:  readFileHandler(fsys),
		Approval: approvalFor(root),
	}
}

func readFileHandler(fsys fs.FS) agent.ToolHandler {
	return func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		var args readFileArgs
		if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
			return agent.ToolResult{IsError: true, Output: err.Error()}, nil
		}

		if args.Path == "" {
			return agent.ToolResult{IsError: true, Output: "path must not be empty"}, nil
		}
		if strings.HasPrefix(args.Path, "/") {
			return agent.ToolResult{IsError: true, Output: "path must be relative, not absolute"}, nil
		}
		if args.Offset != nil && *args.Offset < 1 {
			return agent.ToolResult{IsError: true, Output: "offset must be positive"}, nil
		}
		if args.Limit != nil && *args.Limit <= 0 {
			return agent.ToolResult{IsError: true, Output: "limit must be positive"}, nil
		}

		f, err := fsys.Open(args.Path)
		if err != nil {
			return agent.ToolResult{IsError: true, Output: err.Error()}, nil
		}
		defer func() { _ = f.Close() }()
		stopClose := context.AfterFunc(ctx, func() { _ = f.Close() })
		defer stopClose()

		info, err := f.Stat()
		if err != nil {
			return agent.ToolResult{IsError: true, Output: err.Error()}, nil
		}
		if info.IsDir() {
			return agent.ToolResult{IsError: true, Output: args.Path + " is a directory"}, nil
		}
		if !info.Mode().IsRegular() {
			return agent.ToolResult{IsError: true, Output: args.Path + " is not a regular file"}, nil
		}
		if info.Size() > maxFileSize {
			return agent.ToolResult{IsError: true, Output: fmt.Sprintf("file exceeds 10 MB limit (%d bytes)", info.Size())}, nil
		}

		data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, r: f}, maxFileSize+1))
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return agent.ToolResult{}, ctxErr
			}
			return agent.ToolResult{IsError: true, Output: err.Error()}, nil
		}
		if len(data) > maxFileSize {
			return agent.ToolResult{IsError: true, Output: "file exceeds 10 MB limit"}, nil
		}

		if isBinary(data) {
			return agent.ToolResult{IsError: true, Output: "binary file, cannot read as text"}, nil
		}

		allLines := strings.Split(string(data), "\n")
		totalLines := len(allLines)

		offset := 1
		if args.Offset != nil {
			offset = *args.Offset
		}
		startIdx := offset - 1
		if startIdx < 0 || startIdx >= totalLines {
			return agent.ToolResult{
				IsError: true,
				Output:  fmt.Sprintf("offset %d is beyond end of file (%d lines)", offset, totalLines),
			}, nil
		}

		selected := allLines[startIdx:]
		limitApplied := false
		if args.Limit != nil {
			lim := *args.Limit
			if lim < len(selected) {
				selected = selected[:lim]
				limitApplied = true
			}
		}

		r := truncateLines(selected, offset, totalLines)

		if !r.truncated && limitApplied && startIdx+len(selected) < totalLines {
			remaining := totalLines - (startIdx + len(selected))
			nextOffset := startIdx + len(selected) + 1
			r.content += fmt.Sprintf("\n\n[%d more lines. Use offset=%d to continue.]", remaining, nextOffset)
		}

		return agent.ToolResult{Title: args.Path, Output: r.content}, nil
	}
}
