#!/usr/bin/env bash
#
# dist.sh - build the release archives and checksums for a tag into DIST_DIR
# and print their paths.

set -euo pipefail

PLATFORMS=(darwin/amd64 darwin/arm64 linux/amd64 linux/arm64)

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${DIST_DIR:-$SRC_DIR/dist}"

if [[ $# -ne 1 || ! "$1" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "usage: $0 v<major>.<minor>.<patch>" >&2
    exit 1
fi
version="${1#v}"

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

mkdir -p "$DIST_DIR"
rm -f "$DIST_DIR"/bits_*

files=(README.md LICENSE LICENSE-3rdparty.csv)
for platform in "${PLATFORMS[@]}"; do
    goos="${platform%/*}"
    goarch="${platform#*/}"
    name="bits_${version}_${goos}_${goarch}"
    stage="$work_dir/$name"

    binary="$(GOOS="$goos" GOARCH="$goarch" VERSION="$version" BUILD_DIR="$work_dir/build" \
        "$SRC_DIR/scripts/build.sh")"
    mkdir -p "$stage"
    install -m 0755 "$binary" "$stage/bits"
    (cd "$SRC_DIR" && cp "${files[@]}" "$stage/")
    COPYFILE_DISABLE=1 tar -czf "$DIST_DIR/$name.tar.gz" -C "$stage" bits "${files[@]}"
    echo "$DIST_DIR/$name.tar.gz"
done

checksums="bits_${version}_checksums.txt"
if command -v sha256sum >/dev/null; then
    sha256=(sha256sum)
else
    sha256=(shasum -a 256)
fi
(cd "$DIST_DIR" && "${sha256[@]}" bits_"${version}"_*.tar.gz >"$checksums")
echo "$DIST_DIR/$checksums"
