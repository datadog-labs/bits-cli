package fake

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"go.starlark.net/starlark"
)

// continuePoll is how often a stopped script looks for its continue file.
const continuePoll = 50 * time.Millisecond

// breakpointName keeps a breakpoint's continue file inside the continue
// directory: a name can never be a path.
var breakpointName = regexp.MustCompile(`^[a-z0-9_-]+$`)

// defaultContinueDir is where continue files land when Fake.ContinueDir is
// unset: per user, outside any workspace.
func defaultContinueDir() string {
	return filepath.Join(os.TempDir(), "bits-fake", "continue")
}

// builtinBreakpoint stops the script until its continue file appears or the
// turn is cancelled.
func builtinBreakpoint(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	r := runOf(thread)
	if len(args) == 0 && len(kwargs) == 0 {
		return nil, fmt.Errorf("a name is required, e.g. breakpoint(\"table\"); continue it with: touch %s",
			filepath.Join(r.continueDir, "table"))
	}
	var name string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &name); err != nil {
		return nil, err
	}
	if err := checkBreakpoint(name); err != nil {
		return nil, err
	}
	if err := r.sideEffectError(b.Name()); err != nil {
		return nil, err
	}
	return starlark.None, r.stopAt(name)
}

// checkBreakpoint validates a breakpoint name given to breakpoint() or a
// break_* keyword argument. An empty name means no breakpoint. Starlark
// already prefixes the message with the built-in's name.
func checkBreakpoint(name string) error {
	if name == "" || breakpointName.MatchString(name) {
		return nil
	}
	return fmt.Errorf("breakpoint name must match [a-z0-9_-]+, e.g. \"table\", got %q", name)
}

// stopAt stops at breakpoint name until its continue file appears, then
// consumes the file so each touch continues exactly one stop. A touch made
// before the script arrives continues it on arrival. Cancellation (Esc)
// always wins. Replayed rounds never stop: they already passed.
func (r *run) stopAt(name string) error {
	if name == "" || !r.live() {
		return nil
	}
	if err := os.MkdirAll(r.continueDir, 0o700); err != nil {
		return fmt.Errorf("breakpoint %q: %w", name, err)
	}
	path := filepath.Join(r.continueDir, name)
	tick := time.NewTicker(continuePoll)
	defer tick.Stop()
	for {
		switch err := os.Remove(path); {
		case err == nil:
			return nil
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("breakpoint %q: %w", name, err)
		}
		select {
		case <-r.out.ctx.Done():
			return &abort{r.out.ctx.Err()}
		case <-tick.C:
		}
	}
}
