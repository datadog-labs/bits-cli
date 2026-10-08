#!/usr/bin/env bash
#
# publish.sh - publish the dist.sh output as a GitHub release of HEAD. The
# assets are uploaded to a draft; publishing it creates the tag. The notes
# render .github/release-notes.md, followed by the generated changelog.

set -euo pipefail

REPO="datadog-labs/bits-cli"
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
archives=()
while read -r _ asset; do
    assets+=("$asset")
    [[ "$asset" == *.tar.gz ]] && archives+=("$asset")
done <"$checksums"

downloads=()
for archive in "${archives[@]}"; do
    platform="${archive#"bits_${version}_"}"
    platform="${platform%.tar.gz}"
    downloads+=("# ${platform/_//}" "gh release download $tag --repo $REPO --pattern $archive")
done

# Render the placeholders literally: bash 5.2 otherwise expands & in the
# replacement to the matched text.
shopt -u patsub_replacement 2>/dev/null || true
notes="$(<"$SRC_DIR/.github/release-notes.md")"
download_lines="$(printf '%s\n' "${downloads[@]}")"
notes="${notes//'{{downloads}}'/$download_lines}"
notes="${notes//'{{version}}'/$version}"
notes="${notes//'{{tag}}'/$tag}"
notes="${notes//'{{repo}}'/$REPO}"

case "$(gh release view "$tag" --json isDraft --jq .isDraft 2>/dev/null || true)" in
    "") ;;
    true) gh release delete "$tag" --yes ;;
    *)
        echo "ERROR: release $tag is already published" >&2
        exit 1
        ;;
esac

gh release create "$tag" --target "$target" --draft --title "bits $version" \
    --notes "$notes" --generate-notes "${assets[@]}"
gh release edit "$tag" --draft=false
