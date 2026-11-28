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
while read -r _ artifact; do
    assets+=("$artifact")
done <"$checksums"

# Anyone who can write to the repository can edit a draft. Publish it only if
# it holds exactly the assets this job built and attested. Mark it latest only
# after checking the published, immutable release again. Users can still
# download published releases by tag, and writers can change latest.
same_assets() {
    local expected actual
    expected="$(sha256sum "${assets[@]}" | sort)" || return 1
    actual="$(gh release view "$tag" --json assets --jq '.assets[] | "\(.digest | ltrimstr("sha256:"))  \(.name)"' | sort)" || return 1
    diff <(printf '%s\n' "$expected") <(printf '%s\n' "$actual")
}

# A retry of an older publish job must not promote its release after a
# newer release has been published. The publish checkout includes every tag.
while read -r candidate; do
    if [[ "$candidate" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
        newest="$(printf '%s\n%s\n' "$tag" "$candidate" | sort -V | tail -n 1)"
        if [[ "$newest" != "$tag" ]]; then
            echo "ERROR: release $tag is superseded by $candidate" >&2
            exit 1
        fi
        break
    fi
done < <(git -C "$SRC_DIR" tag --list 'v*' --sort=-version:refname)

state="$(gh release view "$tag" --json isDraft --jq .isDraft 2>/dev/null || true)"
case "$state" in
    "" | true)
        # GitHub ignores --target if the tag already exists.
        if git -C "$SRC_DIR" show-ref --verify --quiet "refs/tags/$tag"; then
            echo "ERROR: tag $tag already exists without a published release" >&2
            exit 1
        fi
        if [[ "$state" == true ]]; then gh release delete "$tag" --yes; fi
        gh release create "$tag" --target "$target" --draft --title "bits $version" --generate-notes "${assets[@]}"
        if ! same_assets; then
            echo "ERROR: the draft of $tag does not hold the assets this job built" >&2
            exit 1
        fi
        gh release edit "$tag" --target "$target" --draft=false --prerelease=false --latest=false
        ;;
    false) ;;
    *) echo "ERROR: unexpected release state: $state" >&2; exit 1 ;;
esac

# A failed lookup or promotion can be retried with the same workflow artifacts.
# Never adopt a release of a different commit, or assets changed in its draft.
if [[ "$(gh release view "$tag" --json isImmutable --jq .isImmutable)" != true ]]; then
    echo "ERROR: release $tag is not immutable" >&2
    exit 1
fi
if [[ "$(gh api "repos/{owner}/{repo}/commits/$tag" --jq .sha)" != "$target" ]]; then
    echo "ERROR: release $tag does not point at $target" >&2
    exit 1
fi
if ! same_assets; then
    echo "ERROR: release $tag does not hold the assets this job built; it is published but not latest" >&2
    exit 1
fi
gh release edit "$tag" --prerelease=false --latest
