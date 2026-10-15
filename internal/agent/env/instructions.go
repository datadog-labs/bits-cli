package env

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

const (
	instructionsContextStart      = "<project_instructions>\n"
	instructionsContextEnd        = "</project_instructions>\n"
	instructionsInsertionNotice   = "Make sure to follow these project instructions."
	instructionFileLimit          = 32 * 1024
	instructionTotalLimit         = 128 * 1024
	instructionsReplacementNotice = instructionsContextStart + "This block replaces all earlier provided <project_instructions> blocks."
	instructionsRemovalNotice     = instructionsContextStart + "This block replaces any earlier <project_instructions> blocks.\nAny previously provided <project_instructions> no longer apply.\n" + instructionsContextEnd
)

// Instructions discovers local project instructions without retaining delivery state.
type Instructions struct{ workspacePath string }

// NewInstructions uses an explicit workspace without changing the process directory.
func NewInstructions(path string) *Instructions { return &Instructions{workspacePath: path} }

// ReplaceInstructions renders an update that supersedes unknown prior instructions.
func ReplaceInstructions(current string) string {
	if current == "" {
		return instructionsRemovalNotice
	}
	return instructionsReplacementNotice + "\n" + strings.TrimPrefix(current, instructionsContextStart)
}

// instructionFilenames lists supported filenames in precedence order. Only the first
// readable regular file in each directory participates, even when it is empty.
var instructionFilenames = [...]string{"AGENTS.override.md", "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"}

type instructionFile struct {
	path      string
	content   []byte
	truncated bool
}

// Snapshot loads instructions from the project root down to the active directory.
func (p *Instructions) Snapshot(parent context.Context) string {
	active, err := filepath.Abs(p.workspacePath)
	if err != nil {
		return ""
	}
	active, err = filepath.EvalSymlinks(active)
	if err != nil {
		return ""
	}
	directories := projectDirectories(active)
	var snapshot strings.Builder
	remaining := instructionTotalLimit
	for _, dir := range directories {
		if parent.Err() != nil {
			break
		}
		file := loadInstructionFile(dir, min(instructionFileLimit, remaining))
		if file == nil {
			continue
		}
		label := filepath.ToSlash(file.path)
		if remaining == 0 {
			fmt.Fprintf(&snapshot, "\n[Instructions from %q omitted: aggregate byte limit reached.]\n", label)
			break
		}
		if strings.TrimSpace(string(file.content)) == "" {
			continue
		}
		remaining -= len(file.content)
		if snapshot.Len() == 0 {
			snapshot.WriteString(instructionsContextStart + instructionsInsertionNotice + "\n\n")
		}
		fmt.Fprintf(&snapshot, "  <file path=\"%s\">\n%s", html.EscapeString(label), file.content)
		if file.truncated {
			fmt.Fprintf(&snapshot, "\n[Truncated: read %q for the remaining instructions.]", label)
		}
		snapshot.WriteString("\n  </file>\n")
	}
	if snapshot.Len() > 0 {
		snapshot.WriteString(instructionsContextEnd)
	}
	return snapshot.String()
}

func loadInstructionFile(directory string, limit int) *instructionFile {
	for _, filename := range instructionFilenames {
		source := filepath.Join(directory, filename)
		info, statErr := os.Stat(source)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		content, readErr := readBoundedFile(source, limit)
		if readErr != nil {
			continue
		}
		truncated := len(content) > limit
		if truncated {
			// Instructions retain a UTF-8-safe prefix; skills reject oversized files.
			for limit > 0 && !utf8.RuneStart(content[limit]) {
				limit--
			}
			content = content[:limit]
		}
		return &instructionFile{path: source, content: bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf}), truncated: truncated}
	}
	return nil
}
