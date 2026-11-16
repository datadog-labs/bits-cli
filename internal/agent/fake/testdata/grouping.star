# Tool grouping QA: a closed group shows only its running calls and the
# newest thinking, which labels the work as the model's intent. Edits stay
# open and commands stand alone. Run from the repository root:
#
#   load("grouping.star", "investigation"); investigation()
#
# It writes, edits, then removes qa-grouping.txt in the workspace. On
# Windows, call investigation(windows=True). delay is how long each round of
# server calls runs.

def investigation(delay="3s", windows=False):
    think("The user asks why checkout latency spiked this morning. I'll start with the error logs.")
    tool("search_datadog_logs", {"query": "service:checkout status:error"}, out="1,204 errors since 07:02", title="Checkout error logs", delay=delay)
    think("Errors spike at 07:02. Next I'll check monitors and deploy events around that time.")
    tool([
        ("search_datadog_monitors", {"query": "service:checkout"}, "checkout p99 triggered at 07:04"),
        ("search_datadog_events", {"query": "deploy"}, "deploy v412 at 07:00"),
    ], delay=delay)
    think("Deploy v412 touched the retry helper. Let me read the code.")
    call([("list_files", {"path": "."}), ("read_file", {"path": "go.mod"}), ("grep_files", {"pattern": "func main"})])
    think("I'll draft the fix in a scratch file.")
    path = "qa-grouping.txt"
    call("write_file", {"path": path, "content": "sleep 50ms\n"})
    call("edit_file", {"path": path, "edits": [{"old_text": "sleep 50ms\n", "new_text": "sleep backoff.Next()\n"}]})
    think("Now I'll remove the scratch file.")
    call("exec_command", {"cmd": ("Remove-Item " if windows else "rm ") + path})
    say("Latency jumped after deploy v412 changed the retry helper to a fixed 50ms sleep.")
