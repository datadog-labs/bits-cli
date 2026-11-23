package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	gitignore "github.com/git-pkgs/gitignore"
)

const maxGitignoreSize = 1024 * 1024

// Ignorer accumulates .gitignore rules encountered during a workspace walk.
// Patterns are matched using Git-compatible wildmatch semantics.
// NOTE: matching is case-sensitive, as in ripgrep, even where git sets
// core.ignorecase: a dist/ rule shows Dist/ on Windows rather than risk
// hiding a distinct directory.
type Ignorer struct {
	matcher *gitignore.Matcher
	// unread holds the errors of the files add could not read.
	unread []error
}

// Add loads .gitignore rules from dir (workspace-relative; "." for the root).
// A missing file is silently skipped; other I/O errors are reported.
func (ig *Ignorer) Add(ctx context.Context, fsys fs.FS, dir string) error {
	name := ".gitignore"
	scope := ""
	if dir != "." && dir != "" {
		name = path.Join(dir, ".gitignore")
		scope = dir
	}
	f, err := fsys.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	stopClose := context.AfterFunc(ctx, func() { _ = f.Close() })
	defer stopClose()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("read %s: not a regular file", name)
	}
	if info.Size() > maxGitignoreSize {
		return fmt.Errorf("read %s: file exceeds 1 MB limit", name)
	}

	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, r: f}, maxGitignoreSize+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("read %s: %w", name, err)
	}
	if len(data) > maxGitignoreSize {
		return fmt.Errorf("read %s: file exceeds 1 MB limit", name)
	}
	if ig.matcher == nil {
		ig.matcher = gitignore.New("")
	}
	// Like git, drop the BOM Windows editors write, but read no other encoding.
	ig.matcher.AddPatterns(bytes.TrimPrefix(data, []byte("\ufeff")), scope)
	return nil
}

// Ignore reports whether filePath (workspace-relative) should be excluded.
func (ig *Ignorer) Ignore(filePath string, isDir bool) bool {
	return ig.matcher != nil && ig.matcher.MatchPath(filePath, isDir)
}

// IgnoreFileError reports the .gitignore files a walk could not read, such as
// one another process holds open on Windows. As git does, the walk still
// visits every entry, only without those files' rules.
type IgnoreFileError struct{ Errs []error }

// maxNamedIgnoreFiles bounds the message, which tools append after their
// output budget: a tree of unreadable files must not flood the model.
const maxNamedIgnoreFiles = 3

func (e *IgnoreFileError) Error() string {
	named := e.Errs[:min(len(e.Errs), maxNamedIgnoreFiles)]
	messages := make([]string, len(named))
	for i, err := range named {
		messages[i] = err.Error()
	}
	message := "ignore rules not applied: " + strings.Join(messages, "; ")
	if more := len(e.Errs) - len(named); more > 0 {
		message += fmt.Sprintf("; and %d more", more)
	}
	return message
}

func (e *IgnoreFileError) Unwrap() []error { return e.Errs }

// add loads dir's .gitignore like Add, but records a read failure rather than
// returning it. Only cancellation stops the caller.
func (ig *Ignorer) add(ctx context.Context, fsys fs.FS, dir string) error {
	err := ig.Add(ctx, fsys, dir)
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	ig.unread = append(ig.unread, err)
	return nil
}

// loadIgnorer loads every .gitignore from the workspace root through base.
// For a file base, only its containing directories are loaded.
func loadIgnorer(ctx context.Context, fsys fs.FS, base string, baseIsDir bool) (Ignorer, error) {
	var ig Ignorer
	if err := ig.add(ctx, fsys, "."); err != nil {
		return Ignorer{}, err
	}

	dir := path.Clean(base)
	if !baseIsDir {
		dir = path.Dir(dir)
	}
	if dir == "." || dir == "" {
		return ig, nil
	}

	current := ""
	for _, part := range strings.Split(dir, "/") {
		current = path.Join(current, part)
		// Git never reads ignore files below an already ignored directory.
		if ig.Ignore(current, true) {
			break
		}
		if err := ig.add(ctx, fsys, current); err != nil {
			return Ignorer{}, err
		}
	}
	return ig, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
