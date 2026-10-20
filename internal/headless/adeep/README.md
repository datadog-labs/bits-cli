# `bits.delivery.adeep` JSONL delivery

Versioned stream consumed by the ADEEP evaluation pipeline. The delivery is a library API (`adeep.New(writer)` driven through `Start`/`Consume`/`Finish`), exposed by `bits run --delivery adeep`, which writes one JSON object per line to stdout with diagnostics on stderr. The schema discriminator is `bits.delivery.adeep` and this document describes version `1`. Bump `Version` in `jsonl.go` and update this document whenever a record gains a field or changes meaning; consumers must reject unknown versions.

## Running from the CLI

Sign in with `bits login`, then run one assistant turn:

```sh
bits run --prompt "Summarize this conversation" --conversation YOUR_CONVERSATION_ID --delivery adeep
```

Omit `--conversation` to start a new conversation. `--prompt` is required and
literal; positional and stdin prompts are not supported. The command requires
a working login and never opens an interactive login flow. For API-key
authentication, see [Contributing](../../../CONTRIBUTING.md#authentication-for-development).

Actions requiring approval are denied by default (`--permissions deny`). Pass
`--permissions skip-permissions` to allow them without prompting. The final
assistant text is in `run.finished.response`.

| Exit status | Meaning |
| --- | --- |
| `0` | Turn completed. |
| `1` | Startup, runtime, or delivery failure. |
| `2` | Invalid command or flags. |
| `3` | At least one approval was denied, even if the assistant subsequently finished. |

## Envelope

Every record carries:

| Field | Type | Meaning |
|---|---|---|
| `schema` | string | Always `bits.delivery.adeep`. |
| `version` | int | Contract version, currently `1`. |
| `type` | string | Record type below. |
| `round` | int | Omitted when `0`. 1-based backend send the record belongs to. |

## Record types

| Type | Round | Fields |
|---|---|---|
| `run.started` | 0 | `requested_model?`, `started_at?` (RFC3339) |
| `round.started` | N | — |
| `round.finished` | N | — |
| `conversation.updated` | N | `conversation_id` |
| `tool.call` | N | `tool{id, name, side: client\|server, arguments}` — `arguments` is the exact wire string, including malformed JSON |
| `tool.result` | N | `tool{id, status, result}` — `result` is the exact wire string |
| `usage` | N | `usage{tokens_used, max_tokens, input_tokens?, output_tokens?}` |
| `run.finished` | 0 | `outcome`, `conversation_id?`, `response?`, `rounds`, `ended_at?` (RFC3339), `error{message}?` |

`tool.call` and `tool.result` correlate by `tool.id`. A call whose input never arrives is still emitted at `Finish`, with the exact (possibly empty) arguments and no invented result. Tool definitions opt into provisional input events with `stream_input: true`. Arguments come from the authoritative final tool call; provisional input stays in the transcript preview and is not emitted as complete arguments.

## Tool status

| Status | Meaning |
|---|---|
| `success` | The tool ran and returned a result. |
| `error` | The tool or its handler failed. |
| `denied` | The approval flow refused the call. |
| `cancelled` | The call was ended by a user denial of another call, a stop, or cancellation — not a tool failure. |

## Outcomes and rounds

- `outcome` is `completed`, `approval_denied`, `failed`, `canceled`, or `timed_out`. A completed turn that denied at least one gate reports `approval_denied`.
- `rounds` counts **backend sends**, including rounds whose content a stopped-round drain discarded, so `round.started`/`round.finished` pairs may exist without any content records between them. A send canceled before producing events is not counted.
- `response` is the final assistant Markdown, drawn **only** from text blocks observed during this run; prior or restored history is never included.

## Failure and lifecycle semantics

- Exactly one `run.finished` ends a healthy stream; `Finish` is rejected afterwards.
- `Consume` before `Start` or after `run.finished` is rejected, as is a second `Start`.
- The first writer failure is latched: every later write, including the terminal record, fails, so a recovering writer can never emit a well-formed but incomplete stream.
- `headless.Run` cancels the turn on the consumer error and reports it as a `failed` outcome; `bits run` maps it to exit `1`.
