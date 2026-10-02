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

type instructionsPhase uint8

const (
	instructionsUnprepared instructionsPhase = iota
	instructionsPending
	instructionsDelivered
)

// ProjectInstructions owns project instruction discovery, formatting, and delivery
// for one conversation at a time. Its engine serializes access.
type ProjectInstructions struct {
	workspacePath  string
	phase          instructionsPhase
	pendingContext string
}

// WithProjectInstructions attaches instructions owned by this engine.
// Instructions must not be shared between engines.
func WithProjectInstructions(instructions *ProjectInstructions) Option {
	return func(engine *Engine) { engine.projectInstructions = instructions }
}

// NewProjectInstructions uses the explicit workspace directory without changing
// the process working directory.
func NewProjectInstructions(workspacePath string) *ProjectInstructions {
	return &ProjectInstructions{workspacePath: workspacePath}
}

// apply prepares an update once per conversation. The exact update stays pending
// until delivery, even if a failed request assigns a conversation ID.
func (p *ProjectInstructions) apply(ctx context.Context, opts assistant.SendOptions) assistant.SendOptions {
	if p == nil || p.phase == instructionsDelivered {
		return opts
	}
	if p.phase == instructionsUnprepared {
		current := p.snapshot(ctx)
		if ctx.Err() != nil {
			return opts
		}
		update := current
		if opts.ConversationID != "" {
			// Resuming starts with unknown prior instructions.
			if current == "" {
				update = instructionsRemovalNotice
			} else {
				update = instructionsReplacementNotice + "\n\n" + current
			}
		}
		p.pendingContext = update
		p.phase = instructionsPending
	}
	if p.pendingContext != "" {
		if opts.CustomUserContext != "" {
			opts.CustomUserContext += "\n\n"
		}
		opts.CustomUserContext += p.pendingContext
	}
	return opts
}

// acknowledge records delivery after a response starts streaming or Send succeeds.
// Unstreamed failures retain the exact prepared update for the next request.
func (p *ProjectInstructions) acknowledge() {
	if p != nil && p.phase == instructionsPending {
		p.phase = instructionsDelivered
		p.pendingContext = ""
	}
}

// reset discards the conversation snapshot so the next request reloads the files.
func (p *ProjectInstructions) reset() {
	if p != nil {
		p.phase = instructionsUnprepared
		p.pendingContext = ""
	}
}

// instructionFilenames lists supported filenames in precedence order. Only the first
// readable regular file in each directory participates, even when it is empty.
var instructionFilenames = [...]string{"AGENTS.override.md", "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"}

type instructionFile struct {
	path      string
	content   []byte
	truncated bool
}

// snapshot loads ancestor instructions from the filesystem
// root to the active directory, including ancestors outside Git repositories.
func (p *ProjectInstructions) snapshot(parent context.Context) string {
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
