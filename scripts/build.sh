#!/usr/bin/env bash
#
# build.sh - build the bits binary for GOOS/GOARCH into BUILD_DIR and print
# its path. A non-empty VERSION (X.Y.Z) produces a stripped release build.

set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
BUILD_DIR="${BUILD_DIR:-$SRC_DIR/build}"
VERSION="${VERSION:-}"

build_args=(-trimpath)
if [[ -n "$VERSION" ]]; then
    if [[ ! "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
        echo "ERROR: VERSION must match <major>.<minor>.<patch>; got '$VERSION'" >&2
        exit 1
    fi
    build_args+=(
        -buildvcs=false
        -ldflags "-s -w -buildid= -X github.com/datadog-labs/bits-cli/internal/cmd.releaseVersion=$VERSION"
    )
fi

output="$BUILD_DIR/bits-$GOOS-$GOARCH"
[[ "$GOOS" == windows ]] && output="$output.exe"

mkdir -p "$BUILD_DIR"
printf 'Building %s with %s\n' "$output" "$(go version)" >&2
cd "$SRC_DIR"
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build "${build_args[@]}" -o "$output" .

echo "$output"
