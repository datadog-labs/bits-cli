"""Background task scenarios for BITS_FAKE_BACKEND=1. Start with `BITS_FAKE_BACKEND=1 go run .`.

Run one from the Starlark console, for example:
  load("tasks.star", "investigation"); investigation()

A task runs apart from the turn: the tray under the composer follows it until
its final update. Updates end with the current step in italics, as an
investigation's do.
"""

STEPS = ["Analyzing errors", "Tracing failure", "Inspecting code", "Verifying failure paths"]

def updates(service):
    out = ["I'm launching an **investigation** into %s and will share updates here." % service]
    for step in STEPS:
        out.append("### Findings\n- `%s` returns 500 for ~64%% of `GET /ads` calls\n\n_%s_" % (service, step))
    out.append("### Root Cause\n`GET /ads` in `%s` fails when the parameters call times out." % service)
    return out

def investigation(every = "3s"):
    """One approved investigation, as the server starts it."""
    gate = call("approval_request", {
        "tool_name": "startBackgroundTask",
        "tool_args": {"task": {"agent_id": "investigation", "task_name": "Investigate ad-server error rate on GET /ads"}},
        "approval_message": "Start a Bits Investigation?",
    })
    if not gate.ok:
        say("Not started.")
        return
    task("Investigate ad-server error rate on GET /ads", updates("ad-server"), every = every)
    say("I started an investigation; its findings will show here.")

def several(n = 5, every = "4s"):
    """More tasks than the tray shows at once."""
    for i in range(n):
        task("Investigate service-%d errors" % (i + 1), updates("service-%d" % (i + 1)), every = every)
    say("Started %d investigations." % n)

def failed(every = "2s"):
    """A task whose final update reports a failure."""
    task("Investigate checkout latency", updates("checkout")[:2], every = every, status = "failed")
    say("Started.")
