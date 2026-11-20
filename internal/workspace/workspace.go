// Package workspace provides confined access to the directory Bits is operating on.
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Workspace owns a confined filesystem root, its display path, and the shell
// selected when the workspace was opened.
//
// A Workspace is safe for concurrent use. Call Close when the process no
// longer needs it; values returned by OSRoot are borrowed and must not be
// closed independently.
type Workspace struct {
	path             string
	displayPath      string
	defaultShellPath string
	root             *os.Root
}

// Open resolves path, opens it as a confined workspace, and selects its shell.
func Open(path string) (*Workspace, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace %q: %w", path, err)
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open workspace %q: %w", abs, err)
	}
	displayPath := abs
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		displayPath = userRelativePath(abs, home)
	}
	return &Workspace{path: abs, displayPath: displayPath, defaultShellPath: resolveDefaultShellPath(), root: root}, nil
}

// Path returns the absolute path used to identify the workspace to processes
// launched in it.
func (w *Workspace) Path() string {
	return w.path
}

// DefaultShellPath returns the shell selected when this workspace was opened.
func (w *Workspace) DefaultShellPath() string {
	return w.defaultShellPath
}

// DisplayPath returns the stable user-facing form of Path. Locations inside
// the user's home directory use a leading ~; other paths remain absolute.
func (w *Workspace) DisplayPath() string {
	return w.displayPath
}

// FS returns a read-only filesystem view confined to the workspace.
func (w *Workspace) FS() fs.FS {
	return w.root.FS()
}

// OSRoot returns the borrowed confined root used by workspace mutations.
// The Workspace retains ownership; callers must not close the returned root.
func (w *Workspace) OSRoot() *os.Root {
	return w.root
}

// Close releases the workspace root.
func (w *Workspace) Close() error {
	return w.root.Close()
}

// LocalPath converts name, a path relative to the workspace at root written
// in the platform's syntax, to the slash-separated form fs.FS and os.Root take.
// It rejects absolute paths, paths that leave the workspace, and names the
// platform cannot store, so every tool reports them the same way.
func LocalPath(root, name string) (string, error) {
	if name == "" {
		return "", errors.New("path must not be empty")
	}
	if filepath.IsAbs(name) || filepath.VolumeName(name) != "" || os.IsPathSeparator(name[0]) {
		if rel, err := filepath.Rel(root, name); err == nil && filepath.IsAbs(name) && filepath.IsLocal(rel) {
			return "", fmt.Errorf("path must be workspace-relative, not absolute: use %q", filepath.ToSlash(rel))
		}
		return "", errors.New("path must be workspace-relative, not absolute")
	}
	// On Windows, Abs cleans names as os.Root does, dropping trailing dots and
	// spaces, so callers report the name the file actually gets.
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(base, name))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(base, full)
	if err != nil {
		return "", fmt.Errorf("%q is not a valid file name", name)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path is outside the workspace")
	}
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%q is not a valid file name", name)
	}
	return filepath.ToSlash(rel), nil
}

func userRelativePath(path, home string) string {
	if home == "" {
		return path
	}
	relative, err := filepath.Rel(home, path)
	if err != nil {
		return path
	}
	switch {
	case relative == ".":
		return "~"
	case relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)):
		return filepath.Join("~", relative)
	default:
		return path
	}
}
