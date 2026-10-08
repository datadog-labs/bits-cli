#!/usr/bin/env bash
#
# lint.sh - install a pinned golangci-lint, lint every module, check the
# shell scripts with shellcheck, and check LICENSE-3rdparty.csv for drift.
#
# The shell script checks are skipped when shellcheck is not installed,
# unless --require-shellcheck is passed, as CI does.

set -euo pipefail

GOLANGCI_LINT_VERSION="v2.14.0"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

require_shellcheck=false
case "${1:-}" in
    "") ;;
    --require-shellcheck) require_shellcheck=true ;;
    *)
        printf 'usage: %s [--require-shellcheck]\n' "$0" >&2
        exit 1
        ;;
esac
if [[ "$require_shellcheck" == true ]] && ! command -v shellcheck >/dev/null 2>&1; then
    printf 'lint: shellcheck is required but not installed.\n' >&2
    exit 1
fi

if command -v golangci-lint >/dev/null 2>&1; then
    GOLANGCI_LINT="$(command -v golangci-lint)"
    GOLANGCI_LINT_INSTALLED_VERSION="$("$GOLANGCI_LINT" version --short 2>/dev/null || true)"
    if [[ -z "$GOLANGCI_LINT_INSTALLED_VERSION" ]]; then
        printf 'lint: could not determine the version of pre-installed golangci-lint at %s.\n' "$GOLANGCI_LINT" >&2
        printf 'lint: expected golangci-lint %s; install that version or remove the pre-installed binary.\n' "$GOLANGCI_LINT_VERSION" >&2
        exit 1
    fi
    if [[ "$GOLANGCI_LINT_INSTALLED_VERSION" != "${GOLANGCI_LINT_VERSION#v}" ]]; then
        printf 'lint: pre-installed golangci-lint at %s is version %s; expected %s.\n' \
            "$GOLANGCI_LINT" "$GOLANGCI_LINT_INSTALLED_VERSION" "$GOLANGCI_LINT_VERSION" >&2
        printf 'lint: install golangci-lint %s or remove the pre-installed binary.\n' "$GOLANGCI_LINT_VERSION" >&2
        exit 1
    fi
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

if command -v shellcheck >/dev/null 2>&1; then
    git ls-files -z '*.sh' | xargs -0 shellcheck
else
    printf 'lint: shellcheck is not installed; skipping shell script checks.\n' >&2
fi

printf 1>&2 "lint: all good.\n"
