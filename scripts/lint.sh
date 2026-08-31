#!/usr/bin/env bash
#
# lint.sh - install a pinned golangci-lint and lint the module.

set -e
set -o pipefail

GOLANGCI_LINT_VERSION="v2.13.2"

go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}"

"$(go env GOPATH)/bin/golangci-lint" run ./...

printf 1>&2 "lint: all good.\n"
