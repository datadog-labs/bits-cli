#!/usr/bin/env bash
#
# package.sh - package the install prefixes that build.sh built in BUILD_DIR
# for every release platform into archives, optional SPDX SBOMs, and
# checksums in DIST_DIR, and print their paths. Each archive is an install
# prefix:
#
#     bin/bits
#     libexec/bits/<tool>/   helpers built for the platform, if any
#     share/doc/bits/        README.md, LICENSE, LICENSE-3rdparty.csv

set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_DIR="${BUILD_DIR:-$SRC_DIR/build}"
DIST_DIR="${DIST_DIR:-$SRC_DIR/dist}"
# shellcheck source=scripts/platforms.sh
source "$SRC_DIR/scripts/platforms.sh"

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
for platform in "${PLATFORMS[@]}"; do
    if [[ ! -d "$BUILD_DIR/${platform/\//-}/bin" ]]; then
        echo "ERROR: no $platform prefix in $BUILD_DIR; run VERSION=$version scripts/build.sh $platform" >&2
        exit 1
    fi
done

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

mkdir -p "$DIST_DIR"
DIST_DIR="$(cd "$DIST_DIR" && pwd)"
rm -f "$DIST_DIR"/bits_*

docs=(README.md LICENSE LICENSE-3rdparty.csv)
artifacts=()
for platform in "${PLATFORMS[@]}"; do
    goos="${platform%/*}"
    name="bits_${version}_${goos}_${platform#*/}"
    stage="$work_dir/$name"

    cp -R "$BUILD_DIR/${platform/\//-}" "$stage"
    mkdir -p "$stage/share/doc/bits"
    (cd "$SRC_DIR" && install -m 0644 "${docs[@]}" "$stage/share/doc/bits/")
    entries=(bin share)
    [[ -d "$stage/libexec" ]] && entries+=(libexec)
    if [[ "$goos" == windows ]]; then
        archive="$name.zip"
        (cd "$stage" && zip -qr "$DIST_DIR/$archive" "${entries[@]}")
    else
        archive="$name.tar.gz"
        COPYFILE_DISABLE=1 tar -czf "$DIST_DIR/$archive" -C "$stage" "${entries[@]}"
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
