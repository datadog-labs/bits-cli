"""Scenarios for exercising prompts with BITS_FAKE_BACKEND=1.

A prompt is a tool request waiting on the user: the approval panel or a tool's
interactive UI (today the questionnaire). The approval docks above the
composer, which stays visible but inert; the questionnaire replaces the
composer and dims the transcript behind it.

Start the TUI in the default permission mode (Ask for Approval):
  BITS_FAKE_BACKEND=1 go run .

Allowing a scenario writes under tmp/approval-demo/ only. Enter one prompt:
  load("fakedata/approval.star", "short_request"); short_request()
  load("fakedata/approval.star", "long_command"); long_command()
  load("fakedata/approval.star", "queued"); queued()
  load("fakedata/approval.star", "write_then_edit"); write_then_edit()
  load("fakedata/approval.star", "question_over_history"); question_over_history()
  load("fakedata/approval.star", "question_then_approval"); question_then_approval()
  load("fakedata/approval.star", "after_decision"); after_decision()

The rules every scenario checks:
  - The keyboard goes to the prompt. PgUp/PgDn page the prompt's body,
    Shift+PgUp/PgDn page the transcript, Esc dismisses (an approval denies),
    Ctrl+X stops the tool round, Ctrl+O toggles the transcript's tool blocks.
  - The pointer goes where it points. The wheel scrolls the prompt over the
    prompt and the transcript anywhere else. A click the prompt has no use for
    (anywhere on the approval) selects text, like on the transcript.

Only exec_command has an approval body that can outgrow the panel; file writes
and edits show a short fixed prompt.

  short_request     Small panel. Left/Right/Tab move the highlight; Enter and
                    Esc decide. Wheel over the panel does nothing visible.
  long_command      The body is taller than its window ("lines X-Y of N").
                    Scrolling never moves the highlight. The panel takes at
                    most half the rows above the composer (at least 15), so
                    the chat keeps the rest; on a short terminal it drops to
                    its compact form without a scroll hint, or the resize hint.
  queued            Header says "3 waiting"; the first is a long command. Scroll
                    its body, then decide: the next panel starts at the top with
                    the first action highlighted. Scroll the chat up before
                    deciding: it stays where you left it.
  write_then_edit   Two approvals in a row (write, then edit). The second starts
                    with the first action highlighted even if you moved it.
  question_over_history
                    The questionnaire replaces the composer; the transcript
                    dims but still scrolls and selects under the pointer.
  question_then_approval
                    A question and an approval wait at once. Whichever arrives
                    first shows and is not replaced by the other; the approval
                    header says "2 waiting". Answer it and the other shows,
                    fresh, in its own place. Ctrl+X instead stops both.
  after_decision    Scroll up while the panel is open, then Allow. The turn
                    continues; submitting a new message jumps to the bottom.

Also try: resizing until the prompt no longer fits (below 36 columns, or too
few rows above the composer; a tall draft takes rows too). The resize hint
replaces the screen, input is ignored, and Ctrl+C still quits.
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
