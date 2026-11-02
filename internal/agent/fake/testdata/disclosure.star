# Transcript disclosure QA: each function shows one renderer's compact and
# full views. Run from the repository root, then click a ▶ or press ctrl+o:
#
#   load("internal/agent/fake/testdata/disclosure.star", "all"); all()
#
# On Windows, call all(windows=True) to use PowerShell commands.
# changes() writes, edits, then removes qa-disclosure.txt in the workspace.

def reasoning():
    think("Plan:\n1. check error logs\n2. check p95 latency\n3. check monitors")
    say("Thinking expands to its text.")

def server_tools():
    tool("search_logs", {"query": "status:error"}, out="web  ERROR timeout\nweb  ERROR timeout\napi  ERROR 502\napi  ERROR 502")
    tool("list_monitors", {}, out="")
    tool("query_metrics", {"query": "p95:trace{*}"}, out="query failed\n  at parse: unexpected token", err=True)
    tool("Skill", {"skill_name": "triage"}, out="loaded")

def inspections():
    call([("list_files", {"path": "."}), ("read_file", {"path": "go.mod"}), ("read_file", {"path": "missing.txt"}), ("grep_files", {"pattern": "TODO"})])
    say("A lone inspection expands like any tool:")
    call("read_file", {"path": "go.mod"})

def commands(windows=False):
    heredoc = "cat <<'EOF'\n" + "\n".join(["row %d" % i for i in range(1, 15)]) + "\nEOF"
    if windows:
        heredoc = "@'\n" + "\n".join(["row %d" % i for i in range(1, 15)]) + "\n'@"
    call([
        ("exec_command", {"cmd": "echo hello"}),
        ("exec_command", {"cmd": "1..40" if windows else "seq 1 40"}),
        ("exec_command", {"cmd": "exit 0" if windows else "true"}),
        ("exec_command", {"cmd": "Get-Item does-not-exist -ErrorAction Stop" if windows else "ls does-not-exist"}),
        ("exec_command", {"cmd": heredoc}),
    ])

def changes():
    path = "qa-disclosure.txt"
    lines = ["line %d" % i for i in range(1, 31)]
    call("write_file", {"path": path, "content": "\n".join(lines) + "\n"})
    call("edit_file", {"path": path, "edits": [{"old_text": "line 3\n", "new_text": "line 3 (edited)\n"}]})
    block = "\n".join(lines[9:29]) + "\n"
    call("edit_file", {"path": path, "edits": [{"old_text": block, "new_text": block.upper()}]})
    call("exec_command", {"cmd": "rm " + path})

def streaming_change():
    """Stops while the write streams, so its diff can be expanded mid-flight.
    Continue with: touch "${TMPDIR:-/tmp}/bits-fake/continue/disclosure"."""
    path = "qa-disclosure.txt"
    content = "\n".join(["line %d" % i for i in range(1, 31)]) + "\n"
    call("write_file", {"path": path, "content": content}, break_input="disclosure", at="line 25")
    call("exec_command", {"cmd": "rm " + path})

def all(windows=False):
    reasoning()
    server_tools()
    inspections()
    commands(windows=windows)
    changes()
