def _suggest(prompts):
    tool("suggest_follow_ups", {"prompts": prompts}, out='{"accepted":true}')

def suggest():
    say("Here is what I can do.")
    _suggest(["Summarize this dashboard", "Check my monitors", "Show recent incidents"])

def after_approval():
    g = call("approval_request", {"tool_name": "mute_monitor", "tool_args": {"id": 42}})
    say("Muted." if g.ok else "Left it alone.")
    _suggest(["Unmute it in an hour", "List muted monitors"])

def superseded():
    _suggest(["An earlier suggestion"])
    say("Thinking again.")
    _suggest(["The later suggestion", "   ", "Another later one"])

def long():
    _suggest(["Compare p99 latency of team:payments services this week against last week and explain regressions", "`kubectl` and \x1b[31mescapes\x1b[0m stay plain text", "/status is sent as a message, not run"])
