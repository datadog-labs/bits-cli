package fake

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

const (
	modulePathKey = "fake.module-path"
	maxModuleSize = 1 << 20
)

// sourceSnapshot is the immutable view of files used by one scripted turn.
// Files are read lazily, because a load statement may occur after a client
// tool round. Once read, a file never comes from disk again for this turn.
type sourceSnapshot struct {
	root string

	mu    sync.Mutex
	files map[string][]byte
}

func newSourceSnapshot(root string) *sourceSnapshot {
	return &sourceSnapshot{root: root, files: make(map[string][]byte)}
}

// moduleEntry is cached only for one interpreter execution. Starlark module
// values contain functions tied to that execution, so they must not be shared
// across replayed rounds.
type moduleEntry struct {
	globals starlark.StringDict
	err     error
}

// moduleLoader implements Starlark's Load hook. It owns path resolution and
// module caching while sourceSnapshot owns replay-stable file contents.
type moduleLoader struct {
	ctx      context.Context
	snapshot *sourceSnapshot
	run      *run

	cache   map[string]moduleEntry
	loading map[string]bool
}

func newModuleLoader(ctx context.Context, snapshot *sourceSnapshot, r *run) *moduleLoader {
	if snapshot == nil {
		// A nil snapshot is only possible for a turn assembled by an old caller;
		// keeping the fallback here makes the loader fail with a useful root error.
		snapshot = newSourceSnapshot("")
	}
	return &moduleLoader{
		ctx:      ctx,
		snapshot: snapshot,
		run:      r,
		cache:    make(map[string]moduleEntry),
		loading:  make(map[string]bool),
	}
}

// Load is the callback used by Starlark's native load("module", "name")
// statement. Module names are workspace-relative at the top level and
// relative to their importing module below it.
func (l *moduleLoader) Load(thread *starlark.Thread, module string) (starlark.StringDict, error) {
	id, path, err := l.resolve(thread, module)
	if err != nil {
		return nil, err
	}
	if entry, ok := l.cache[id]; ok {
		return entry.globals, entry.err
	}
	if l.loading[id] {
		return nil, fmt.Errorf("module cycle involving %q", id)
	}
	l.loading[id] = true
	defer delete(l.loading, id)

	src, err := l.snapshot.read(id, path)
	if err != nil {
		entry := moduleEntry{err: err}
		l.cache[id] = entry
		return nil, err
	}

	// A loaded module may define functions that call fake built-ins later, but
	// its initialization itself must be side-effect free. This prevents module
	// import order from becoming an observable part of replay.
	l.run.moduleDepth++
	defer func() { l.run.moduleDepth-- }()

	moduleThread := &starlark.Thread{
		Name:  "load " + id,
		Print: func(*starlark.Thread, string) {},
		Load:  l.Load,
	}
	moduleThread.SetLocal(runKey, l.run)
	moduleThread.SetLocal(modulePathKey, id)
	moduleThread.SetMaxExecutionSteps(maxSteps)
	stop := context.AfterFunc(l.ctx, func() { moduleThread.Cancel("cancelled") })
	defer stop()

	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{}, moduleThread, id, src, modulePredeclared())
	entry := moduleEntry{globals: globals, err: err}
	l.cache[id] = entry
	return globals, err
}

// resolve maps a requested module name to a logical module id and a verified
// filesystem path. The logical id is used for cache keys and diagnostics;
// the real path is used for the one read that populates the snapshot.
func (l *moduleLoader) resolve(thread *starlark.Thread, module string) (string, string, error) {
	if l.snapshot.root == "" {
		return "", "", errors.New("load: script root is unavailable")
	}
	requested := filepath.FromSlash(module)
	if module == "" || filepath.IsAbs(requested) {
		return "", "", fmt.Errorf("load: module path must be a non-empty relative path")
	}

	parent, _ := thread.Local(modulePathKey).(string)
	base := l.snapshot.root
	if parent != "" {
		base = filepath.Join(base, filepath.Dir(filepath.FromSlash(parent)))
	}
	candidate := filepath.Clean(filepath.Join(base, requested))
	if !within(baseRoot(l.snapshot.root), candidate) {
		return "", "", fmt.Errorf("load: module %q escapes the script root", module)
	}

	root, err := filepath.EvalSymlinks(baseRoot(l.snapshot.root))
	if err != nil {
		return "", "", fmt.Errorf("load: resolve script root: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", "", fmt.Errorf("load: resolve %q: %w", module, err)
	}
	if !within(root, realPath) {
		return "", "", fmt.Errorf("load: module %q escapes the script root", module)
	}

	rel, err := filepath.Rel(baseRoot(l.snapshot.root), candidate)
	if err != nil || rel == "." {
		return "", "", fmt.Errorf("load: invalid module path %q", module)
	}
	return filepath.ToSlash(filepath.Clean(rel)), realPath, nil
}

func baseRoot(root string) string {
	if absolute, err := filepath.Abs(root); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(root)
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *sourceSnapshot) read(id, path string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if src, ok := s.files[id]; ok {
		return src, nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", id, err)
	}
	if len(src) > maxModuleSize {
		return nil, fmt.Errorf("%q is larger than %d bytes", id, maxModuleSize)
	}
	s.files[id] = src
	return src, nil
}

// modulePredeclared gives modules the same wire built-ins as the main script,
// plus the lowercase aliases supplied by prelude.star.
func modulePredeclared() starlark.StringDict {
	env := maps.Clone(builtins)
	env["true"] = starlark.True
	env["false"] = starlark.False
	env["null"] = starlark.None
	return env
}
