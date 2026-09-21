#!/usr/bin/env bash
#
# lint.sh - install a pinned golangci-lint, lint every module, and check
# LICENSE-3rdparty.csv for drift.

set -euo pipefail

GOLANGCI_LINT_VERSION="v2.13.2"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if command -v golangci-lint >/dev/null 2>&1; then
    GOLANGCI_LINT="$(command -v golangci-lint)"
else
    go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}"
    GOLANGCI_LINT="$(go env GOPATH)/bin/golangci-lint"
fi

cd "$SRC_DIR"
"$GOLANGCI_LINT" run ./...

# tools/licenses is a nested module, so root-module lints never reach it;
# see tools/licenses/main.go's package doc for why it is nested.
cd tools/licenses
"$GOLANGCI_LINT" run --config="$SRC_DIR/.golangci.yml" ./...

# LICENSE-3rdparty.csv is generated from the dependency tree and must not
# drift. The check downloads module sources through its own -modfile, so
# nothing needs warming up here.
cd "$SRC_DIR"
scripts/generate-licenses.sh check

printf 1>&2 "lint: all good.\n"
