package fake

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"slices"
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

// scriptOptions lets one-liners use top-level if/for and reassign globals.
// while, recursion, and set stay disabled.
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
	out      *emitter
	opts     assistant.SendOptions
	results  [][]assistant.ClientToolResponse // responses to earlier rounds
	round    int                              // call() invocations so far
	sawUsage bool                             // raw() sent usage in the live part
	words    int                              // words said live, for default usage
}

// live reports whether execution has passed every answered round. Built-ins
// emit only when live; before that they replay silently.
func (r *run) live() bool { return r.round == len(r.results) }

// fenceOpen starts a multi-line script; a line holding only "```" ends it.
const fenceOpen = "```fake"

// scriptSource reports whether text is a script and returns its source. A
// one-line script follows "::". A longer one is a ```fake fenced block whose
// lines lose their shared indentation, so a copied block runs as pasted. A
// malformed script is still a script: err explains how to fix it.
func scriptSource(text string) (src string, scripted bool, err error) {
	text = strings.TrimSpace(text)
	if line, ok := strings.CutPrefix(text, "::"); ok {
		if strings.Contains(line, "\n") {
			return "", true, errors.New("a \"::\" script is one line: use a ```fake block for several lines")
		}
		return strings.TrimSpace(line), true, nil
	}
	first, rest, _ := strings.Cut(text, "\n")
	switch {
	case !strings.HasPrefix(first, fenceOpen):
		return "", false, nil
	case strings.TrimSpace(first) != fenceOpen:
		return "", true, errors.New("put the script on the lines after ```fake")
	}
	lines := strings.Split(rest, "\n")
	end := slices.IndexFunc(lines, func(line string) bool { return strings.TrimSpace(line) == "```" })
	if end < 0 {
		end = len(lines) // the closing fence is optional
	} else if strings.TrimSpace(strings.Join(lines[end+1:], "\n")) != "" {
		return "", true, errors.New("unexpected text after the closing ```")
	}
	return dedent(strings.Join(lines[:end], "\n")), true, nil
}

// dedent removes the whitespace prefix shared by every non-blank line.
func dedent(s string) string {
	lines := strings.Split(s, "\n")
	prefix, first := "", true
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		switch {
		case first:
			prefix, first = indent, false
		case !strings.HasPrefix(indent, prefix):
			prefix = commonPrefix(prefix, indent)
		}
	}
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, prefix)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func commonPrefix(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return a[:i]
}

// runScript executes one round of a scripted turn by re-running the whole
// script: earlier call()s return their recorded responses and only the
// output after them is emitted. Starlark is deterministic, so this reproduces
// the same program state without keeping anything alive between Sends.
func runScript(out *emitter, opts assistant.SendOptions, turn scriptTurn) error {
	r := &run{out: out, opts: opts, results: turn.results}
	thread := &starlark.Thread{Name: "fake", Print: func(*starlark.Thread, string) {}}
	thread.SetLocal(runKey, r)
	thread.SetMaxExecutionSteps(maxSteps)
	stop := context.AfterFunc(out.ctx, func() { thread.Cancel("cancelled") })
	defer stop()

	env, err := prelude(thread)
	if err != nil {
		return err // a broken prelude is a bug, not a script error
	}
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
		return answerScriptError(out, err, turn.src)
	}
	if r.sawUsage {
		return nil
	}
	return out.emit(usageMessage(out.nextID(), r.words))
}

// answerScriptError answers a broken script instead of failing the turn, so
// the mistake is visible where the script was typed.
func answerScriptError(out *emitter, err error, src string) error {
	if err := out.text(assistant.ContentMarkdownFragment, scriptError(err, src)); err != nil {
		return err
	}
	return out.emit(usageMessage(out.nextID(), 0))
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

// scriptError renders a script failure as Markdown: the offending line with
// a caret and a hint for static errors, the backtrace for runtime errors.
func scriptError(err error, src string) string {
	detail := err.Error()
	var syntaxErr syntax.Error
	var resolveErrs resolve.ErrorList
	var evalErr *starlark.EvalError
	switch {
	case errors.As(err, &syntaxErr):
		detail += explain(src, syntaxErr.Pos, syntaxErr.Msg)
	case errors.As(err, &resolveErrs) && len(resolveErrs) > 0:
		detail += explain(src, resolveErrs[0].Pos, resolveErrs[0].Msg)
	case errors.As(err, &evalErr):
		detail = evalErr.Backtrace()
	}
	return "**fake script error**\n\n```\n" + detail + "\n```\n"
}

// explain shows the source line at pos with a caret under its column, plus
// a hint for the mistake one-liners invite.
func explain(src string, pos syntax.Position, msg string) string {
	lines := strings.Split(src, "\n")
	if pos.Line < 1 || int(pos.Line) > len(lines) {
		return ""
	}
	line := lines[pos.Line-1]
	out := fmt.Sprintf("\n\n%s\n%s^", line, strings.Repeat(" ", max(int(pos.Col)-1, 0)))
	if strings.HasPrefix(msg, "got if,") || strings.HasPrefix(msg, "got for,") {
		out += "\n\nif/for cannot follow \";\": use a ```fake block for several lines."
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
