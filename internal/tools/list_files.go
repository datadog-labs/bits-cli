package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools/spec"
)

const (
	maxListEntries = 2000
	maxListBytes   = 100 * 1024

	listFilesDefaultDepth = 1
	listFilesMinDepth     = 1
	listFilesMaxDepth     = 10
)

func newListFilesTool(fsys fs.FS, root string) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        spec.ListFiles,
			Description: "List files and directories in the workspace. Directories end with /. Respects .gitignore. Returns up to 2000 entries.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":  map[string]any{"type": "string", "description": "Workspace-relative directory. Default: workspace root."},
					"depth": map[string]any{"type": "integer", "description": fmt.Sprintf("Max traversal depth. Default: %d. Range: %d-%d.", listFilesDefaultDepth, listFilesMinDepth, listFilesMaxDepth)},
				},
				"additionalProperties": false,
			},
		},
		Approval: workspaceReadApproval(root),
		Handler:  listFilesHandler(fsys),
	}
}

func listFilesHandler(fsys fs.FS) agent.ToolHandler {
	return func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		var args spec.ListFilesInput
		if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
			return errorResult("invalid input: %s", err.Error()), nil
		}
		if strings.HasPrefix(args.Path, "/") {
			return errorResult("path must be workspace-relative, not absolute"), nil
		}

		base := path.Clean(args.Path)

		maxDepth := listFilesDefaultDepth
		if args.Depth != nil {
			maxDepth = *args.Depth
			if maxDepth < listFilesMinDepth || maxDepth > listFilesMaxDepth {
				return errorResult("depth must be between %d and %d", listFilesMinDepth, listFilesMaxDepth), nil
			}
		}

		// Verify the target exists and is a directory.
		info, err := fs.Stat(fsys, base)
		if err != nil {
			return errorResult("cannot access path: %s", err.Error()), nil
		}
		if !info.IsDir() {
			return errorResult("%s is not a directory", args.Path), nil
		}

		ig, err := loadIgnorer(ctx, fsys, base, true)
		if err != nil {
			return errorResult("%s", err.Error()), nil
		}
		if base != "." && ig.Ignore(base, true) {
			return agent.ToolResult{Title: base, Output: "(empty)"}, nil
		}

		var entries []string
		hitEntryLimit := false
		hitByteLimit := false
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
			if len(entries) >= maxListEntries {
				hitEntryLimit = true
				return fs.SkipAll
			}
			if byteCount+len(line) > maxListBytes {
				hitByteLimit = true
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
			return errorResult("walk error: %s", err.Error()), nil
		}

		if len(entries) == 0 {
			return agent.ToolResult{Title: base, Output: "(empty)"}, nil
		}

		output := strings.Join(entries, "\n")
		if hitEntryLimit || hitByteLimit {
			var warnings []string
			if hitEntryLimit {
				warnings = append(warnings, fmt.Sprintf("%d entries limit", maxListEntries))
			}
			if hitByteLimit {
				warnings = append(warnings, fmt.Sprintf("%dKB limit", maxListBytes/1024))
			}
			output += "\n\n[Truncated: " + strings.Join(warnings, ", ") + "]"
		}
		return agent.ToolResult{Title: base, Output: output}, nil
	}
}
