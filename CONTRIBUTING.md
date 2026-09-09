# Contributing to Bits CLI

Thanks for your interest in contributing to Bits CLI! This document covers the basics for getting a development environment running.

## Prerequisites

- Go 1.27 or later

## Getting started

Clone the repository:

```sh
git clone git@github.com:DataDog/bits-cli.git
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
4. Open a pull request against `main` with a clear description of the change and how it was tested. External GitHub pull requests run no CI themselves; maintainers mirror the branch to the internal GitLab CI for validation.

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
- The generator's own dependencies (including the license detector) are
  excluded from the inventory by design: `tools/licenses` is a nested module,
  so its dependency tree never enters the root module's `go.sum`.

## License

By contributing to Bits CLI, you agree that your contributions will be licensed under the [Apache-2.0 License](LICENSE).
