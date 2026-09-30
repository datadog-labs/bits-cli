package agent

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DataDog/bits-cli/internal/assistant"
)

const (
	instructionsInsertionNotice   = "Make sure to follow the instructions in the context below"
	instructionFileLimit          = 32 * 1024
	instructionTotalLimit         = 128 * 1024
	instructionsReplacementNotice = "These project instructions replace all previously provided project instructions."
	instructionsRemovalNotice     = "The previously provided project instructions no longer apply."
)

// ProjectInstructionsManager owns project instruction discovery, formatting, and context updates
// for one conversation at a time. Its engine serializes access.
type ProjectInstructionsManager struct {
	workspacePath   string
	globalDirectory string
	prepared        bool
	previous        string
	known           bool
}

// WithProjectInstructionsManager attaches the manager owned by this engine. A manager
// must not be shared between engines.
func WithProjectInstructionsManager(manager *ProjectInstructionsManager) Option {
	return func(engine *Engine) { engine.projectInstructionsManager = manager }
}

// NewProjectInstructionsManager uses the explicit workspace directory without changing
// the process working directory.
func NewProjectInstructionsManager(workspacePath string) *ProjectInstructionsManager {
	manager := &ProjectInstructionsManager{workspacePath: workspacePath}
	if home, err := os.UserHomeDir(); err == nil {
		manager.globalDirectory = filepath.Join(home, ".bits-cli")
	}
	return manager
}

// apply sends an instruction update once per refresh. The backend preserves
// prior instructions in history, so a changed snapshot must explicitly replace
// or withdraw them. A resumed conversation starts with unknown prior context.
func (m *ProjectInstructionsManager) apply(ctx context.Context, opts assistant.SendOptions) assistant.SendOptions {
	if m == nil || m.prepared {
		return opts
	}
	current := m.snapshot(ctx)
	if ctx.Err() != nil {
		return opts
	}
	unknown := !m.known && opts.ConversationID != ""
	update := current
	switch {
	case m.known && current == m.previous:
		update = ""
	case current != "" && (unknown || m.previous != ""):
		update = instructionsReplacementNotice + "\n\n" + current
	case current == "" && (unknown || m.previous != ""):
		update = instructionsRemovalNotice
	}
	m.previous, m.known, m.prepared = current, true, true
	if update != "" {
		if opts.CustomUserContext != "" {
			opts.CustomUserContext += "\n\n"
		}
		opts.CustomUserContext += update
	}
	return opts
}

// refresh checks the files again on the next request, retaining the previous
// snapshot so unchanged instructions are not resent.
func (m *ProjectInstructionsManager) refresh() {
	if m != nil {
		m.prepared = false
	}
}

func (m *ProjectInstructionsManager) reset() {
	if m != nil {
		m.prepared = false
		m.previous = ""
		m.known = false
	}
}

// instructionFilenames follows Pi's context-file precedence. Only the first
// readable regular file in each directory participates, even when it is empty.
var instructionFilenames = [...]string{"AGENTS.override.md", "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"}

type instructionFile struct {
	path      string
	content   []byte
	truncated bool
}

// snapshot loads global instructions first, then ancestors from the filesystem
// root to the active directory, including ancestors outside Git repositories.
func (m *ProjectInstructionsManager) snapshot(parent context.Context) string {
	active, err := filepath.Abs(m.workspacePath)
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
	if m.globalDirectory != "" {
		if global, absErr := filepath.Abs(m.globalDirectory); absErr == nil {
			directories = append([]string{global}, directories...)
		}
	}
	var snapshot strings.Builder
	remaining := instructionTotalLimit
	seen := make(map[string]bool)
	for _, dir := range directories {
		if parent.Err() != nil {
			break
		}
		boundary := dir
		if repository != "" && withinInstructionsRoot(repository, dir) {
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
			snapshot.WriteString("# Project-Specific Context\n" + instructionsInsertionNotice + "\n\n<project_context>\n")
		}
		fmt.Fprintf(&snapshot, "  <file path=\"%s\">\n%s", html.EscapeString(label), file.content)
		if file.truncated {
			fmt.Fprintf(&snapshot, "\n[Truncated: read %q for the remaining instructions.]", label)
		}
		snapshot.WriteString("\n  </file>\n")
	}
	if snapshot.Len() > 0 {
		snapshot.WriteString("</project_context>\n")
	}
	return snapshot.String()
}

// instructionRepository identifies the worktree confinement boundary and the
// main checkout file shadowed by a nested linked worktree's own selected file.
// Bare layouts, submodules, and sibling worktrees do not shadow ancestor files.
func instructionRepository(parent context.Context, active string) (string, string) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	command.Dir = active
	output, err := command.Output()
	if err != nil {
		return "", ""
	}
	paths := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(paths) != 2 {
		return "", ""
	}
	repository, err := filepath.EvalSymlinks(paths[0])
	if err != nil {
		return "", ""
	}
	common, err := filepath.EvalSymlinks(paths[1])
	if err != nil {
		return repository, ""
	}
	main := filepath.Dir(common)
	mainGit, err := filepath.EvalSymlinks(filepath.Join(main, ".git"))
	if err != nil || mainGit != common || main == repository || !withinInstructionsRoot(main, repository) {
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
		if resolveErr != nil || !withinInstructionsRoot(resolvedBoundary, resolved) {
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
		content, truncated, readErr := readInstructions(root, relative, limit)
		if readErr != nil {
			continue
		}
		return &instructionFile{path: source, content: bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf}), truncated: truncated}
	}
	return nil
}

func withinInstructionsRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func readInstructions(root *os.Root, path string, limit int) ([]byte, bool, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("instruction file is not regular")
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	truncated := len(content) > limit
	if truncated {
		// Avoid splitting a UTF-8 character at the byte limit.
		for limit > 0 && !utf8.RuneStart(content[limit]) {
			limit--
		}
		content = content[:limit]
	}
	return content, truncated, nil
}
