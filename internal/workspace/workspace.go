// Package workspace provides confined access to the directory Bits is operating on.
package workspace

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Workspace owns a confined filesystem root and its display path.
//
// A Workspace is safe for concurrent use. Call Close when the process no
// longer needs it; values returned by OSRoot are borrowed and must not be
// closed independently.
type Workspace struct {
	path        string
	displayPath string
	root        *os.Root
}

// Open resolves path and opens it as a confined workspace.
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
	return &Workspace{path: abs, displayPath: displayPath, root: root}, nil
}

// Path returns the absolute path used to identify the workspace to processes
// launched in it.
func (w *Workspace) Path() string {
	return w.path
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
