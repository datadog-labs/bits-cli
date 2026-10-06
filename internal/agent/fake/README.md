# Fake backend

An offline `agent.Backend`: the real engine, tools, transcript, and rendering
run unchanged; only the network is faked. Every user message is a
[Starlark](https://github.com/bazelbuild/starlark) script whose built-ins emit
exactly the wire output asked for.

```sh
BITS_FAKE_BACKEND=1 bits                                   # TUI
BITS_FAKE_BACKEND=1 bits run --delivery adeep --prompt 'say("hi")'
```

Conversations live in memory for the process: `/resume` works within a
session, but an id from a previous run is not found.

## Scripts

```text
say("# Title\n\n| a | b |\n| --- | --- |\n| 1 | 2 |")
think("checking"); r = call("read_file", {"path": "go.mod"}); say(r.output.splitlines()[0])
a, b = call([("list_files", {"path": "."}), ("grep_files", {"pattern": "TODO"})])
g = call("approval_request", {"tool_name": "delete_dashboard", "tool_args": {}}); say("deleted" if g.ok else "kept")
load("internal/agent/fake/testdata/questions.star", "ask_user_question"); ask_user_question()
load("internal/agent/fake/testdata/disclosure.star", "all"); all()
```

| Built-in | Emits |
| --- | --- |
| `say(text)`, `think(text)` | streamed answer text or reasoning |
| `random(seed=None, size=None)` | a pseudo-random answer, at least `size` Markdown bytes |
| `tool(name, input, out=, err=, ns=, title=, detail=, stream=)` or `tool([(name, input, out), …])` | server tool calls, then their results |
| `call(name, input, stream=)` or `call([(name, input), …])` | one round of client tool calls, run by the real engine; returns results with `ok`, `status`, `title`, `output` |
| `raw(content, results=, id=)` | any other wire content, through the real decoder |
| `fail(status)`, `fail("net")`, `fail("timeout")` | a backend failure after the output so far |
| `sleep("2s")` | a stall |
| `breakpoint(name)` | nothing; stops until continued ([Breakpoints](#breakpoints)) |
| `push_conversation(fn, …, title=)` | nothing; records a new conversation, returns its id ([Pushed conversations](#pushed-conversations)) |
| `load("file.star", "name", …)` | imports definitions from a module below the script root (the working directory) |
| `help()` | the built-ins and the client tools' input schemas |
| `kitchen()` | every output type once, read-only |

- `;` separates statements, but `if`/`for` start a new line (Shift+Enter or
  Ctrl+J in the TUI).
- A script error, prose included, is answered as text instead of failing the
  turn.
- `call` really runs local tools: in `manual` mode writes and commands wait on
  the permission panel; in `skip-permissions` they run.
- Client tool input streams only when its available definition has
  `stream_input: true`; `stream=False` disables it for one call. Server tools
  opt in through `tool(..., stream=True)`. `approval_request` gets its
  `tool_call_id` filled in.
- `random("x")` or `random(42)` always gives the same answer. Unseeded, it
  depends on the turn and call position, so a fresh conversation replays the
  same sequence.
- Modules define functions and data; wire built-ins fail while a module
  loads but work when its functions run.
- Built-ins know the wire protocol, never tool schemas: those live in
  `internal/tools/spec` and are listed by `help()`.

## Rounds and replay

A `call` ends the `Send`, as a real client tool call pauses the stream. When
the engine sends the results back, the script runs again from the top:
earlier `call`s return their recorded results, earlier output is not
re-emitted, and execution stops at the next `call`. Starlark here has no
clock, I/O, or ambient randomness, and loaded files are snapshotted for the
turn, so every run is deterministic and nothing stays alive between `Send`s.

## Breakpoints

`breakpoint(name)` stops the stream until `$TMPDIR/bits-fake/continue/<name>`
exists (`help()` prints the path); Esc cancels instead.

```text
say("| web | 42ms |"); breakpoint("table"); say("All services healthy.")
call("write_file", {"path": "a.txt", "content": "alpha\nbravo\n"}, break_input="diff", at="bravo")
tool("search_logs", {"query": "status:error"}, out="12 results", break_before_results="search")
```

```sh
touch "${TMPDIR:-/tmp}/bits-fake/continue/table"
```

- `break_input=name` on `call`/`tool` stops while input streams: halfway by
  default, or `at=START`, `at=END`, or right after `at="text"` (which must
  occur in every call's input).
- `break_before_results=name` on `tool` stops after the calls, before any
  result.
- A continue file is consumed by one stop; one created early continues on
  arrival. Replayed rounds never stop again.
- Names match `[a-z0-9_-]+`. The directory is shared by every fake process,
  so concurrent sessions need distinct names.

## Pushed conversations

A pushed conversation is one an earlier session left behind. Its turns ran
without an engine, so a `call()` left its round unanswered, as when the user
quit while a tool waited. Opening it restores the history, and resumable
tools such as `ask_user_question` continue.

At startup, `--conversation` takes a script instead of an id; the script is
the only turn of the conversation that opens:

```sh
BITS_FAKE_BACKEND=1 bits --conversation 'load("internal/agent/fake/testdata/questions.star", "ask_user_question"); ask_user_question()'
```

In a session, `push_conversation` takes functions as the turns, in order,
and returns the id to open with `/resume`:

```text
load("internal/agent/fake/testdata/questions.star", "ask_user_question"); say(push_conversation(ask_user_question, title="Pending question"))
load("internal/agent/fake/testdata/questions.star", "ask_user_question"); say(push_conversation(lambda: say("hi"), ask_user_question))
```

- Several turns script other histories, e.g. a pending call then a new user
  turn, which must not resume.
- The title defaults to the first turn, `name()`; a lambda shows as
  `lambda()`, so prefer a `def`.
- A turn is called again when it resumes, so its function and the globals it
  reaches are frozen when pushed.
- A replayed round returns its earlier id instead of pushing again.

## Tests

- `Fake{}` streams instantly; `New()` adds a 10 ms delay for interactive use.
- `Fake.PushConversation(ctx, title, scripts...)` pushes from Go.
- Set `Fake.ContinueDir` to a `t.TempDir()`. A breakpoint stops right after
  its last output, so a test that sees that output acts, then writes the
  continue file.
