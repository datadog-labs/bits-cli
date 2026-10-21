# Contributing to Bits CLI

Thanks for your interest in contributing to Bits CLI! This document covers the basics for getting a development environment running.

## Prerequisites

- Go 1.27.3 or later

## Getting started

Clone the repository:

```sh
git clone git@github.com:datadog-labs/bits-cli.git
cd bits-cli
```

Build and test:

```sh
go build ./...
./scripts/test.sh
```

`go build ./...` covers only the root module. `./scripts/test.sh` runs the
unit tests of the root module and of the nested `tools/licenses` module,
which `go test ./...` from the repository root does not reach.

Lint with the pinned golangci-lint configuration:

```sh
./scripts/lint.sh
```

`./scripts/lint.sh` lints both modules, checks the shell scripts with
shellcheck when it is installed (CI always does), and verifies that
`LICENSE-3rdparty.csv` matches the dependency tree.

## Authentication for development

Run `bits login` to sign in through your browser. For API-key authentication,
set both `DD_API_KEY` and `DD_APP_KEY`, then select the mode and API site explicitly:

```sh
bits --auth api-key --site https://api.datadoghq.com
```

This uses the keys for the current invocation without changing your saved OAuth
session. Plain `bits` uses OAuth even when those environment variables are set.

### Staging

Configure the staging login origin and callback domain before signing in:

```sh
export BITS_STAGING_SITE=https://login.staging.test
export BITS_STAGING_DOMAIN=staging.test
export BITS_STAGING_CLIENT_ID=staging-test-client
bits login --site "$BITS_STAGING_SITE"
```

Use your environment's actual values. `BITS_STAGING_SITE` and
`BITS_STAGING_DOMAIN` are required; `BITS_STAGING_CLIENT_ID` supplies an optional
default that `bits login --client-id` can override. The login origin must use
HTTPS and belong to the callback domain. API and token requests go to
`api.<domain>`.

Keep the staging configuration set while using the saved session. If it is
missing or names a different domain, Bits reports an error. To switch back to
production, run `bits logout` with the staging configuration still set, unset
the staging variables, and sign in again.

### Integration tests

Tests that contact Datadog are opt-in:

- `BITS_OAUTH_E2E=1` reuses your stored OAuth login.
- `BITS_ASSISTANT_E2E=1` requires `DD_API_KEY`, `DD_APP_KEY`, and an explicit
  `BITS_E2E_SITE` naming the Datadog API site.

For noninteractive runs, see the [ADEEP delivery documentation](internal/headless/adeep/README.md).

## Development workflow

1. Create a branch for your change.
2. Make your change, keeping commits focused.
3. Make sure `./scripts/test.sh` and `./scripts/lint.sh` — the same scripts CI
   runs — both pass.
4. Open a pull request against `main` with a clear description of the change
   and how it was tested. GitHub Actions runs lint, tests, and builds for
   every pull request.

## Continuous integration

`.github/workflows/ci.yml` runs on pull requests (forks included), pushes to
`main`, and queued merges. It is unprivileged (read-only token, no secrets,
SHA-pinned actions) and runs `./scripts/lint.sh`, `./scripts/test.sh`, and
`./scripts/build.sh` for linux, darwin, and windows on amd64 and arm64.
Releases are described in RELEASE.md.

## Third-party licenses

`LICENSE-3rdparty.csv` is generated output: never hand-edit it. It covers every
external Go module whose source is part of the build/test closure of the
shipped root module (its `go.sum` entries with a zip hash), with the SPDX
license identifier and copyright holder of each module.

- After any dependency change, run `./scripts/generate-licenses.sh` and commit
  the regenerated CSV. This includes the weekly Dependabot pull requests.
- The drift check runs as part of `./scripts/lint.sh` and compares the committed
  CSV byte for byte with the freshly generated one.
- If a module cannot be resolved (unknown license, no copyright statement, or
  an underivable upstream origin), the generator fails listing the module.
  Fix it with an entry in `tools/licenses/overrides.json`, which requires a
  one-line `reason` for each entry. Stale override keys and license overrides
  contradicting a confident detection fail the run, so corrections always
  stay deliberate.
- The generator also refuses any copyleft dependency (GPL, AGPL, LGPL, EPL).
  If one is ever deliberate, acknowledge it with `"copyleft": true` and a
  reason in the same overrides file.
- The generator's own dependencies (including the license detector) are
  excluded from the inventory by design: `tools/licenses` is a nested module,
  so its dependency tree never enters the root module's `go.sum`.

## License

By contributing to Bits CLI, you agree that your contributions will be licensed under the [Apache-2.0 License](LICENSE).

## Multi-agent Assistant transport

The regular Assistant endpoint remains the default. Opt into experimental
subagents with `BITS_USE_EXPERIMENTAL_SUBAGENTS=1 bits`, or with
`BITS_USE_EXPERIMENTAL_SUBAGENTS=1 bits run --prompt '…' --delivery adeep`.
Only `1` enables it; unset, empty, or other values keep the regular endpoint.
The setting is not persisted.

With the environment variable enabled, eligible turns use multi-agent run
endpoints with the `cli` profile. The server must support these endpoints and enable
`command-assistant-multi-agent-runs` for the organization. Each turn creates a new
run while keeping the conversation ID; history and conversation management use
the existing APIs. Child answers stay out of the final root answer, and local
tools from different agents can run concurrently, subject to the same permission
checks and a limit of 16 executions.

Turns using `--reasoning`, explicit raw history, or payloads exceeding the bounded
run API's limits use the regular Assistant endpoint, including its tool-result
continuations. This preserves existing image and instruction capabilities. Tests verify that
the shipped tool definitions fit the run API limits. There is no retry through
another endpoint after a run has started.

For a test drive, set `BITS_CUSTOM_HEADERS='test-drive-NAME=1'`. The shared
request builder forwards these headers on start, conversation continuation,
tool-result submission, cancellation, and regular API requests.
