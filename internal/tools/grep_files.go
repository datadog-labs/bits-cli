package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strings"
	"unicode/utf8"

	gitignore "github.com/git-pkgs/gitignore"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

const (
	maxGrepMatches    = 200
	maxGrepBytes      = 100 * 1024
	maxGrepLineBytes  = 10 * 1024 * 1024
	maxGrepMatchBytes = 8 * 1024
)

var errGrepMatchLimit = errors.New("grep match limit reached")

func newGrepFilesTool(fsys fs.FS, root string) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        toolGrepFiles,
			Description: "Search workspace files using a Go regular expression. Returns file:line: text matches. Skips binary files. Respects .gitignore. Returns up to 200 matches.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern":        map[string]any{"type": "string", "description": "Go regular expression."},
					"path":           map[string]any{"type": "string", "description": "Workspace-relative directory or file. Default: workspace root."},
					"include":        map[string]any{"type": "string", "description": "Glob pattern to filter files, e.g. '**/*.go'."},
					"case_sensitive": map[string]any{"type": "boolean", "description": "Default: false."},
					"offset":         map[string]any{"type": "integer", "description": "0-based match index to start from. Default: 0."},
				},
				"required":             []string{"pattern"},
				"additionalProperties": false,
			},
		},
		Approval: workspaceReadApproval(root),
		// TODO: add optional ripgrep acceleration once profiling identifies a bottleneck.
		Handler: grepFilesHandler(fsys),
	}
}

func grepFilesHandler(fsys fs.FS) agent.ToolHandler {
	return func(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		var args struct {
			Pattern       string `json:"pattern"`
			Path          string `json:"path"`
			Include       string `json:"include"`
			CaseSensitive bool   `json:"case_sensitive"`
			Offset        int    `json:"offset"`
		}
		if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
			return agent.ToolResult{IsError: true, Output: "invalid input: " + err.Error()}, nil
		}
		if args.Pattern == "" {
			return agent.ToolResult{IsError: true, Output: "pattern is required"}, nil
		}
		if args.Offset < 0 {
			return agent.ToolResult{IsError: true, Output: "offset must not be negative"}, nil
		}
		if strings.HasPrefix(args.Path, "/") {
			return agent.ToolResult{IsError: true, Output: "path must be workspace-relative, not absolute"}, nil
		}

		reStr := args.Pattern
		if !args.CaseSensitive {
			reStr = "(?i)" + reStr
		}
		re, err := regexp.Compile(reStr)
		if err != nil {
			return agent.ToolResult{IsError: true, Output: "invalid pattern: " + err.Error()}, nil
		}

		var includeMatcher *gitignore.Matcher
		if args.Include != "" {
			includeMatcher = gitignore.New("")
			includeMatcher.AddPatterns([]byte(args.Include), "")
			if len(includeMatcher.Errors()) > 0 {
				return agent.ToolResult{IsError: true, Output: "invalid include pattern: " + args.Include}, nil
			}
		}

		base := args.Path
		if base == "" {
			base = "."
		}

		info, err := fs.Stat(fsys, base)
		if err != nil {
			return agent.ToolResult{IsError: true, Output: "cannot access path: " + err.Error()}, nil
		}

		ig, err := loadIgnorer(ctx, fsys, base, info.IsDir())
		if err != nil {
			return agent.ToolResult{IsError: true, Output: err.Error()}, nil
		}
		if base != "." && ig.Ignore(base, info.IsDir()) {
			return agent.ToolResult{Output: "No matches found."}, nil
		}

		type match struct {
			file string
			line int
			text string
		}
		var allMatches []match
		start := args.Offset
		seen := 0

		searchFile := func(filePath string, skipOpenErrors bool) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			f, err := fsys.Open(filePath)
			if err != nil {
				if skipOpenErrors {
					return nil
				}
				return fmt.Errorf("open %s: %w", filePath, err)
			}
			defer func() { _ = f.Close() }()
			stopClose := context.AfterFunc(ctx, func() { _ = f.Close() })
			defer stopClose()
			info, err := f.Stat()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}

			// Binary probe.
			probe := make([]byte, binaryProbeSize)
			n, readErr := io.ReadFull(contextReader{ctx: ctx, r: f}, probe)
			if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				return readErr
			}
			if isBinary(probe[:n]) {
				return nil
			}

			// Replay the bytes used for probing, then continue with the original file.
			return scanGrepLines(ctx, io.MultiReader(bytes.NewReader(probe[:n]), f), func(lineNum int, line []byte) error {
				if re.Match(line) {
					seen++
					if seen <= start {
						return nil
					}
					rel := filePath
					if base != "." && strings.HasPrefix(filePath, base+"/") {
						rel = strings.TrimPrefix(filePath, base+"/")
					}
					allMatches = append(allMatches, match{file: rel, line: lineNum, text: truncateGrepMatch(line)})
					if len(allMatches) > maxGrepMatches {
						return errGrepMatchLimit
					}
				}
				return nil
			})
		}

		cappedByMatchLimit := false
		if !info.IsDir() {
			if includeMatcher != nil && !includeMatcher.MatchPath(base, false) {
				return agent.ToolResult{Output: "No matches found."}, nil
			}
			if err := searchFile(base, false); err != nil {
				if !errors.Is(err, errGrepMatchLimit) {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return agent.ToolResult{}, ctxErr
					}
					return agent.ToolResult{IsError: true, Output: err.Error()}, nil
				}
				cappedByMatchLimit = true
			}
		} else {
			walkErr := fs.WalkDir(fsys, base, func(p string, d fs.DirEntry, err error) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err != nil {
					return nil
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
				if d.IsDir() {
					return nil
				}
				if includeMatcher != nil && !includeMatcher.MatchPath(p, false) {
					return nil
				}
				return searchFile(p, true)
			})
			if errors.Is(walkErr, errGrepMatchLimit) {
				cappedByMatchLimit = true
			} else if walkErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return agent.ToolResult{}, ctxErr
				}
				return agent.ToolResult{IsError: true, Output: "walk error: " + walkErr.Error()}, nil
			}
		}

		if seen == 0 {
			return agent.ToolResult{Output: "No matches found."}, nil
		}

		if len(allMatches) == 0 {
			return agent.ToolResult{
				Output: fmt.Sprintf("offset %d is beyond the last match (%d total).", start, seen),
			}, nil
		}

		visible := allMatches
		capped := cappedByMatchLimit
		if len(visible) > maxGrepMatches {
			visible = visible[:maxGrepMatches]
			capped = true
		}

		var sb strings.Builder
		byteCount := 0
		shown := 0
		for _, m := range visible {
			line := fmt.Sprintf("%s:%d: %s\n", m.file, m.line, m.text)
			if byteCount+len(line) > maxGrepBytes {
				capped = true
				if shown == 0 {
					sb.WriteString(truncateOutputLine(line, maxGrepBytes))
					shown++
				}
				break
			}
			sb.WriteString(line)
			byteCount += len(line)
			shown++
		}

		output := strings.TrimRight(sb.String(), "\n")
		if capped {
			next := start + shown
			if cappedByMatchLimit {
				output += fmt.Sprintf("\n\n[more matches may be available. Use offset=%d to continue.]", next)
			} else {
				remaining := len(allMatches) - shown
				output += fmt.Sprintf("\n\n[%d more matches. Use offset=%d to continue.]", remaining, next)
			}
		}
		return agent.ToolResult{Output: output}, nil
	}
}

func scanGrepLines(ctx context.Context, r io.Reader, visit func(int, []byte) error) error {
	br := bufio.NewReaderSize(contextReader{ctx: ctx, r: r}, binaryProbeSize)
	line := make([]byte, 0, binaryProbeSize)
	lineNum := 0
	tooLong := false

	for {
		fragment, isPrefix, err := br.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(line) > 0 && !tooLong {
					lineNum++
					if err := visit(lineNum, line); err != nil {
						return err
					}
				}
				return nil
			}
			return err
		}
		if !tooLong {
			if len(line)+len(fragment) > maxGrepLineBytes {
				line = line[:0]
				tooLong = true
			} else {
				line = append(line, fragment...)
			}
		}
		if isPrefix {
			continue
		}

		lineNum++
		if !tooLong {
			if err := visit(lineNum, line); err != nil {
				return err
			}
		}
		line = line[:0]
		tooLong = false
	}
}

func truncateGrepMatch(line []byte) string {
	if len(line) <= maxGrepMatchBytes {
		return string(line)
	}
	cut := maxGrepMatchBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return string(line[:cut]) + "… [match truncated]"
}

func truncateOutputLine(line string, limit int) string {
	if len(line) <= limit {
		return line
	}
	const suffix = "… [match truncated]\n"
	if limit <= len(suffix) {
		return truncateUTF8(suffix, limit)
	}
	return truncateUTF8(line, limit-len(suffix)) + suffix
}

func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
