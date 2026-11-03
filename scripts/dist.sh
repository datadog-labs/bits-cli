#!/usr/bin/env bash
#
# dist.sh - build every release platform from scratch with build.sh, then
# package them with package.sh into DIST_DIR. CI runs the two steps on
# separate runners instead.

set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/platforms.sh
source "$SRC_DIR/scripts/platforms.sh"

tag="${*: -1}"
if [[ $# -lt 1 || $# -gt 2 || ($# -eq 2 && "$1" != --sbom) ||
    ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "usage: $0 [--sbom] v<major>.<minor>.<patch>" >&2
    exit 1
fi

build_dir="$(mktemp -d)"
trap 'rm -rf "$build_dir"' EXIT

VERSION="${tag#v}" BUILD_DIR="$build_dir" "$SRC_DIR/scripts/build.sh" "${PLATFORMS[@]}" >/dev/null
BUILD_DIR="$build_dir" "$SRC_DIR/scripts/package.sh" "$@"
