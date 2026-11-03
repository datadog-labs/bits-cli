#!/usr/bin/env bash
#
# build.sh - build the ddviz macOS viewer for GOOS/GOARCH into OUT_DIR:
#
#     ddviz
#     assets/   loaded from next to the executable
#
# Needs the Xcode command line tools. See ../README.md for the contract.

set -euo pipefail

TOOL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
BUILD_DIR="${BUILD_DIR:-$(cd "$TOOL_DIR/../.." && pwd)/build}"

if [[ $# -ne 1 ]]; then
    echo "usage: $0 OUT_DIR" >&2
    exit 1
fi
out="$1"

[[ "$GOOS" == darwin ]] || exit 0
if [[ "$(uname -s)" != Darwin ]]; then
    echo "ERROR: ddviz builds on macOS only" >&2
    exit 1
fi
case "$GOARCH" in
    arm64) arch=arm64 ;;
    amd64) arch=x86_64 ;;
    *)
        echo "ERROR: no ddviz build for darwin/$GOARCH" >&2
        exit 1
        ;;
esac

swift_args=(
    --package-path "$TOOL_DIR"
    --scratch-path "$BUILD_DIR/.cache/ddviz/$GOARCH"
    --configuration release
    --triple "$arch-apple-macosx13.0"
)
xcrun swift build "${swift_args[@]}" >&2
bin_dir="$(xcrun swift build "${swift_args[@]}" --show-bin-path)"

mkdir -p "$out/assets"
install -m 0755 "$bin_dir/ddviz" "$out/ddviz"
install -m 0644 "$TOOL_DIR"/assets/* "$out/assets/"
