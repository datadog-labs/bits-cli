#!/usr/bin/env bash
#
# test.sh - run the unit tests of every module in the repository.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$SRC_DIR"
go test ./...

# tools/licenses is a nested module: the license detector and its dependency
# tree are kept out of the shipped module (see tools/licenses/main.go's
# package doc for why), so root-module test runs never reach it and it needs
# its own pass.
# These tests read the root module's dependency files, outside this module's
# test-cache inputs. Always rerun them when checking the current inventory.
go -C tools/licenses test -count=1 ./...

printf 1>&2 "test: all good.\n"
