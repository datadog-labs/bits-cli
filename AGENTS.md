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

## Checks

- Run `./scripts/test.sh` to test both Go modules.
- Run `./scripts/lint.sh` for lint and license checks.
