# `bits.delivery.adeep` JSONL delivery

Versioned stream consumed by the ADEEP evaluation pipeline. The delivery is a library API (`adeep.New(writer)` driven through `Start`/`Consume`/`Finish`); the stacked command PR wires it as `bits run --delivery adeep`, which writes one JSON object per line to stdout with diagnostics on stderr. The schema discriminator is `bits.delivery.adeep` and this document describes version `1`. Bump `Version` in `jsonl.go` and update this document whenever a record gains a field or changes meaning; consumers must reject unknown versions.

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

`tool.call` and `tool.result` correlate by `tool.id`. A call whose input never arrives is still emitted at `Finish`, with the exact (possibly empty) arguments and no invented result. The CLI does not negotiate the `stream_tool_call_input` capability, so arguments always come from the complete tool call; if that capability is ever enabled, partial streamed input must first be plumbed through `agent.ToolBlock` or an interrupted call would flush with empty arguments.

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
- The engine cancels the turn on the consumer error (`TurnOutcomeConsumerFailed`); `bits run` maps it to exit `1`.
