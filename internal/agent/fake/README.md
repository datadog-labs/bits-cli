# Fake backend

`BITS_FAKE_BACKEND=1` runs Bits offline against this package, in the TUI or
with `bits run`. It implements `agent.Backend` plus conversation history,
listing, and the current user, so the real engine, tools, transcript, and
rendering run unchanged. Only the network is faked.

Conversations live in memory for the life of the process: `/resume` and
`/status` work within a session, but a new process knows no earlier
conversation, so `--conversation` with an id from a previous run is not
found.

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
| `tool(name, input, out=, err=, ns=, title=, detail=, stream=, break_input=, at=, break_before_results=)` or `tool([(name, input, out), …])` | server tool calls, then their results |
| `call(name, input, stream=, break_input=, at=)` or `call([(name, input), …])` | one round of client tool calls, run by the real engine; returns results with `ok`, `status`, `title`, `output` |
| `raw(content, results=, id=)` | any other wire content, decoded by the real decoder |
| `fail(status)`, `fail("net")`, `fail("timeout")` | a backend failure after the output so far |
| `sleep("2s")` | a stall |
| `breakpoint(name)` | nothing: stops the script until it is continued (see [Breakpoints](#breakpoints)) |
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
- `call` really runs local tools. Under the default `manual` permissions mode,
  scripted `write_file`, `edit_file`, and `exec_command` pause on the
  permission panel before they change the workspace; under `skip-permissions`
  they change it without asking.

## Breakpoints

A breakpoint stops the script at a precise point of the stream, so you can
look at the TUI, resize it, press keys, or cancel, and then let the turn go
on. It is continued by creating a file named after it:

```text
say("| service | p95 |\n| --- | --- |\n| web | 42ms |"); breakpoint("table"); say("All services healthy.")
```

```sh
touch "${TMPDIR:-/tmp}/bits-fake/continue/table"   # continue "table"
```

`help()` prints the exact continue directory. Keyword arguments stop inside
a built-in; their value is the breakpoint's name:

```text
call("write_file", {"path": "a.txt", "content": "alpha\nbravo\n"}, break_input="diff")
call("write_file", {"path": "a.txt", "content": "alpha\nbravo\n"}, break_input="diff", at="bravo")
call("write_file", {"path": "a.txt", "content": "alpha\nbravo\n"}, break_input="diff", at=START)
call("write_file", {"path": "a.txt", "content": "alpha\nbravo\n"}, break_input="diff", at=END)
tool("search_logs", {"query": "status:error"}, out="12 results", break_before_results="search")
```

- `break_input` stops while streaming each call's input, before its final
  call, on `call()` and `tool()`. It needs streamed input. `at=` says where:
  - omitted: halfway, with at least one character streamed;
  - `START`: after `tool_call_started`, before any input;
  - `END`: all input streamed, final call not sent;
  - `"text"`: right after the first occurrence of `text` in the streamed
    input JSON, e.g. `at="bravo"` or `at='"path":'`. It must be in every
    call's input, or the script fails before anything is emitted.
- `break_before_results` stops after `tool()`'s calls, before any result.
- Each continue file continues one stop and is consumed, so the same script
  stops again next time. A file created before the script gets there
  continues it on arrival.
- Esc cancels a stopped turn as usual; a breakpoint nobody continues is how
  to hold a state until you interrupt it.
- Breakpoints in replayed rounds never stop again: they already passed.
- Names match `[a-z0-9_-]+`, so a continue file never leaves its directory.
- The continue directory is shared by every fake process of the user, so
  the path stays predictable. Two sessions stopped at the same name race for
  one continue file: give concurrent sessions distinct names, e.g.
  `breakpoint("qa1-table")`.
- Tests set `Fake.ContinueDir` to a `t.TempDir()`. The script stops right
  after its last output, so a test that sees that output knows the backend
  is stopped, acts, then writes the continue file.

## How rounds work

A `call` ends the current `Send`, as a real client tool call pauses the
server stream. When the engine sends the tool results back, the script runs
again from the top: earlier `call`s return their recorded results, output
before them is not re-emitted, and execution stops at the next `call`.
Apart from the snapshotted module sources and breakpoint continue files,
Starlark has no clock, ambient randomness, or I/O here, so every run is deterministic and nothing stays alive
between `Send`s.

Built-ins know the wire protocol, never tool input schemas. Those are owned
by `internal/tools/spec` and listed at runtime by `help()`.
