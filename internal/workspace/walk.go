package workspace

import (
	"context"
	"io/fs"
	"path"
)

// WalkFunc is called for each non-ignored entry beneath a workspace-relative
// base. It may return the same control errors accepted by fs.WalkDirFunc.
type WalkFunc func(filePath string, entry fs.DirEntry) error

// Walk traverses base through the workspace's confined filesystem.
func (w *Workspace) Walk(ctx context.Context, base string, visit WalkFunc) error {
	return WalkFS(ctx, w.FS(), base, visit)
}

// WalkFS traverses base without following directory symlinks. It skips .git,
// unreadable entries, and paths excluded by the applicable nested .gitignore
// files. Paths passed to visit are relative to the filesystem root.
//
// WalkFS is exposed so callers can exercise the same traversal against an
// in-memory fs.FS in tests. Production callers should normally use
// Workspace.Walk.
func WalkFS(ctx context.Context, fsys fs.FS, base string, visit WalkFunc) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	base = path.Clean(base)
	info, err := fs.Stat(fsys, base)
	if err != nil {
		return err
	}
	ignorer, err := loadIgnorer(ctx, fsys, base, info.IsDir())
	if err != nil {
		return err
	}
	if base != "." && ignorer.Ignore(base, info.IsDir()) {
		return nil
	}

	return fs.WalkDir(fsys, base, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return nil
		}
		if entry.Name() == ".git" && entry.IsDir() {
			return fs.SkipDir
		}
		if ignorer.Ignore(filePath, entry.IsDir()) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() && filePath != base {
			if err := ignorer.Add(ctx, fsys, filePath); err != nil {
				return err
			}
		}
		return visit(filePath, entry)
	})
}
