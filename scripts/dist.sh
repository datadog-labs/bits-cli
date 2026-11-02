#!/usr/bin/env bash
#
# dist.sh - build release archives, optional SPDX SBOMs, and checksums into DIST_DIR
# and print their paths.

set -euo pipefail

PLATFORMS=(darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64)

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${DIST_DIR:-$SRC_DIR/dist}"

sbom=false
if [[ "${1:-}" == --sbom ]]; then
    sbom=true
    shift
fi

if [[ $# -ne 1 || ! "$1" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "usage: $0 [--sbom] v<major>.<minor>.<patch>" >&2
    exit 1
fi
version="${1#v}"

if [[ "$sbom" == true ]] && ! command -v syft >/dev/null 2>&1; then
    echo "ERROR: syft is required to generate release SBOMs (https://github.com/anchore/syft)" >&2
    exit 1
fi

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

mkdir -p "$DIST_DIR"
rm -f "$DIST_DIR"/bits_*

files=(README.md LICENSE LICENSE-3rdparty.csv)
artifacts=()
for platform in "${PLATFORMS[@]}"; do
    goos="${platform%/*}"
    goarch="${platform#*/}"
    name="bits_${version}_${goos}_${goarch}"
    stage="$work_dir/$name"

    binary="$(GOOS="$goos" GOARCH="$goarch" VERSION="$version" BUILD_DIR="$work_dir/build" \
        "$SRC_DIR/scripts/build.sh")"
    mkdir -p "$stage"
    executable=bits
    [[ "$goos" == windows ]] && executable=bits.exe
    install -m 0755 "$binary" "$stage/$executable"
    (cd "$SRC_DIR" && cp "${files[@]}" "$stage/")
    if [[ "$goos" == windows ]]; then
        archive="$name.zip"
        (cd "$stage" && zip -q "$DIST_DIR/$archive" "$executable" "${files[@]}")
    else
        archive="$name.tar.gz"
        COPYFILE_DISABLE=1 tar -czf "$DIST_DIR/$archive" -C "$stage" "$executable" "${files[@]}"
    fi
    artifacts+=("$archive")
    echo "$DIST_DIR/$archive"
    if [[ "$sbom" == true ]]; then
        syft "$DIST_DIR/$archive" --output "spdx-json=$DIST_DIR/$archive.sbom.json"
        artifacts+=("$archive.sbom.json")
        echo "$DIST_DIR/$archive.sbom.json"
    fi
done

checksums="bits_${version}_checksums.txt"
if command -v sha256sum >/dev/null; then
    sha256=(sha256sum)
else
    sha256=(shasum -a 256)
fi
(cd "$DIST_DIR" && "${sha256[@]}" "${artifacts[@]}" >"$checksums")
echo "$DIST_DIR/$checksums"
