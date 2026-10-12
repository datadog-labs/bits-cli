package env

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/datadog-labs/bits-cli/internal/workspace"
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

// Snapshot loads ancestor instructions from the filesystem
// root to the active directory, including ancestors outside Git repositories.
func (p *Instructions) Snapshot(parent context.Context) string {
	active, err := filepath.Abs(p.workspacePath)
	if err != nil {
		return ""
	}
	repository, shadowed := instructionRepository(parent, active)
	var directories []string
	for dir := active; ; dir = filepath.Dir(dir) {
		directories = append(directories, dir)
		if filepath.Dir(dir) == dir {
			break
		}
	}
	slices.Reverse(directories)
	var snapshot strings.Builder
	remaining := instructionTotalLimit
	seen := make(map[string]bool)
	for _, dir := range directories {
		if parent.Err() != nil {
			break
		}
		boundary := dir
		if repository != "" && withinRoot(repository, dir) {
			boundary = repository
		}
		file := loadInstructionFile(dir, boundary, min(instructionFileLimit, remaining))
		if file == nil || seen[file.path] {
			continue
		}
		resolved, _ := filepath.EvalSymlinks(file.path)
		if shadowed != "" && resolved == shadowed {
			continue
		}
		seen[file.path] = true
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

// instructionRepository identifies the worktree confinement boundary and the
// main checkout file shadowed by a nested linked worktree's own selected file.
// Bare layouts, submodules, and sibling worktrees do not shadow ancestor files.
func instructionRepository(parent context.Context, active string) (string, string) {
	layout := workspace.InspectRepositoryLayout(parent, active)
	repository, common := layout.Root, layout.CommonDir
	if repository == "" || common == "" {
		return repository, ""
	}
	main := filepath.Dir(common)
	mainGit, err := filepath.EvalSymlinks(filepath.Join(main, ".git"))
	if err != nil || mainGit != common || main == repository || !withinRoot(main, repository) {
		return repository, ""
	}
	// A one-byte probe selects the same readable filename without retaining a
	// second copy of the worktree's instruction contents.
	selected := loadInstructionFile(repository, repository, 1)
	if selected == nil {
		return repository, ""
	}
	return repository, filepath.Join(main, filepath.Base(selected.path))
}

func loadInstructionFile(directory, boundary string, limit int) *instructionFile {
	resolvedBoundary, err := filepath.EvalSymlinks(boundary)
	if err != nil {
		return nil
	}
	root, err := os.OpenRoot(resolvedBoundary)
	if err != nil {
		return nil
	}
	defer func() { _ = root.Close() }()
	for _, filename := range instructionFilenames {
		source := filepath.Join(directory, filename)
		resolved, resolveErr := filepath.EvalSymlinks(source)
		if resolveErr != nil || !withinRoot(resolvedBoundary, resolved) {
			continue
		}
		relative, relErr := filepath.Rel(resolvedBoundary, resolved)
		if relErr != nil {
			continue
		}
		info, statErr := root.Stat(relative)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		content, readErr := readBoundedFile(root, relative, limit)
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
