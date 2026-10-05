package agent

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// invocationContext rereads the registered file through its discovery boundary.
// Bodies are loaded only for explicit user turns, never for menu discovery.
func (s LocalSkill) invocationContext(ctx context.Context, arguments string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.boundary == "" || !withinInstructionsRoot(s.boundary, s.Path) {
		return "", fmt.Errorf("local skill %q has no registered read boundary", s.Name)
	}
	root, err := os.OpenRoot(s.boundary)
	if err != nil {
		return "", fmt.Errorf("open local skill %q root: %w", s.Name, err)
	}
	defer func() { _ = root.Close() }()
	boundaryInfo, err := root.Stat(".")
	if err != nil {
		return "", fmt.Errorf("stat local skill %q root: %w", s.Name, err)
	}
	if s.boundaryInfo == nil || !os.SameFile(s.boundaryInfo, boundaryInfo) {
		return "", fmt.Errorf("local skill %q discovery root changed; start a new conversation to rescan", s.Name)
	}
	relative, err := filepath.Rel(s.boundary, s.Path)
	if err != nil {
		return "", fmt.Errorf("resolve local skill %q: %w", s.Name, err)
	}
	info, err := root.Stat(relative)
	if err != nil {
		return "", fmt.Errorf("stat local skill %q: %w", s.Name, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("local skill %q is not a regular file", s.Name)
	}
	content, truncated, err := readInstructions(root, relative, skillFileLimit)
	if err != nil {
		return "", fmt.Errorf("read local skill %q: %w", s.Name, err)
	}
	if truncated || !utf8.Valid(content) {
		return "", fmt.Errorf("local skill %q is oversized or is not valid UTF-8", s.Name)
	}
	metadata, valid := parseClientSkill(content, filepath.Base(filepath.Dir(s.Path)))
	if !valid || metadata.Name != s.Name {
		return "", fmt.Errorf("local skill %q metadata changed or is invalid; start a new conversation to rescan", s.Name)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Validation above guarantees a frontmatter delimiter. Preserve the body
	// exactly, including line endings and trailing whitespace.
	text := string(bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf}))
	offset := 0
	for i, line := range strings.SplitAfter(text, "\n") {
		offset += len(line)
		if i > 0 && strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r") == "---" {
			break
		}
	}
	return fmt.Sprintf("<invoked-local-client-skill name=\"%s\" path=\"%s\" base-directory=\"%s\">\nThe user explicitly invoked this local skill. Follow these instructions, resolving relative references from its base directory.\n<instructions>%s</instructions>\n<arguments>%s</arguments>\n</invoked-local-client-skill>", html.EscapeString(s.Name), html.EscapeString(filepath.ToSlash(s.Path)), html.EscapeString(filepath.ToSlash(filepath.Dir(s.Path))), html.EscapeString(text[offset:]), html.EscapeString(arguments)), nil
}
