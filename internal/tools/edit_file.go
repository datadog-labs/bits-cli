package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

const bom = "\ufeff"

type textReplacement struct {
	start   int
	length  int
	newText string
}

type fileEdit struct {
	OldText string  `json:"old_text"`
	NewText *string `json:"new_text"`
}

func newEditFileTool(r *os.Root, root string, locker *mutationLocker) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        toolEditFile,
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
		Approval: workspaceWriteApproval(root),
		Handler:  editFileHandler(r, locker),
	}
}

func editFileHandler(r *os.Root, locker *mutationLocker) agent.ToolHandler {
	return func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		var args struct {
			Path  string     `json:"path"`
			Edits []fileEdit `json:"edits"`
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
		if len(args.Edits) == 0 {
			return errorResult("edits must contain at least one replacement"), nil
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
		ending, ok := lineEndingStyle(body)
		if !ok {
			return errorResult("%s has inconsistent line endings; normalize them before editing", args.Path), nil
		}
		base := normalizeToLF(body)

		repls, errResult := matchEdits(base, args.Edits, filePath)
		if errResult != nil {
			return *errResult, nil
		}

		newBase := applyReplacements(base, repls)
		if newBase == base {
			return errorResult("no changes: the replacements produced identical content"), nil
		}

		final := restoreLineEndings(newBase, ending)
		if hasBOM {
			final = bom + final
		}
		if err := safeReplace(ctx, r, filePath, []byte(final)); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return agent.ToolResult{}, ctxErr
			}
			return errorResult("write failed: %s", err.Error()), nil
		}

		// The model sees only a concise summary; the full unified diff travels out
		// of the model's token band in Display (the tool response's display-only
		// content), so it is never capped for token budget.
		diff := strings.TrimSuffix(udiff.Unified("a/"+filePath, "b/"+filePath, base, newBase), "\n")
		noun := "replacement"
		if len(repls) != 1 {
			noun = "replacements"
		}
		return agent.ToolResult{
			Title:   filePath,
			Output:  fmt.Sprintf("Applied %d %s to %s", len(repls), noun, filePath),
			Display: "```diff\n" + diff + "\n```",
		}, nil
	}
}

// matchEdits resolves every edit against a single snapshot of base. It returns
// the replacements sorted by position, or an error ToolResult describing the
// first edit that is empty, missing, ambiguous, or overlaps another edit. No
// edit is applied unless all of them resolve to unique, disjoint regions.
func matchEdits(base string, edits []fileEdit, filePath string) ([]textReplacement, *agent.ToolResult) {
	repls := make([]textReplacement, 0, len(edits))
	for i, e := range edits {
		oldText := normalizeToLF(e.OldText)
		if oldText == "" {
			res := editError(i, len(edits), filePath, "old_text must not be empty")
			return nil, &res
		}
		if e.NewText == nil {
			res := editError(i, len(edits), filePath, "new_text is required")
			return nil, &res
		}
		count := countMatches(base, oldText)
		if count == 0 {
			res := editError(i, len(edits), filePath, "old_text was not found; it must match the file exactly, including whitespace and newlines")
			return nil, &res
		}
		if count > 1 {
			res := editError(i, len(edits), filePath, fmt.Sprintf("old_text matched %d regions; add surrounding context so it matches exactly one", count))
			return nil, &res
		}
		repls = append(repls, textReplacement{
			start:   strings.Index(base, oldText),
			length:  len(oldText),
			newText: normalizeToLF(*e.NewText),
		})
	}

	sort.Slice(repls, func(a, b int) bool { return repls[a].start < repls[b].start })
	for i := 1; i < len(repls); i++ {
		if repls[i-1].start+repls[i-1].length > repls[i].start {
			res := errorResult("edits in %s overlap; each edit must target a disjoint region of the original file", filePath)
			return nil, &res
		}
	}
	return repls, nil
}

// countMatches counts occurrences of sub in s including overlapping ones, so a
// self-overlapping old_text (e.g. "\n\n" in "\n\n\n") is correctly treated as
// ambiguous rather than unique. strings.Count only counts non-overlapping runs
// and would report such a match as occurring exactly once.
func countMatches(s, sub string) int {
	n := 0
	for i := 0; ; {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			return n
		}
		n++
		i += j + 1
	}
}

func editError(index, total int, filePath, msg string) agent.ToolResult {
	if total == 1 {
		return errorResult("%s in %s", msg, filePath)
	}
	return errorResult("edits[%d]: %s in %s", index, msg, filePath)
}

// applyReplacements rewrites base by copying the gaps between the (position
// sorted, non-overlapping) replacements and substituting each matched region.
func applyReplacements(base string, repls []textReplacement) string {
	var b strings.Builder
	prev := 0
	for _, rep := range repls {
		b.WriteString(base[prev:rep.start])
		b.WriteString(rep.newText)
		prev = rep.start + rep.length
	}
	b.WriteString(base[prev:])
	return b.String()
}

// lineEndingStyle reports the file's single line-ending convention: "\r\n" if
// every break is CRLF, "\n" otherwise. It returns ok=false when the endings are
// inconsistent (a mix of CRLF and bare LF, or any bare CR), rather than silently
// normalizing untouched lines to one convention on write.
func lineEndingStyle(s string) (string, bool) {
	crlf := strings.Count(s, "\r\n")
	bareLF := strings.Count(s, "\n") - crlf
	bareCR := strings.Count(s, "\r") - crlf
	if bareCR > 0 || (crlf > 0 && bareLF > 0) {
		return "", false
	}
	if crlf > 0 {
		return "\r\n", true
	}
	return "\n", true
}

func normalizeToLF(text string) string {
	if !strings.ContainsRune(text, '\r') {
		return text
	}
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

func restoreLineEndings(text, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}
