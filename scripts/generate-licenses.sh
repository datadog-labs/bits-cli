#!/usr/bin/env bash
#
# generate-licenses.sh - regenerate LICENSE-3rdparty.csv from the dependency
# tree.
#
# With no argument (or "generate") the CSV is rewritten. "check" verifies the
# committed CSV matches the dependency tree and is used by scripts/lint.sh.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$SRC_DIR"

if [ $# -eq 0 ]; then
    set -- generate
fi

# tools/licenses is a nested module; see its main.go package doc for why.
go -C tools/licenses run . "$@"
