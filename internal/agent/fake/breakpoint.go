package fake

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

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

// inputPosition is the type of START and END: where break_input stops,
// distinct from text so an anchor can never be mistaken for a position.
type inputPosition string

const (
	startPosition inputPosition = "START" // before any input
	endPosition   inputPosition = "END"   // all input, before the final call
)

func (p inputPosition) String() string        { return string(p) }
func (inputPosition) Type() string            { return "position" }
func (inputPosition) Freeze()                 {}
func (inputPosition) Truth() starlark.Bool    { return starlark.True }
func (p inputPosition) Hash() (uint32, error) { return starlark.String(p).Hash() }

// planBreaks validates a built-in's break arguments before anything is
// emitted, so a mistake never leaves a half-streamed call behind. Anchors are
// checked against each input as written; emitToolCall cuts the final input.
func (r *run) planBreaks(specs []spec, style toolEmit, breakInput string, at starlark.Value, breakBefore string) error {
	for _, name := range []string{breakInput, breakBefore} {
		if err := checkBreakpoint(name); err != nil {
			return err
		}
	}
	if at != starlark.None && breakInput == "" {
		return errors.New("at needs break_input")
	}
	for _, s := range specs {
		if _, err := inputCut(s, at); err != nil {
			return err
		}
	}
	for _, s := range specs {
		if breakInput != "" && !r.streamsInput(s.name, style) {
			return errors.New("break_input needs streamed input, which is off here " +
				"(stream=False, or the tool did not opt into streamed input)")
		}
	}
	return nil
}

// inputCut is the byte offset in s.input where break_input stops: halfway
// by default (at least one character), or as at says. Anchors match the
// streamed input JSON, so the cut always falls on a character boundary.
func inputCut(s spec, at starlark.Value) (int, error) {
	switch at := at.(type) {
	case starlark.NoneType:
		half := (utf8.RuneCountInString(s.input) + 1) / 2
		cut := 0
		for range half {
			_, size := utf8.DecodeRuneInString(s.input[cut:])
			cut += size
		}
		return cut, nil
	case inputPosition:
		if at == startPosition {
			return 0, nil
		}
		return len(s.input), nil
	case starlark.String:
		if at == "" {
			return 0, errors.New("at needs non-empty text")
		}
		i := strings.Index(s.input, string(at))
		if i < 0 {
			return 0, fmt.Errorf("at: %q is not in the input of %s", string(at), s.name)
		}
		return i + len(at), nil
	default:
		return 0, fmt.Errorf("at must be START, END, or text, got %s", at.Type())
	}
}
