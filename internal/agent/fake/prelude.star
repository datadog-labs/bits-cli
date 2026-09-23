# Loaded before every script. Only kitchen() names tools; it is exercised by
# the fake's engine tests against the real tools, so schema drift fails there.

true, false, null = True, False, None

def kitchen():
    """Every output type once. Read-only: never writes the workspace."""
    think("Planning a tour of every output type.")
    say("# Kitchen sink\n\nSome **bold**, `code`, and a [link](https://docs.datadoghq.com).\n")
    tool("search_logs", {"query": "status:error"}, out="12 results")
    tool("search_logs", {"query": "status:warn"}, out="index unavailable", err=True)
    tool("search", {"q": "p95"}, out="3 monitors", ns="datadog", title="Datadog search", detail="**3** monitors match")
    tool("Skill", {"skill_name": "triage"}, out="loaded")
    tool([("get_metrics", {"query": "avg:trace.http.request.duration{*}"}, "ok"), ("list_monitors", {}, "2 monitors")])
    call([("list_files", {"path": "."}), ("read_file", {"path": "go.mod"}), ("grep_files", {"pattern": "TODO"})])
    call("exec_command", {"cmd": "echo hello"})
    call("approval_request", {"tool_name": "create_monitor", "tool_args": {"name": "p95"}, "approval_message": "Create monitor p95?"})
    call([("does_not_exist", {}), ("read_file", "{not json")])
    raw({"type": "widget_def", "title": "p95 latency", "widget_def": {}})
    raw({"type": "dashboard", "title": "Checkout", "widgets": []})
    raw({"type": "background_task_update", "content": "Investigation 50% done", "event_type": "progress", "task_id": "task-1"})
    raw({"type": "thinking", "content": "", "redacted": True})
    raw({"type": "future_content_type"})
    say("## Summary\n\n| service | p95 |\n| --- | --- |\n| web | 42ms |\n\n```go\nfmt.Println(\"done\")\n```\n")
