# Repository guidance

Read [CONTRIBUTING.md](CONTRIBUTING.md) for setup, tests, authentication, and dependencies.

## Keep docs current

- Update user docs in the same PR when changing commands, flags, environment
  variables, settings, skills, permissions, or supported features. Fixes,
  refactors, hidden options, and dev-only env vars need no doc changes.
- Keep the README short and focused on common tasks. Link to detailed guides,
  including `internal/headless/adeep/README.md` for noninteractive use. Most
  changes need no README edit; change it only when a common task changes.
- Check examples against the code. Explain defaults, requirements, and limits,
  including server requirements for experimental features.
- In each PR, link to the doc updates or explain why none are needed.

## Checks

- Run `./scripts/test.sh` to test both Go modules.
- Run `./scripts/lint.sh` for lint and license checks.
- After a dependency change, run `./scripts/generate-licenses.sh`.
- `BITS_FAKE_BACKEND=1 go run .` runs the real engine and TUI offline; see
  [`internal/agent/fake/README.md`](internal/agent/fake/README.md).

## Layout

- `internal/cmd` reads flags and wires packages. Prefer reading new env vars
  there and passing options down.
- `internal/agent` owns conversation state on one actor goroutine (see
  [`Engine`](internal/agent/engine.go)). It stays generic: no tool- or
  UI-specific state.
- `internal/tui` only renders. State lives in the component that owns it, not in
  `model.go`. Views never fetch data.
- `internal/headless` and `internal/tui` share the same public `agent` API.

## Code style

### Simple, not shortcuts

Write the simplest code that fully solves the problem. Prefer fewer mechanisms
to a smaller diff: extend shared code instead of adding a special case, even if
that touches more files, and remove what your change leaves unused. If the right
fix is out of reach, say so rather than working around it.

### Reuse before you write

Look for an existing type, file, event, or style first, then the standard
library, then current dependencies. Add a dependency only when it is widely used
and clearly better.

### Composition, not abstraction

An interface needs a second implementation; a helper needs a second caller. No
mirror types, const aliases, or mapping layers. Define small interfaces where
they are consumed.

### Data dominates

Keep fields orthogonal: each holds one fact, and no field can contradict or be
derived from another. One enum beats a status plus bools; a pointer beats `X`
plus `HasX`. Prefer typed structs to `map[string]any` and tables to long
switches. Enum switches have no `default:`, so the `exhaustive` linter catches
new cases.

### Communicate, don't lock

Don't add a mutex to the engine; send it a command or extend the existing event
stream.

### Handle errors

Never discard an error with `_`. Don't pre-check for a binary or platform; run
it and handle the error.

### Match the server

Never invent backend behavior; delete what you cannot verify against the server.

### Prefer existing files

Put code in the file that owns the concept. A new file is fine for a new
concept; justify it by what it owns, not by length. No `doc.go` or package
READMEs.

### Comments explain WHY, not WHAT

No comment beats one that restates the code. Don't reference a PR, ticket, task,
or inspiring project. Mark an accepted limit with `NOTE:`, not `TODO`.

## Tests

The single rule: **a test must be able to fail for a real reason.**

- Don't test the standard library or dependencies, or read back input built to
  match.
- Test at package boundaries and fake the network, not the unit.
- Use table-driven cases and plain `testing.T`.
- Extend an existing table before writing a new test. Each `_test.go` file
  matches a production file.
- No sleeps or polling. `e2e` names only tests that hit a real backend.
- When a test fails because of an intended change, fix the test.

## Commits and pull requests

- Subject is `pkg: lowercase imperative summary`, such as
  `agent: queue prompts submitted during a turn`. No `fix(...)` prefixes.
- The body says why the change is right: the problem, the fix, the limits.
  Describe the result, not the iterations.
- Every commit builds. Keep commits under about 1000 lines, with renames
  separate from behavior changes. Amend fixes into the commit they fix.
- Fill in [the PR template](.github/PULL_REQUEST_TEMPLATE.md) with short
  paragraphs, not bullets. "How to test it" gives pasteable manual steps,
  usually with `BITS_FAKE_BACKEND=1 go run .`.
