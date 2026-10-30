# Repository guidance

Read [CONTRIBUTING.md](CONTRIBUTING.md) for setup, tests, authentication, and dependencies.

## Keep docs current

- Update user docs in the same PR when changing commands, flags, environment
  variables, settings, skills, permissions, or supported features.
- Keep the README short and focused on common tasks. Link to detailed guides,
  including `internal/headless/adeep/README.md` for noninteractive use.
- Check examples against the code. Explain defaults, requirements, and limits,
  including server requirements for experimental features.
- In each PR, link to the doc updates or explain why none are needed.

## Code and checks

- Comments should explain current behavior and why it exists, not how the change evolved.
- Test intended behavior. Do not keep tests just for bugs introduced and removed
  while building the same change.
- Run `./scripts/test.sh` to test both Go modules.
- Run `./scripts/lint.sh` before committing. Sign commits before pushing.
- Never commit superpowers documents.

## Pull requests

- Open new PRs as drafts. Never change a ready PR back to draft.
- Start descriptions with a short TLDR. Fully explain what the final diff does,
  how it works, checks run, and known limits. Leave out implementation history.
- Include a lines-of-code table with feature groups as rows, not filenames.
  Use production code, tests, and totals as columns. Show additions and deletions
  and a totals row. Count docs and other non-code changes separately.
- Never add screenshot placeholders or request another review from someone who approved.
- You may push to open, reviewed PRs. Always push after rebasing. Do not keep
  rebasing just because the remote base moves.

## Reviews and comments

- Ask before posting GitHub comments. Start each with `[Codex]`. For review
  findings, follow it with a short bold sentence explaining the impact.
- Resolve review threads once addressed.
- For a thorough review, use three Astra subagents with narrow scopes and also
  review the full change yourself. If several issues point to a flawed approach,
  consider a broader rewrite, even outside the PR, when it would leave cleaner code.
