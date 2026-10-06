package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
)

const (
	maxFileSize  = 10 * 1024 * 1024
	maxReadLines = 2000
	maxReadBytes = 100 * 1024
)

func newReadFileTool(fsys fs.FS, root string) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        spec.ReadFile,
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
		Approval: workspaceReadApproval(root),
	}
}

func readFileHandler(fsys fs.FS) agent.ToolHandler {
	return func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		var args spec.ReadFileInput
		if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
			return errorResult("%s", err.Error()), nil
		}

		if args.Path == "" {
			return errorResult("path must not be empty"), nil
		}
		if strings.HasPrefix(args.Path, "/") {
			return errorResult("path must be relative, not absolute"), nil
		}
		if args.Offset != nil && *args.Offset < 1 {
			return errorResult("offset must be positive"), nil
		}
		if args.Limit != nil && *args.Limit <= 0 {
			return errorResult("limit must be positive"), nil
		}

		filePath := path.Clean(args.Path)
		f, err := fsys.Open(filePath)
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
		if info.IsDir() {
			return errorResult("%s is a directory", filePath), nil
		}
		if !info.Mode().IsRegular() {
			return errorResult("%s is not a regular file", filePath), nil
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
			return errorResult("binary file, cannot read as text"), nil
		}

		content := strings.TrimSuffix(string(data), "\n")
		totalLines := strings.Count(content, "\n") + 1

		offset := 1
		if args.Offset != nil {
			offset = *args.Offset
		}
		if offset > totalLines {
			return errorResult("offset %d is beyond end of file (%d lines)", offset, totalLines), nil
		}

		return agent.ToolResult{Title: filePath, Output: readWindow(content, offset, args.Limit, totalLines)}, nil
	}
}

// readWindow returns the slice of content starting at the 1-based offset line,
// bounded by the optional limit and by the line and byte read caps, with a
// continuation hint appended when the window stops short of the end of file.
//
// offset is assumed valid (1 <= offset <= totalLines). The returned string is a
// sub-slice of content when the whole window fits, so the common untruncated
// read allocates nothing beyond the mandatory string(data). A single sized
// allocation happens only when a hint must be appended.
func readWindow(content string, offset int, limit *int, totalLines int) string {
	startIdx := offset - 1

	startOff := 0
	for range startIdx {
		startOff += strings.IndexByte(content[startOff:], '\n') + 1
	}

	avail := totalLines - startIdx
	maxLines := avail
	if limit != nil && *limit < maxLines {
		maxLines = *limit
	}

	byteCount := 0
	written := 0
	endOff := startOff
	pos := startOff
	capped := false
	for written < maxLines {
		lineEnd := len(content)
		cost := 0
		if rel := strings.IndexByte(content[pos:], '\n'); rel >= 0 {
			lineEnd = pos + rel
			cost = 1 // the trailing newline; the last line has none
		}
		cost += lineEnd - pos
		if written >= maxReadLines || byteCount+cost > maxReadBytes {
			capped = true
			break
		}
		byteCount += cost
		endOff = lineEnd
		written++
		pos = lineEnd + 1
	}

	// Only the byte cap can trip with written == 0, since maxReadLines > 0.
	if capped && written == 0 {
		msg := fmt.Sprintf("[Line %d exceeds the %d-byte read limit.]", offset, maxReadBytes)
		if offset < totalLines {
			msg += fmt.Sprintf(" Use offset=%d to continue.", offset+1)
		}
		return msg
	}

	window := content[startOff:endOff]
	if capped {
		return window + fmt.Sprintf("\n\n[Showing lines %d\u2013%d of %d. Use offset=%d to continue.]",
			offset, offset+written-1, totalLines, offset+written)
	}
	if written < avail {
		return window + fmt.Sprintf("\n\n[%d more lines. Use offset=%d to continue.]", avail-written, offset+written)
	}
	return window
}
