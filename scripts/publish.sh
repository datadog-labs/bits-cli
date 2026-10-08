#!/usr/bin/env bash
#
# publish.sh - publish the dist.sh output as a GitHub release of HEAD. The
# assets are uploaded to a draft; publishing it creates the tag.

set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${DIST_DIR:-$SRC_DIR/dist}"

if [[ $# -ne 1 || ! "$1" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "usage: $0 v<major>.<minor>.<patch>" >&2
    exit 1
fi
tag="$1"
version="${tag#v}"
target="$(git -C "$SRC_DIR" rev-parse HEAD)"

cd "$DIST_DIR"
checksums="bits_${version}_checksums.txt"
sha256sum --check --quiet "$checksums"

assets=("$checksums")
while read -r _ archive; do
    assets+=("$archive")
done <"$checksums"

case "$(gh release view "$tag" --json isDraft --jq .isDraft 2>/dev/null || true)" in
    "") ;;
    true) gh release delete "$tag" --yes ;;
    *)
        echo "ERROR: release $tag is already published" >&2
        exit 1
        ;;
esac

gh release create "$tag" --target "$target" --draft --title "bits $version" --generate-notes "${assets[@]}"
gh release edit "$tag" --draft=false
