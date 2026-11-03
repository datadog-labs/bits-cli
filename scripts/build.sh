#!/usr/bin/env bash
#
# build.sh - build bits and its libexec helpers for each os/arch target
# (default: the go env target) into an install prefix, BUILD_DIR/<os>-<arch>,
# and print the prefixes:
#
#     bin/bits
#     libexec/bits/<tool>/   one directory per libexec/<tool>/build.sh
#
#     ./scripts/build.sh                            # the host
#     ./scripts/build.sh darwin/amd64 darwin/arm64
#
# A non-empty VERSION (X.Y.Z) produces a stripped release build.
# EXTRA_LDFLAGS is appended to the linker flags, for local test builds only.
# BUILD_LIBEXEC=0 builds bits alone, for Go-only and cross-compiled builds.

set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_DIR="${BUILD_DIR:-$SRC_DIR/build}"
VERSION="${VERSION:-}"

targets=("$@")
[[ ${#targets[@]} -gt 0 ]] || targets=("$(go env GOOS)/$(go env GOARCH)")
for target in "${targets[@]}"; do
    if [[ ! "$target" =~ ^[a-z0-9]+/[a-z0-9]+$ ]]; then
        echo "usage: $0 [<os>/<arch>...]" >&2
        exit 1
    fi
done

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
cd "$SRC_DIR"
shopt -s nullglob

for target in "${targets[@]}"; do
    goos="${target%/*}"
    goarch="${target#*/}"
    prefix="$BUILD_DIR/$goos-$goarch"
    executable="$prefix/bin/bits"
    [[ "$goos" == windows ]] && executable="$executable.exe"

    printf 'Building %s with %s\n' "$executable" "$(go version)" >&2
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build "${build_args[@]}" -o "$executable" .

    rm -rf "$prefix/libexec"
    if [[ "${BUILD_LIBEXEC:-1}" != 0 ]]; then
        for tool_build in "$SRC_DIR"/libexec/*/build.sh; do
            tool="$(basename "$(dirname "$tool_build")")"
            printf 'Building %s/libexec/bits/%s\n' "$prefix" "$tool" >&2
            GOOS="$goos" GOARCH="$goarch" VERSION="$VERSION" BUILD_DIR="$BUILD_DIR" \
                "$tool_build" "$prefix/libexec/bits/$tool" >&2
        done
    fi

    echo "$prefix"
done
