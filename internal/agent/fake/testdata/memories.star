"""Injected memory scenarios for BITS_FAKE_BACKEND=1. Start with `BITS_FAKE_BACKEND=1 go run .`.

Run one from the Starlark console, for example:
  load("memories.star", "injected"); injected()

The backend retrieves memories while the model works and adds them after the
step that finishes next, so they can land between reasoning and tool calls.
"""

MEMORIES = [
    {
        "memory_id": "memory-1",
        "title": "Checkout failover runbook",
        "preview": "During a failover, point checkout at the **primary** database pool. The replica lags by several minutes, so reads from it return stale carts. See the [runbook](https://example.com/runbook).",
    },
    {
        "memory_id": "memory-2",
        "title": "Payments oncall",
        "preview": "Page `#payments-oncall` before restarting the ledger service.",
    },
    {"memory_id": "memory-3", "title": "", "preview": "A memory saved without a title."},
]

def memories(items = MEMORIES):
    raw({"type": "memory_context", "memories": items})

def injected(wait = "4s"):
    """Memories that arrive while the model reasons, then a tool round."""
    think("Checking how checkout fails over")
    memories()
    sleep(wait)
    tool("search_logs", {"query": "service:checkout status:error"}, out = "12 results")
    say("Point checkout at the primary pool, then page payments oncall before restarting the ledger.")

def between_tools():
    """Memories that land between two tool calls, splitting their group."""
    tool("search_logs", {"query": "status:error"}, out = "3 results")
    memories(MEMORIES[:1])
    tool("search_logs", {"query": "status:warn"}, out = "1 result")
    say("Done.")
