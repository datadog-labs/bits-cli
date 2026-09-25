package fake

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"strings"

	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/DataDog/bits-cli/internal/assistant"
)

//go:embed prelude.star
var preludeSrc string

const (
	runKey          = "fake.run"
	maxSteps        = 1_000_000
	inputChunkRunes = 16
)

// scriptOptions allows top-level if/for and global reassignment, so a
// message reads like a plain sequence of statements. while, recursion, and
// set stay disabled.
var scriptOptions = &syntax.FileOptions{TopLevelControl: true, GlobalReassign: true}

// errRoundEnd stops execution after call() emitted a round of client calls.
var errRoundEnd = errors.New("fake: round end")

// abort carries an error out of the interpreter unchanged: fail(), an error
// from the engine's callback, or cancellation.
type abort struct{ err error }

func (a *abort) Error() string { return a.err.Error() }
func (a *abort) Unwrap() error { return a.err }

// run is the state of one script execution, shared with the built-ins
// through the Starlark thread.
type run struct {
	out         *emitter
	opts        assistant.SendOptions
	results     [][]assistant.ClientToolResponse // responses to earlier rounds
	round       int                              // call() invocations so far
	sawUsage    bool                             // raw() sent usage in the live part
	words       int                              // words said live, for default usage
	turn        int                              // the turn's index in its conversation
	randoms     int                              // random() invocations so far, replayed or live
	moduleDepth int                              // nested module initialization depth
	continueDir string                           // where breakpoints look for continue files
}

// live reports whether execution has passed every answered round. Built-ins
// emit only when live; before that they replay silently.
func (r *run) live() bool { return r.round == len(r.results) }

// sideEffectError keeps module initialization declarative. Functions defined
// by a module run later on the caller's thread and are allowed to emit.
func (r *run) sideEffectError(name string) error {
	if r.moduleDepth > 0 {
		return fmt.Errorf("%s: cannot run while loading a module", name)
	}
	return nil
}

// runScript executes one round of a scripted turn by re-running the whole
// script: earlier call()s return their recorded responses and only the
// output after them is emitted. Starlark is deterministic, so this reproduces
// the same program state without keeping anything alive between Sends.
func runScript(out *emitter, opts assistant.SendOptions, turn scriptTurn) error {
	r := &run{out: out, opts: opts, results: turn.results, turn: turn.index, continueDir: turn.continueDir}
	loader := newModuleLoader(out.ctx, turn.snapshot, r)
	thread := &starlark.Thread{Name: "fake", Print: func(*starlark.Thread, string) {}}
	thread.SetLocal(runKey, r)
	thread.SetLocal(modulePathKey, "")
	thread.SetMaxExecutionSteps(maxSteps)
	thread.Load = loader.Load

	env, err := prelude(thread)
	if err != nil {
		return err // a broken prelude is a bug, not a script error
	}
	// Armed after the prelude, so cancellation is reported as such rather
	// than as a broken prelude.
	stop := context.AfterFunc(out.ctx, func() { thread.Cancel("cancelled") })
	defer stop()
	_, err = starlark.ExecFileOptions(scriptOptions, thread, "script", turn.src, env)
	if ctxErr := out.ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var a *abort
	switch {
	case err == nil, errors.Is(err, errRoundEnd):
	case errors.As(err, &a):
		return a.err
	default:
		// A broken script is answered instead of failing the turn, so the
		// mistake is visible where the script was typed.
		if err := out.text(assistant.ContentMarkdownFragment, scriptError(err, turn.src)); err != nil {
			return err
		}
	}
	if r.sawUsage {
		return nil
	}
	return out.emit(usageMessage(out.nextID(), r.words))
}

// prelude executes prelude.star and returns the environment for scripts:
// the built-ins plus the prelude's globals.
func prelude(thread *starlark.Thread) (starlark.StringDict, error) {
	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, "prelude.star", preludeSrc, builtins)
	if err != nil {
		return nil, err
	}
	env := maps.Clone(builtins)
	maps.Copy(env, globals)
	return env, nil
}

// staticHint follows every syntax or resolve error, because the likeliest
// cause is a prose prompt typed out of habit.
const staticHint = "\nEvery message to the fake backend is a Starlark script: " +
	"try `random()`, `say(\"hi\")`, or `help()`.\n"

// scriptError renders a script failure as Markdown: the offending line with
// a caret and a hint for static errors, the backtrace for runtime errors.
func scriptError(err error, src string) string {
	detail, hint := err.Error(), ""
	var syntaxErr syntax.Error
	var resolveErrs resolve.ErrorList
	var evalErr *starlark.EvalError
	switch {
	case errors.As(err, &syntaxErr):
		detail += explain(src, syntaxErr.Pos, syntaxErr.Msg)
		hint = staticHint
	case errors.As(err, &resolveErrs) && len(resolveErrs) > 0:
		detail += explain(src, resolveErrs[0].Pos, resolveErrs[0].Msg)
		hint = staticHint
	case errors.As(err, &evalErr):
		detail = evalErr.Backtrace()
	}
	return "**fake script error**\n\n```\n" + detail + "\n```\n" + hint
}

// explain shows the source line at pos with a caret under its column, plus
// a hint for the mistake one-line scripts invite.
func explain(src string, pos syntax.Position, msg string) string {
	lines := strings.Split(src, "\n")
	if pos.Line < 1 || int(pos.Line) > len(lines) {
		return ""
	}
	line := lines[pos.Line-1]
	out := fmt.Sprintf("\n\n%s\n%s^", line, strings.Repeat(" ", max(int(pos.Col)-1, 0)))
	if strings.HasPrefix(msg, "got if,") || strings.HasPrefix(msg, "got for,") {
		out += "\n\nif/for cannot follow \";\": start it on a new line (Shift+Enter or Ctrl+J)."
	}
	return out
}

// runOf returns the execution state attached to the thread by runScript.
func runOf(thread *starlark.Thread) *run { return thread.Local(runKey).(*run) }

// emitErr wraps emitter failures so they leave the interpreter unchanged.
func emitErr(err error) error {
	if err != nil {
		return &abort{err}
	}
	return nil
}
