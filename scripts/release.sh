#!/usr/bin/env bash
#
# release.sh - trigger the release workflow on main for a tag. With --check,
# verify instead that the tag is the next patch, minor, or major release
# after the existing tags; the release workflow runs this first.

set -euo pipefail

SEMVER='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'

die() {
    echo "ERROR: $*" >&2
    exit 1
}

check=false
if [[ "${1:-}" == --check ]]; then
    check=true
    shift
fi
[[ $# -eq 1 && "$1" =~ $SEMVER ]] || die "usage: $0 [--check] v<major>.<minor>.<patch>"
tag="$1"

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [[ "$check" == false ]]; then
    gh workflow run release.yml --repo datadog-labs/bits-cli --ref main --field tag="$tag"
    echo "Triggered the $tag release; follow it with: gh run watch --repo datadog-labs/bits-cli"
    exit 0
fi

# A shallow clone may lack the tags the version check depends on.
[[ "$(git rev-parse --is-shallow-repository)" == false ]] ||
    die "the version check needs a full clone with all tags"
if git rev-parse --quiet --verify "refs/tags/$tag" >/dev/null; then
    die "tag $tag already exists"
fi

tags="$(git tag --list 'v*' --sort=-version:refname)"
previous=""
while read -r candidate; do
    if [[ "$candidate" =~ $SEMVER ]]; then
        previous="$candidate"
        break
    fi
done <<<"$tags"

if [[ -n "$previous" ]]; then
    IFS=. read -r major minor patch <<<"${previous#v}"
    case "${tag#v}" in
        "$major.$minor.$((patch + 1))" | "$major.$((minor + 1)).0" | "$((major + 1)).0.0") ;;
        *) die "$tag is not the next patch, minor, or major release after $previous" ;;
    esac
fi
echo "$tag is the next release after ${previous:-no previous release}"
