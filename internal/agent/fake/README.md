# Fake backend

`BITS_FAKE_BACKEND=1` runs Bits offline against this package, in the TUI or
with `bits run`. It implements `agent.Backend` plus conversation history,
listing, and the current user, so the real engine, tools, transcript, and
rendering run unchanged. Only the network is faked.

## Random turns

A plain prompt streams pseudo-random output seeded by the prompt: thinking,
text, server tool calls, then a Markdown answer and usage. The same prompt
always produces the same content.

## Scripted turns

A script is [Starlark](https://github.com/bazelbuild/starlark) whose
built-ins emit exactly the wire output you ask for. A one-line script follows
`::`:

```text
:: say("# Title\n\n| a | b |\n| --- | --- |\n| 1 | 2 |")
:: think("checking"); r = call("read_file", {"path": "go.mod"}); say("first line: " + r.output.splitlines()[0])
:: a, b = call([("list_files", {"path": "."}), ("grep_files", {"pattern": "TODO"})])
:: g = call("approval_request", {"tool_name": "delete_dashboard", "tool_args": {}}); say("deleted" if g.ok else "kept")
:: tool("search", {"q": "p95"}, out="3 monitors", ns="datadog", detail="**3** monitors match")
:: raw({"type": "widget_def", "title": "p95", "widget_def": {}})
:: say("partial answer"); sleep("1s"); fail(503, "upstream overloaded")
:: kitchen()
:: help()
```

| Built-in | Emits |
| --- | --- |
| `say(text)`, `think(text)` | streamed answer text or reasoning |
| `tool(name, input, out=, err=, ns=, title=, detail=, stream=)` or `tool([(name, input, out), …])` | server tool calls, then their results |
| `call(name, input, stream=)` or `call([(name, input), …])` | one round of client tool calls, run by the real engine; returns results with `ok`, `status`, `title`, `output` |
| `raw(content, results=, id=)` | any other wire content, decoded by the real decoder |
| `fail(status)`, `fail("net")`, `fail("timeout")` | a backend failure after the output so far |
| `sleep("2s")` | a stall |
| `help()` | the built-ins and the declared client tools with their input schemas |
| `kitchen()` | every output type once, read-only |

A longer script is a `fake` fenced block (Shift+Enter or Ctrl+J for a
newline in the TUI). The closing fence is optional, and the lines lose their
shared indentation, so a copied block works as pasted:

````text
```fake
r = call("read_file", {"path": "go.mod"})
if r.ok:
    say("first line: " + r.output.splitlines()[0])
else:
    fail(503)
```
````

Notes:

- A `::` script is exactly one line. Statements may be separated by `;`, but
  `if`/`for` cannot follow `;`: use a fenced block or `a if cond else b`.
- Syntax errors show the offending line with a caret and a hint.
- Tool input is streamed (`tool_call_started` and input deltas) whenever the
  engine asks for it; `stream=False` disables it for one call.
- `approval_request` calls get their `tool_call_id` filled in automatically.
- `call` really runs local tools. Under the default `allow-all` approval mode,
  scripted `write_file`, `edit_file`, and `exec_command` change the workspace.
- Script errors are shown as the answer instead of failing the turn.

## How rounds work

A `call` ends the current `Send`, as a real client tool call pauses the
server stream. When the engine sends the tool results back, the script runs
again from the top: earlier `call`s return their recorded results, output
before them is not re-emitted, and execution stops at the next `call`.
Starlark has no clock, randomness, or I/O here, so every run is
deterministic and nothing stays alive between `Send`s.

Built-ins know the wire protocol, never tool input schemas. Those are owned
by `internal/tools/spec` and listed at runtime by `help()`.
