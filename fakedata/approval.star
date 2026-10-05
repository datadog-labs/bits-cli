"""Prompt scenarios for BITS_FAKE_BACKEND=1. Start with `BITS_FAKE_BACKEND=1 go run .`.

Run one from the Starlark console, for example:
  load("fakedata/approval.star", "long_command"); long_command()

Scenarios cover short and scrollable approvals, queued prompts, questionnaires,
and scrolling after a decision. Allowing writes only under tmp/approval-demo/.
PgUp/PgDn scroll the prompt; Shift+PgUp/PgDn scroll chat. Esc denies or
dismisses, Ctrl+X stops the tool round, and Ctrl+O toggles tool blocks.
"""

def pad(n):
    # Starlark has no %02d.
    return ("0" if n < 10 else "") + str(n)

def filler(lines, label = "history"):
    # One short list item per row: enough rows to scroll, few words to stream.
    say("\n".join(["- %s %s" % (label, pad(i + 1)) for i in range(lines)]))

def numbered(count, prefix):
    return "\n".join(["%s %s" % (prefix, pad(i + 1)) for i in range(count)])

def long_script(steps):
    # Mix short lines with one that must wrap on a narrow terminal.
    lines = ["echo step " + pad(i + 1) for i in range(steps)]
    lines[3] = "echo " + " ".join(["a-long-line-that-wraps-on-narrow-terminals"] * 4)
    return "\n".join(lines)

def short_request():
    filler(30)
    r = call("write_file", {"path": "tmp/approval-demo/short.txt", "content": "hello\n"})
    say("write_file finished: " + r.status)

def long_command():
    filler(30)
    cmd = long_script(40)
    r = call("exec_command", {"cmd": cmd})
    say("exec_command finished: " + r.status)

def queued():
    filler(30)
    results = call([
        ("exec_command", {"cmd": long_script(30)}),
        ("write_file", {"path": "tmp/approval-demo/two.txt", "content": numbered(5, "two") + "\n"}),
        ("exec_command", {"cmd": "echo three"}),
    ])
    say("batch finished: " + ", ".join([r.status for r in results]))

def write_then_edit():
    filler(20)
    path = "tmp/approval-demo/diff.txt"
    w = call("write_file", {"path": path, "content": numbered(40, "row") + "\n"})
    if not w.ok:
        say("Skipping the edit because the write was not allowed.")
        return
    e = call("edit_file", {"path": path, "edits": [
        {"old_text": "row 05", "new_text": "row 05 (changed)"},
        {"old_text": "row 20", "new_text": "row 20 (changed)"},
    ]})
    say("edit_file finished: " + e.status)

def question_over_history():
    filler(30)
    load_questions()

QUESTIONS = {"questions": [
    {"question": "Which region should we investigate?", "options": [
        {"label": "US (Recommended)", "description": "Start with the US production environment."},
        {"label": "EU", "description": "Investigate the EU production environment."},
    ]},
    {"question": "Which service should we focus on?", "options": [
        {"label": "API", "description": "Inspect API latency and error rates."},
        {"label": "Worker", "description": "Inspect background jobs and queue delays."},
    ]},
]}

def load_questions():
    r = call("ask_user_question", QUESTIONS)
    say("Questions finished: " + r.status)

def question_then_approval():
    filler(30)
    results = call([
        ("ask_user_question", QUESTIONS),
        ("write_file", {"path": "tmp/approval-demo/mixed.txt", "content": numbered(5, "mixed") + "\n"}),
    ])
    say("batch finished: " + ", ".join([r.status for r in results]))

def after_decision():
    filler(30)
    r = call("write_file", {"path": "tmp/approval-demo/after.txt", "content": numbered(25, "after") + "\n"})
    say("The decision was: " + r.status + ".")
    filler(5, "after")
