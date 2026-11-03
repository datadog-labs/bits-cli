#!/usr/bin/env bash
#
# build.sh - build bits and its libexec helpers for GOOS/GOARCH into an
# install prefix, BUILD_DIR/<os>-<arch>, and print the prefix:
#
#     bin/bits
#     libexec/bits/<tool>/   one directory per libexec/<tool>/build.sh
#
# A non-empty VERSION (X.Y.Z) produces a stripped release build.
# EXTRA_LDFLAGS is appended to the linker flags, for local test builds only.
# BUILD_LIBEXEC=0 builds bits alone, for Go-only and cross-compiled builds.

set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
BUILD_DIR="${BUILD_DIR:-$SRC_DIR/build}"
VERSION="${VERSION:-}"

build_args=(-trimpath)
ldflags=""
if [[ -n "$VERSION" ]]; then
    if [[ ! "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
        echo "ERROR: VERSION must match <major>.<minor>.<patch>; got '$VERSION'" >&2
        exit 1
    fi
    build_args+=(-buildvcs=false)
    ldflags="-s -w -buildid= -X github.com/datadog-labs/bits-cli/internal/version.releaseVersion=$VERSION"
fi
# go build keeps only the last -ldflags, so both sources share one flag.
ldflags="$ldflags ${EXTRA_LDFLAGS:-}"
[[ -n "${ldflags// /}" ]] && build_args+=(-ldflags "$ldflags")

mkdir -p "$BUILD_DIR"
BUILD_DIR="$(cd "$BUILD_DIR" && pwd)"
prefix="$BUILD_DIR/$GOOS-$GOARCH"
executable="$prefix/bin/bits"
[[ "$GOOS" == windows ]] && executable="$executable.exe"

printf 'Building %s with %s\n' "$executable" "$(go version)" >&2
cd "$SRC_DIR"
CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build "${build_args[@]}" -o "$executable" .

rm -rf "$prefix/libexec"
if [[ "${BUILD_LIBEXEC:-1}" != 0 ]]; then
    shopt -s nullglob
    for tool_build in "$SRC_DIR"/libexec/*/build.sh; do
        tool="$(basename "$(dirname "$tool_build")")"
        printf 'Building libexec/bits/%s\n' "$tool" >&2
        GOOS="$GOOS" GOARCH="$GOARCH" VERSION="$VERSION" BUILD_DIR="$BUILD_DIR" \
            "$tool_build" "$prefix/libexec/bits/$tool" >&2
    done
fi

echo "$prefix"
