#!/usr/bin/env bash
#
# build.sh - build the bits CLI binary.
#
# Output goes to BUILD_DIR as bits-<goos>-<goarch>[.exe]; the final path is
# echoed to stdout so callers can consume it.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRC_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
BUILD_DIR="${BUILD_DIR:-$SRC_DIR/build}"

OUTPUT="$BUILD_DIR/bits-$GOOS-$GOARCH"
if [ "$GOOS" = "windows" ]; then
    OUTPUT="$OUTPUT.exe"
fi

mkdir -p "$BUILD_DIR"

printf 1>&2 "Building %s with %s\n" "$OUTPUT" "$(go version)"

cd "$SRC_DIR"
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath -o "$OUTPUT" .

echo "$OUTPUT"
