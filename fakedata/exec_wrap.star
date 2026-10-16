"""exec_command wrapping scenarios for BITS_FAKE_BACKEND=1. Start with `BITS_FAKE_BACKEND=1 go run .`.

Run everything from the Starlark console:
  load("fakedata/exec_wrap.star", "all"); all()

or one group / one case:
  load("fakedata/exec_wrap.star", "realistic"); realistic()
  load("fakedata/exec_wrap.star", "corners"); corners()
  load("fakedata/exec_wrap.star", "hyphens"); hyphens()

What to check:
- Collapsed: each command is one header line (plus at most 9 │ rows for
  multi-line commands), cut with "…".
- Expanded (Ctrl+O, or click ▶): no "…" inside a command, nothing lost at
  the right edge; the first line wraps under the command's first column,
  later lines wrap behind │ with soft continuations indented by 2 more,
  and source indentation is kept on every row.
- Resize down to ~20 columns and back while expanded: text rewraps into a
  narrower column, never truncated, │ markers stay.

Every command is read-only. go/git/python3/rg may be missing on a machine; the
call then fails, which still exercises the error header. In manual mode each
call waits on the approval panel, which shows the "awaiting approval" suffix.
"""

def words(word, n):
    return " ".join([word] * n)

def run(cmd, **kwargs):
    args = {"cmd": cmd}
    args.update(kwargs)
    return call("exec_command", args)

# ---------------------------------------------------------------------------
# Realistic agent-style commands.

def rg_search():
    # ripgrep with globs, an alternation regex and a pipe.
    run("rg -n --hidden --no-heading -g '*.go' -g '!vendor/**' -g '!**/*_test.go' -e 'renderExecInvocation|hangWrap|lipgloss\\.Wrap|ansi\\.Truncate' internal/tui | sort -t: -k1,1 -k2,2n | head -n 40", timeout_ms = 20000)

def go_chain():
    # A typical long && chain; takes a few seconds, so the running state shows.
    run("cd internal/tui/chat && go vet ./... && go test -count=1 -run 'TestExecCommand|TestRender' ./... 2>&1 | tail -n 20 && cd - >/dev/null && git status --short && git diff --stat -- internal/tui/chat", timeout_ms = 120000)

def git_history():
    # git log | awk | xargs git show, with a format string full of % and -.
    run("git log --oneline --no-merges -n 5 -- internal/tui/chat | awk '{print $1}' | xargs -I{} git show --stat --format='--- %h %an (%ar): %s' {} | head -n 60")

def find_sizes():
    # find -print0 | xargs -0 with a fallback; workdir shows in the header.
    run("find . -type f -name '*.go' -not -path './vendor/*' -not -path './.git/*' -print0 | xargs -0 wc -l | sort -rn | head -n 15 || echo 'find failed' >&2", workdir = "internal")

def shell_loop():
    # set -euo pipefail, a for loop and a conditional, all on one line.
    run("set -euo pipefail; for pkg in internal/tui/chat internal/tui/components internal/tui/diffrender; do n=$(ls \"$pkg\"/*.go 2>/dev/null | wc -l | tr -d ' '); if [ \"$n\" -gt 0 ]; then printf '%-32s %3s go files\\n' \"$pkg\" \"$n\"; else echo \"skip $pkg\"; fi; done", shell = "bash")

def python_inline():
    # python3 -c one-liner with nested quotes.
    run("python3 -c \"import json, pathlib, collections; c = collections.Counter(p.suffix or '<none>' for p in pathlib.Path('internal').rglob('*') if p.is_file()); print(json.dumps(dict(c.most_common(8)), indent=2, sort_keys=True))\"")

def python_heredoc():
    # Indented heredoc lines that wrap: continuation indent is fixed, not code-aligned.
    run("""python3 - <<'PY'
import pathlib, re

pattern = re.compile(r"func (render[A-Z]\\w*|hangWrap|wrapSummarySpans)\\(")
hits = []
for path in sorted(pathlib.Path("internal/tui/chat").glob("*.go")):
    if path.name.endswith("_test.go"):
        continue
    for number, line in enumerate(path.read_text().splitlines(), start=1):
        if (match := pattern.search(line)) is not None:
            hits.append((path.as_posix(), number, match.group(1)))
for path, number, name in hits[:20]:
    print(f"{path}:{number}: {name}")
print(f"{len(hits)} renderer functions found across {len({h[0] for h in hits})} files in internal/tui/chat")
PY""")

def realistic():
    rg_search()
    go_chain()
    git_history()
    find_sizes()
    shell_loop()
    python_inline()
    python_heredoc()

# ---------------------------------------------------------------------------
# Corner cases found while reviewing the wrap.

def header_metadata():
    # Long command + long workdir + timeout: " in …" and " · timeout …" follow
    # the wrapped command; the " · " separators may be orphaned at a row end.
    run("echo " + words("metadata-follows-the-command", 5), workdir = "internal/tui/chat/../components/../chat", timeout_ms = 45000)

def exit_suffix():
    # Non-zero exit: " · exit N" after a wrapped command.
    run("ls " + " ".join(["does-not-exist-%d" % i for i in range(1, 12)]))

def timed_out():
    # " · timed out" after a wrapped command.
    run("sleep 3 && echo " + words("never-printed-because-the-command-times-out", 3), timeout_ms = 1000)

def hard_break():
    # One token with no breakpoints: hard-wrapped mid-token.
    run("echo " + "x" * 300)

def long_path():
    # Paths break after "/" (shellBreakpoints).
    run("ls -la /usr/local/share/" + "/".join(["very-long-directory-name-%d" % i for i in range(1, 8)]) + " 2>/dev/null || true")

def hyphens():
    # Known ambiguity: a break after "-" inside a token looks like a space.
    # Compare "--format=%h-%an-%s" and "two-weeks-ago" across row ends.
    run("git log -n 3 --format=%h-%an-%s --since=two-weeks-ago --date=format-local:%Y-%m-%d-%H-%M --no-merges -- internal/tui/chat")

def heredoc_long_line():
    # Many rows (compact middle-clamps) with one long line behind │.
    rows = ["row %d" % i for i in range(1, 15)]
    rows[5] = "row 6 " + words("heredoc-content-that-wraps", 6)
    run("cat <<'EOF'\n" + "\n".join(rows) + "\nEOF")

def tabs_and_blanks():
    # Tab-indented and blank lines inside a heredoc keep their layout.
    run("cat <<'EOF'\nall:\n\tgo build ./... && go vet ./... && go test -count=1 -race ./internal/tui/... ./internal/agent/...\n\n\n\t\t@echo " + words("tab-indented-recipe-line", 4) + "\nEOF")

def unicode():
    # Wide graphemes (CJK, emoji, combining marks) must wrap on cell width.
    run("echo '" + words("日本語のコマンド", 6) + " 🚀🔥✨ " + words("café-naïve-résumé", 4) + "'")

def shell_quoting():
    # Nested quotes, $(…), backticks and escaped characters wrapping mid-way.
    run("printf '%s\\n' \"$(git rev-parse --abbrev-ref HEAD)\" `date -u +%Y-%m-%dT%H:%M:%SZ` 'single \"quoted\" text with spaces' \"double 'quoted' text with \\$HOME and \\\"escapes\\\"\" | sed -e 's/^/  > /' -e 's/$/ </'")

def indentation():
    # Review repro: at ~32 columns the indented print used to lose its
    # indentation behind an empty │ row. Indentation now repeats on every row.
    run("python3 - <<'PY'\nif True:\n        print('abcdefghij')\n        print('" + words("indented-continuation", 4) + "')\nPY")

def operators():
    # Review repro: at ~20 columns "&&" used to overflow the row and be cut.
    run("go test ./... && go vet ./... && git diff --stat")

def short():
    # Control: expanding a short command changes nothing.
    run("echo short")

def corners():
    header_metadata()
    exit_suffix()
    timed_out()
    hard_break()
    long_path()
    hyphens()
    heredoc_long_line()
    tabs_and_blanks()
    unicode()
    shell_quoting()
    indentation()
    operators()
    short()

# ---------------------------------------------------------------------------

def batch():
    # Several commands in one round: queued approvals, adjacent tool rows.
    call([
        ("exec_command", {"cmd": "rg -n -g '*.go' -e 'hangWrap|renderExecInvocationFull' internal/tui/chat | head -n 20"}),
        ("exec_command", {"cmd": "echo first\necho " + words("second-line-wraps-behind-the-branch", 4) + "\necho third"}),
        ("exec_command", {"cmd": "echo short"}),
    ])

def all():
    realistic()
    corners()
    batch()
    say("Press Ctrl+O to expand every command, then resize the terminal down to ~20 columns and back.")
