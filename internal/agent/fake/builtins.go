package fake

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	stjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// builtins are the functions a script can call. They know the wire protocol,
// never tool input schemas.
var builtins = starlark.StringDict{
	"say":        starlark.NewBuiltin("say", builtinSay),
	"think":      starlark.NewBuiltin("think", builtinThink),
	"tool":       starlark.NewBuiltin("tool", builtinTool),
	"call":       starlark.NewBuiltin("call", builtinCall),
	"raw":        starlark.NewBuiltin("raw", builtinRaw),
	"random":     starlark.NewBuiltin("random", builtinRandom),
	"fail":       starlark.NewBuiltin("fail", builtinFail),
	"sleep":      starlark.NewBuiltin("sleep", builtinSleep),
	"help":       starlark.NewBuiltin("help", builtinHelp),
	"breakpoint": starlark.NewBuiltin("breakpoint", builtinBreakpoint),
	"START":      startPosition,
	"END":        endPosition,
	"json":       stjson.Module,
}

// spec is one tool call requested by a script.
type spec struct {
	id    string // message id and tool call id
	name  string
	input string // JSON sent verbatim
	out   string // server tools only
}

// builtinSay streams answer text.
func builtinSay(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return streamText(thread, b, args, kwargs, assistant.ContentMarkdownFragment)
}

// builtinThink streams reasoning.
func builtinThink(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return streamText(thread, b, args, kwargs, assistant.ContentThinking)
}

func streamText(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple, typ string) (starlark.Value, error) {
	var text string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &text); err != nil {
		return nil, err
	}
	return starlark.None, runOf(thread).text(typ, text)
}

// text emits s when live and counts its words for the default usage.
func (r *run) text(typ, s string) error {
	if err := r.sideEffectError("say/think"); err != nil {
		return err
	}
	if !r.live() {
		return nil
	}
	r.words += len(strings.Fields(s))
	return emitErr(r.out.text(typ, s))
}

// builtinTool emits server tool calls, then their results. break_input
// stops at a breakpoint while streaming each call's input, at the position
// at= names; break_before_results stops after the calls, before any result.
func builtinTool(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var first, input starlark.Value
	var at starlark.Value = starlark.None
	var out, ns, title, detail, breakInput, breakBefore string
	isErr, stream := false, true
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"name_or_calls", &first, "input?", &input, "out?", &out, "err?", &isErr,
		"ns?", &ns, "title?", &title, "detail?", &detail, "stream?", &stream,
		"break_input?", &breakInput, "at?", &at, "break_before_results?", &breakBefore); err != nil {
		return nil, err
	}
	specs, _, err := unpackSpecs(thread, first, input, out)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	r := runOf(thread)
	if err := r.planBreaks(specs, stream, breakInput, at, breakBefore); err != nil {
		return nil, err
	}
	if err := r.sideEffectError(b.Name()); err != nil {
		return nil, err
	}
	if !r.live() {
		return starlark.None, nil
	}
	style := toolEmit{stream: stream, namespace: ns, breakInput: breakInput, at: at}
	for i := range specs {
		specs[i].id = r.out.nextID()
		if err := r.emitToolCall(specs[i], style); err != nil {
			return nil, err
		}
	}
	if err := r.stopAt(breakBefore); err != nil {
		return nil, err
	}
	status := assistant.ToolStatusSuccess
	if isErr {
		status = assistant.ToolStatusError
	}
	for _, s := range specs {
		content := assistant.ToolResultContent(s.id, "", status, s.out)
		content.Tool.Title = title
		if detail != "" {
			content.Tool.Detail = &assistant.MarkdownPayload{Content: detail}
		}
		if err := r.out.emit(assistant.AssistantMessage(s.id, content)); err != nil {
			return nil, emitErr(err)
		}
	}
	return starlark.None, nil
}

// builtinCall emits one round of client tool calls and ends the Send. On
// re-execution it returns that round's recorded responses instead.
// break_input stops at a breakpoint while streaming each call's input, at
// the position at= names.
func builtinCall(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var first, input starlark.Value
	var at starlark.Value = starlark.None
	var breakInput string
	stream := true
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "name_or_calls", &first, "input?", &input,
		"stream?", &stream, "break_input?", &breakInput, "at?", &at); err != nil {
		return nil, err
	}
	specs, single, err := unpackSpecs(thread, first, input, "")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	r := runOf(thread)
	if err := r.planBreaks(specs, stream, breakInput, at, ""); err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return starlark.Tuple{}, nil // no calls, no round: the script continues
	}
	if err := r.sideEffectError(b.Name()); err != nil {
		return nil, err
	}
	if !r.live() {
		return r.replayRound(len(specs), single)
	}
	for _, s := range specs {
		s.id = r.out.nextID()
		if s.name == assistant.ApprovalRequestTool {
			s.input = withToolCallID(s.input, s.id)
		}
		if err := r.emitToolCall(s, toolEmit{client: true, stream: stream, breakInput: breakInput, at: at}); err != nil {
			return nil, err
		}
	}
	return nil, errRoundEnd
}

// replayRound returns the recorded responses of the next round.
func (r *run) replayRound(n int, single bool) (starlark.Value, error) {
	responses := r.results[r.round]
	r.round++
	if len(responses) != n {
		return nil, fmt.Errorf("call: got %d tool responses for %d calls", len(responses), n)
	}
	values := make(starlark.Tuple, n)
	for i, response := range responses {
		values[i] = resultValue(response)
	}
	if single {
		return values[0], nil
	}
	return values, nil
}

// toolEmit is how one tool call goes on the wire.
type toolEmit struct {
	client     bool           // client_tool_call rather than a server tool_call
	stream     bool           // stream the input when the engine asks for it
	namespace  string         // server tools only
	breakInput string         // breakpoint while streaming the input
	at         starlark.Value // where break_input stops; None is halfway
}

// emitToolCall emits the call's streamed input when requested, then the
// final call. The message id is the tool call id.
func (r *run) emitToolCall(s spec, style toolEmit) error {
	if style.stream && r.opts.StreamToolCallInput {
		if err := r.out.emit(assistant.AssistantMessage(s.id, toolCallStarted(s.id, s.name, style.client))); err != nil {
			return emitErr(err)
		}
		cut := len(s.input)
		if style.breakInput != "" {
			var err error
			if cut, err = inputCut(s, style.at); err != nil {
				return err
			}
		}
		if err := r.emitDeltas(s.id, s.input[:cut]); err != nil {
			return err
		}
		if err := r.stopAt(style.breakInput); err != nil {
			return err
		}
		if err := r.emitDeltas(s.id, s.input[cut:]); err != nil {
			return err
		}
	}
	content := assistant.ToolCallContent(s.id, s.name, s.input)
	if style.client {
		content = clientToolCall(s.id, s.name, s.input)
	}
	if style.namespace != "" {
		content.Tool.Metadata.Namespace = &style.namespace
	}
	return emitErr(r.out.emit(assistant.AssistantMessage(s.id, content)))
}

// emitDeltas streams part of one tool call's input in chunks.
func (r *run) emitDeltas(id, input string) error {
	for _, chunk := range chunkRunes(input, inputChunkRunes) {
		if err := r.out.emit(assistant.AssistantMessage(id, toolCallInputDelta(id, chunk))); err != nil {
			return emitErr(err)
		}
	}
	return nil
}

// builtinRandom streams a pseudo-random answer. An explicit string or int
// seed always reproduces the same answer. Without one, the seed is the
// turn's index in the conversation and the call's position in the script:
// successive turns differ, yet a fresh conversation replays the same
// sequence, and replayed rounds reproduce their seeds because the position
// counts replayed calls too.
func builtinRandom(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var seed starlark.Value = starlark.None
	var sizeValue starlark.Value = starlark.None
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "seed?", &seed, "size?", &sizeValue); err != nil {
		return nil, err
	}
	r := runOf(thread)
	r.randoms++
	if err := r.sideEffectError(b.Name()); err != nil {
		return nil, err
	}
	var n int64
	switch s := seed.(type) {
	case starlark.NoneType:
		n = int64(hashString(fmt.Sprintf("turn %d, random %d", r.turn, r.randoms)))
	case starlark.String:
		n = int64(hashString(string(s)))
	case starlark.Int:
		v, ok := s.Int64()
		if !ok {
			return nil, errors.New("random: seed out of int64 range")
		}
		n = v
	default:
		return nil, fmt.Errorf("random: seed must be a string or an int, not %s", seed.Type())
	}
	size, err := randomSize(sizeValue)
	if err != nil {
		return nil, err
	}
	if !r.live() {
		return starlark.None, nil
	}
	words, err := streamRandom(r.out, n, size)
	r.words += words
	return starlark.None, emitErr(err)
}

const maxRandomSize = 16 << 20

func randomSize(value starlark.Value) (int, error) {
	if value == starlark.None {
		return 0, nil
	}
	size, ok := value.(starlark.Int)
	if !ok {
		return 0, fmt.Errorf("random: size must be an int, not %s", value.Type())
	}
	n, ok := size.Int64()
	if !ok || n <= 0 || n > maxRandomSize {
		return 0, fmt.Errorf("random: size must be between 1 and %d bytes", maxRandomSize)
	}
	return int(n), nil
}

// builtinRaw emits one message whose content (and optional results) are
// decoded by the real assistant decoders.
func builtinRaw(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var content *starlark.Dict
	var results starlark.Value = starlark.None
	var id string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "content", &content, "results?", &results, "id?", &id); err != nil {
		return nil, err
	}
	r := runOf(thread)
	if err := r.sideEffectError(b.Name()); err != nil {
		return nil, err
	}
	if !r.live() {
		return starlark.None, nil
	}
	msg := assistant.AssistantMessage(cmp.Or(id, r.out.nextID()), assistant.Content{})
	if err := decodeInto(thread, content, &msg.Content); err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	if msg.Content.Type == assistant.ContentClientToolCall {
		return nil, errors.New("raw: use call() for client tool calls")
	}
	if results != starlark.None {
		msg.Results = &assistant.Results{}
		if err := decodeInto(thread, results, msg.Results); err != nil {
			return nil, fmt.Errorf("%s: %w", b.Name(), err)
		}
		r.sawUsage = r.sawUsage || msg.Results.Usage != nil
	}
	return starlark.None, emitErr(r.out.emit(msg))
}

// builtinFail makes Send return the error the real client would return. It
// never runs during replay: a failed round has no continuation.
func builtinFail(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var kind starlark.Value
	var msg string
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "kind", &kind, "msg?", &msg); err != nil {
		return nil, err
	}
	if err := runOf(thread).sideEffectError(b.Name()); err != nil {
		return nil, err
	}
	err := failure(kind, msg)
	if err == nil {
		return nil, errors.New(`fail: kind must be 400-599, "net", or "timeout"`)
	}
	return nil, &abort{err}
}

// failure maps fail()'s kind to a client-shaped error, or nil for an
// unknown kind.
func failure(kind starlark.Value, msg string) error {
	switch k := kind.(type) {
	case starlark.Int:
		status, ok := k.Int64()
		if !ok || status < 400 || status > 599 {
			return nil
		}
		return &assistant.APIError{StatusCode: int(status), Title: http.StatusText(int(status)), Detail: msg}
	case starlark.String:
		switch k {
		case "net":
			return &net.OpError{Op: "read", Net: "tcp", Err: errors.New(cmp.Or(msg, "connection reset by peer"))}
		case "timeout":
			return fmt.Errorf("%s: %w", cmp.Or(msg, "stream idle timeout"), context.DeadlineExceeded)
		}
	}
	return nil
}

// builtinSleep pauses the stream, honoring cancellation.
func builtinSleep(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var d string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &d); err != nil {
		return nil, err
	}
	duration, err := time.ParseDuration(d)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	r := runOf(thread)
	if err := r.sideEffectError(b.Name()); err != nil {
		return nil, err
	}
	if !r.live() {
		return starlark.None, nil
	}
	t := time.NewTimer(duration)
	defer t.Stop()
	select {
	case <-r.out.ctx.Done():
		return nil, &abort{r.out.ctx.Err()}
	case <-t.C:
		return starlark.None, nil
	}
}

// builtinHelp answers with the built-ins and the client tools the engine
// declared, so tool schemas are discovered rather than duplicated.
func builtinHelp(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	r := runOf(thread)
	if err := r.sideEffectError(b.Name()); err != nil {
		return nil, err
	}
	var sb strings.Builder
	sb.WriteString(helpText)
	fmt.Fprintf(&sb, "\nContinue a script stopped at `breakpoint(name)` with `touch %s`. Esc cancels it instead. "+
		"Every fake process shares that directory: use distinct names for concurrent sessions.\n",
		filepath.Join(r.continueDir, "<name>"))
	sb.WriteString("\n## Client tools\n\n")
	for _, tool := range r.opts.ClientTools {
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&sb, "- `%s` `%s`\n", tool.Name, schema)
	}
	return starlark.None, r.text(assistant.ContentMarkdownFragment, sb.String())
}

const helpText = "## Fake backend scripts\n\n" +
	"Every message is a Starlark script. Statements may be separated by `;`, but `if`/`for` " +
	"must start a new line (Shift+Enter or Ctrl+J).\n\n" +
	"| Built-in | Emits |\n| --- | --- |\n" +
	"| `load(\"file.star\", \"name\", ...)` | imports definitions/data from a deterministic local Starlark module |\n" +
	"| `say(text)` | streamed answer text |\n" +
	"| `think(text)` | streamed reasoning |\n" +
	"| `random(seed=None, size=None)` | a pseudo-random answer; `size` requests at least that many Markdown bytes |\n" +
	"| `tool(name, input, out=, err=, ns=, title=, detail=, stream=, break_input=, at=, break_before_results=)` or `tool([(name, input, out), ...])` | server tool calls, then results |\n" +
	"| `call(name, input, stream=, break_input=, at=)` or `call([(name, input), ...])` | one round of client tool calls; returns results (`ok`, `status`, `title`, `output`) |\n" +
	"| `raw(content, results=, id=)` | any other wire content |\n" +
	"| `fail(429)`, `fail(\"net\")`, `fail(\"timeout\")` | a backend failure |\n" +
	"| `sleep(\"2s\")` | a stall |\n" +
	"| `breakpoint(name)` | stops until its continue file appears (see below) |\n" +
	"| `break_input=name, at=` on `tool`/`call` | stops while streaming input: halfway by default, or `at=START`, `at=END`, `at=\"text\"` (right after it) |\n" +
	"| `break_before_results=name` on `tool` | stops after the calls, before any result |\n" +
	"| `kitchen()` | every output type once |\n" +
	"| `json.encode`, `json.decode`, `true`, `false`, `null` | JSON helpers |\n"

// unpackSpecs accepts a name (with input and out given separately) or a
// list/tuple of (name, input) or (name, input, out) tuples. single reports
// the name form.
func unpackSpecs(thread *starlark.Thread, first, input starlark.Value, out string) ([]spec, bool, error) {
	if name, ok := first.(starlark.String); ok {
		in, err := toolInput(thread, input)
		if err != nil {
			return nil, false, err
		}
		return []spec{{name: string(name), input: in, out: out}}, true, nil
	}
	calls, ok := first.(starlark.Indexable) // checked after String: strings are indexable too
	if !ok || input != nil {
		return nil, false, errors.New("want a tool name or a list of (name, input) tuples")
	}
	specs := make([]spec, calls.Len())
	for i := range specs {
		s, err := unpackItem(thread, calls.Index(i), out)
		if err != nil {
			return nil, false, fmt.Errorf("item %d: %w", i, err)
		}
		specs[i] = s
	}
	return specs, false, nil
}

// unpackItem reads one (name, input) or (name, input, out) tuple.
func unpackItem(thread *starlark.Thread, v starlark.Value, out string) (spec, error) {
	item, ok := v.(starlark.Tuple)
	if !ok || len(item) < 2 || len(item) > 3 {
		return spec{}, errors.New("want (name, input) or (name, input, out)")
	}
	name, ok := item[0].(starlark.String)
	if !ok {
		return spec{}, errors.New("name must be a string")
	}
	in, err := toolInput(thread, item[1])
	if err != nil {
		return spec{}, err
	}
	if len(item) == 3 {
		o, ok := item[2].(starlark.String)
		if !ok {
			return spec{}, errors.New("out must be a string")
		}
		out = string(o)
	}
	return spec{name: string(name), input: in, out: out}, nil
}

// toolInput sends a string verbatim (it may be invalid JSON on purpose) and
// encodes anything else, with no input meaning an empty object.
func toolInput(thread *starlark.Thread, v starlark.Value) (string, error) {
	switch v := v.(type) {
	case nil, starlark.NoneType:
		return "{}", nil
	case starlark.String:
		return string(v), nil
	default:
		return encodeOrdered(thread, v, map[starlark.Value]bool{})
	}
}

// encodeOrdered is json.encode, except that dict keys keep the order they
// were written in. json.encode sorts them, but input streams in key order,
// and a real model writes "path" before "content": a sorted write_file
// input would stop mid-input before its preview knows which file it is.
// path holds the lists and dicts being encoded: like json.encode, a value
// that contains itself is an error, not endless recursion that would crash
// the process. Tuples cannot close a cycle without a list or dict.
func encodeOrdered(thread *starlark.Thread, v starlark.Value, path map[starlark.Value]bool) (string, error) {
	switch v.(type) {
	case *starlark.Dict, *starlark.List:
		if path[v] {
			return "", errors.New("cycle in JSON structure")
		}
		path[v] = true
		defer delete(path, v)
	}
	var parts []string
	start, end := "[", "]"
	switch v := v.(type) {
	case *starlark.Dict:
		start, end = "{", "}"
		for _, item := range v.Items() {
			key, ok := item[0].(starlark.String)
			if !ok {
				return "", fmt.Errorf("tool input keys must be strings, got %s", item[0].Type())
			}
			value, err := encodeOrdered(thread, item[1], path)
			if err != nil {
				return "", err
			}
			name, _ := json.Marshal(string(key))
			parts = append(parts, string(name)+":"+value)
		}
	case *starlark.List, starlark.Tuple:
		iter := starlark.Iterate(v)
		defer iter.Done()
		var item starlark.Value
		for iter.Next(&item) {
			value, err := encodeOrdered(thread, item, path)
			if err != nil {
				return "", err
			}
			parts = append(parts, value)
		}
	default:
		return encodeJSON(thread, v)
	}
	return start + strings.Join(parts, ",") + end, nil
}

// encodeJSON calls json.encode so Starlark owns value conversion.
func encodeJSON(thread *starlark.Thread, v starlark.Value) (string, error) {
	s, err := starlark.Call(thread, stjson.Module.Members["encode"], starlark.Tuple{v}, nil)
	if err != nil {
		return "", err
	}
	return string(s.(starlark.String)), nil
}

// decodeInto converts a Starlark value to JSON and decodes it into dst.
func decodeInto(thread *starlark.Thread, v starlark.Value, dst any) error {
	s, err := encodeJSON(thread, v)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(s), dst)
}

// withToolCallID adds the tool_call_id an approval_request must repeat,
// unless the input already has one or is not a JSON object.
func withToolCallID(input, id string) string {
	var fields map[string]json.RawMessage // raw values keep numbers exact
	if json.Unmarshal([]byte(input), &fields) != nil || fields == nil {
		return input
	}
	if _, ok := fields["tool_call_id"]; ok {
		return input
	}
	idJSON, err := json.Marshal(id)
	if err != nil {
		return input
	}
	fields["tool_call_id"] = idJSON
	b, err := json.Marshal(fields)
	if err != nil {
		return input
	}
	return string(b)
}

// chunkRunes splits s into pieces of at most n runes.
func chunkRunes(s string, n int) []string {
	var chunks []string
	for s != "" {
		i := 0
		for count := 0; i < len(s) && count < n; count++ {
			_, size := utf8.DecodeRuneInString(s[i:])
			i += size
		}
		chunks = append(chunks, s[:i])
		s = s[i:]
	}
	return chunks
}

// resultValue exposes one client tool response to the script.
func resultValue(r assistant.ClientToolResponse) starlark.Value {
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"ok":     starlark.Bool(r.Status == assistant.ToolStatusSuccess),
		"status": starlark.String(r.Status),
		"title":  starlark.String(r.Title),
		"output": starlark.String(r.Metadata.Output),
	})
}
