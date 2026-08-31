package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

const (
	maxListEntries = 2000
	maxListBytes   = 100 * 1024
)

func newListFilesTool(fsys fs.FS, root string) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        "list_files",
			Description: "List files and directories in the workspace. Directories end with /. Respects .gitignore. Returns up to 2000 entries.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":  map[string]any{"type": "string", "description": "Workspace-relative directory. Default: workspace root."},
					"depth": map[string]any{"type": "integer", "description": "Max traversal depth. Default: 1. Max: 10."},
				},
				"additionalProperties": false,
			},
		},
		Approval: approvalFor(root),
		Handler:  listFilesHandler(fsys),
	}
}

func listFilesHandler(fsys fs.FS) agent.ToolHandler {
	return func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		var args struct {
			Path  string `json:"path"`
			Depth *int   `json:"depth"`
		}
		if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
			return agent.ToolResult{IsError: true, Output: "invalid input: " + err.Error()}, nil
		}
		if strings.HasPrefix(args.Path, "/") {
			return agent.ToolResult{IsError: true, Output: "path must be workspace-relative, not absolute"}, nil
		}

		base := args.Path
		if base == "" {
			base = "."
		}

		maxDepth := 1
		if args.Depth != nil {
			maxDepth = *args.Depth
			if maxDepth < 1 {
				maxDepth = 1
			}
			if maxDepth > 10 {
				maxDepth = 10
			}
		}

		// Verify the target exists and is a directory.
		info, err := fs.Stat(fsys, base)
		if err != nil {
			return agent.ToolResult{IsError: true, Output: "cannot access path: " + err.Error()}, nil
		}
		if !info.IsDir() {
			return agent.ToolResult{IsError: true, Output: args.Path + " is not a directory"}, nil
		}

		ig, err := loadIgnorer(ctx, fsys, base, true)
		if err != nil {
			return agent.ToolResult{IsError: true, Output: err.Error()}, nil
		}
		if base != "." && ig.Ignore(base, true) {
			return agent.ToolResult{Title: base, Output: "(empty)"}, nil
		}

		var entries []string
		truncated := false
		byteCount := 0

		err = fs.WalkDir(fsys, base, func(p string, d fs.DirEntry, err error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err != nil {
				return nil // skip unreadable entries
			}
			if d.Name() == ".git" && d.IsDir() {
				return fs.SkipDir
			}
			if ig.Ignore(p, d.IsDir()) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() && p != base {
				if err := ig.Add(ctx, fsys, p); err != nil {
					return err
				}
			}
			if p == base {
				return nil // skip the root entry itself
			}

			depth := strings.Count(strings.TrimPrefix(p, base+"/"), "/") + 1
			if base == "." {
				depth = strings.Count(p, "/") + 1
			}
			if depth > maxDepth {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}

			rel := p
			if base != "." {
				rel = strings.TrimPrefix(p, base+"/")
			}
			entry := rel
			if d.IsDir() {
				entry += "/"
			}

			line := entry + "\n"
			if len(entries) >= maxListEntries || byteCount+len(line) > maxListBytes {
				truncated = true
				return fs.SkipAll
			}
			entries = append(entries, entry)
			byteCount += len(line)
			return nil
		})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return agent.ToolResult{}, ctxErr
			}
			return agent.ToolResult{IsError: true, Output: "walk error: " + err.Error()}, nil
		}

		if len(entries) == 0 {
			return agent.ToolResult{Title: base, Output: "(empty)"}, nil
		}

		output := strings.Join(entries, "\n")
		if truncated {
			output += fmt.Sprintf("\n\n[Truncated: showing %d of more entries.]", len(entries))
		}
		return agent.ToolResult{Title: base, Output: output}, nil
	}
}
