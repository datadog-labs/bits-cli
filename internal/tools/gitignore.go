package tools

import (
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
type Ignorer struct {
	matcher *gitignore.Matcher
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
	ig.matcher.AddPatterns(data, scope)
	return nil
}

// Ignore reports whether filePath (workspace-relative) should be excluded.
func (ig *Ignorer) Ignore(filePath string, isDir bool) bool {
	return ig.matcher != nil && ig.matcher.MatchPath(filePath, isDir)
}

// loadIgnorer loads every .gitignore from the workspace root through base.
// For a file base, only its containing directories are loaded.
func loadIgnorer(ctx context.Context, fsys fs.FS, base string, baseIsDir bool) (Ignorer, error) {
	var ig Ignorer
	if err := ig.Add(ctx, fsys, "."); err != nil {
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
		if err := ig.Add(ctx, fsys, current); err != nil {
			return Ignorer{}, err
		}
	}
	return ig, nil
}
