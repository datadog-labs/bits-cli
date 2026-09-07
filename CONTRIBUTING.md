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
go test ./...
```

Lint with the pinned golangci-lint configuration:

```sh
scripts/lint.sh
```

## Development workflow

1. Create a branch for your change.
2. Make your change, keeping commits focused.
3. Make sure `go build ./...`, `go test ./...`, and `./scripts/lint.sh` all pass.
4. Open a pull request against `main` with a clear description of the change and how it was tested. External GitHub pull requests run no CI themselves; maintainers mirror the branch to the internal GitLab CI for validation.

## Code of conduct

Be respectful of other contributors and follow standard open-source conduct expectations.

## License

By contributing to Bits CLI, you agree that your contributions will be licensed under the [Apache-2.0 License](LICENSE).
