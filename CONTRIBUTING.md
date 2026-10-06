# Contributing to Bits CLI

Thanks for your interest in contributing to Bits CLI! This document covers the basics for getting a development environment running.

## Prerequisites

- Go 1.27 or later

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

`./scripts/lint.sh` lints both modules and also verifies that
`LICENSE-3rdparty.csv` matches the dependency tree.

## Development workflow

1. Create a branch for your change.
2. Make your change, keeping commits focused.
3. Make sure `./scripts/test.sh` and `./scripts/lint.sh` — the same scripts CI
   runs — both pass.
4. Open a pull request against `main` with a clear description of the change
   and how it was tested. GitHub Actions runs lint, tests, and builds for
   pull requests; for external fork PRs the runs may require maintainer
   approval under the repository policy, but maintainers no longer need to
   mirror branches to an internal CI.

## Continuous integration

GitHub Actions (`.github/workflows/ci.yml`) runs for pull requests
(forks included, though external runs may require maintainer approval
under the repository policy), pushes to `main`, queued merges, and every
`v`-prefixed tag:

- **Lint** runs `./scripts/lint.sh`: the pinned golangci-lint over both
  modules plus the `LICENSE-3rdparty.csv` drift check.
- **Test** runs `./scripts/test.sh` on Linux.
- **Build** cross-builds the six supported targets (linux/darwin/windows on
  amd64/arm64) with `./scripts/build.sh` and uploads each binary as a
  workflow artifact.
- On a strict stable tag (`v<major>.<minor>.<patch>`, no leading zeroes),
  the three Dogbrew platform tarballs are additionally packaged as
  workflow artifacts. Nothing is ever published from Actions.

The workflow is deliberately unprivileged — read-only permissions, no
secrets, SHA-pinned official actions — so fork pull requests get the same
checks as internal ones without any trust escalation. Pre-release tags
(for example `v1.2.3-rc1`) build ordinary development binaries only; see
RELEASE.md for the release flow.

The internal GitLab pipeline (`.gitlab-ci.yml`) no longer runs for branches
or pull requests. It only keeps a stable-tag release pipeline that feeds
the manual internal Dogbrew publisher (see RELEASE.md). Benchmark
collection and Datadog reporting are deferred for now.

## Third-party licenses

`LICENSE-3rdparty.csv` is generated output: never hand-edit it. It covers every
external Go module whose source is part of the build/test closure of the
shipped root module (its `go.sum` entries with a zip hash), with the SPDX
license identifier and copyright holder of each module.

- After any dependency change, run `./scripts/generate-licenses.sh` and commit
  the regenerated CSV.
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
