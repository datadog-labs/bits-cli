# Fake backend

`BITS_FAKE_BACKEND=1` runs Bits offline against this package, in the TUI or
with `bits run`. It implements `agent.Backend` plus conversation history,
listing, and the current user, so the real engine, tools, transcript, and
rendering run unchanged. Only the network is faked.

## Scripts

Every message is a [Starlark](https://github.com/bazelbuild/starlark)
script whose built-ins emit exactly the wire output you ask for:

```text
random()
load("fixtures/incident.star", "run"); run()
say("# Title\n\n| a | b |\n| --- | --- |\n| 1 | 2 |")
think("checking"); r = call("read_file", {"path": "go.mod"}); say("first line: " + r.output.splitlines()[0])
a, b = call([("list_files", {"path": "."}), ("grep_files", {"pattern": "TODO"})])
g = call("approval_request", {"tool_name": "delete_dashboard", "tool_args": {}}); say("deleted" if g.ok else "kept")
tool("search", {"q": "p95"}, out="3 monitors", ns="datadog", detail="**3** monitors match")
raw({"type": "widget_def", "title": "p95", "widget_def": {}})
say("partial answer"); sleep("1s"); fail(503, "upstream overloaded")
kitchen()
help()
```

| Built-in | Emits |
| --- | --- |
| `load("file.star", "name", …)` | imports definitions/data from a local Starlark module |
| `say(text)`, `think(text)` | streamed answer text or reasoning |
| `random(seed=None)` | a pseudo-random answer: thinking, server tool calls, and Markdown |
| `tool(name, input, out=, err=, ns=, title=, detail=, stream=)` or `tool([(name, input, out), …])` | server tool calls, then their results |
| `call(name, input, stream=)` or `call([(name, input), …])` | one round of client tool calls, run by the real engine; returns results with `ok`, `status`, `title`, `output` |
| `raw(content, results=, id=)` | any other wire content, decoded by the real decoder |
| `fail(status)`, `fail("net")`, `fail("timeout")` | a backend failure after the output so far |
| `sleep("2s")` | a stall |
| `help()` | the built-ins and the declared client tools with their input schemas |
| `kitchen()` | every output type once, read-only |

A script may span several lines (Shift+Enter or Ctrl+J for a newline in the
TUI):

```text
r = call("read_file", {"path": "go.mod"})
if r.ok:
    say("first line: " + r.output.splitlines()[0])
else:
    fail(503)
```

`load()` uses native Starlark module semantics. Files are resolved below the
fake backend's script root, and loaded source is snapshotted for the lifetime
of the current turn. This matters when a client tool pauses the stream: the
continuation re-executes the script, but it still sees the same file bytes even
if the file changed on disk. A new user turn gets a fresh snapshot. Loaded
modules should define functions and data; wire-emitting built-ins are rejected
during module initialization but work when called by an exported function.

Notes:

- `random("hello")` or `random(42)` always streams the same answer. Without a
  seed, the answer depends on the turn's position in the conversation and the
  call's position in the script, so successive turns differ while a fresh
  conversation replays the same sequence.
- Statements may be separated by `;`, but `if`/`for` must start a new line
  (or use `a if cond else b`).
- A prose message is not a script: it is answered with the syntax error and a
  hint, like any other script mistake, instead of failing the turn.
- Tool input is streamed (`tool_call_started` and input deltas) whenever the
  engine asks for it; `stream=False` disables it for one call.
- `approval_request` calls get their `tool_call_id` filled in automatically.
- `load()` accepts Starlark source files, not arbitrary Python programs.
- `call` really runs local tools. Under the default `allow-all` approval mode,
  scripted `write_file`, `edit_file`, and `exec_command` change the workspace.

## How rounds work

A `call` ends the current `Send`, as a real client tool call pauses the
server stream. When the engine sends the tool results back, the script runs
again from the top: earlier `call`s return their recorded results, output
before them is not re-emitted, and execution stops at the next `call`.
Apart from the snapshotted module sources, Starlark has no clock, ambient
randomness, or I/O here, so every run is deterministic and nothing stays alive
between `Send`s.

Built-ins know the wire protocol, never tool input schemas. Those are owned
by `internal/tools/spec` and listed at runtime by `help()`.
