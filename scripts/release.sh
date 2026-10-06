#!/usr/bin/env bash
#
# release.sh - validate, create, and push the next Bits release tag.

set -euo pipefail

usage() {
    echo "Usage: $0 v<major>.<minor>.<patch>" >&2
    echo "Creates and pushes an annotated release tag at HEAD." >&2
}

if [[ $# -eq 1 && ( "$1" == "--help" || "$1" == "-h" ) ]]; then
    usage
    exit 0
fi
if [[ $# -ne 1 ]]; then
    usage
    exit 1
fi

tag="$1"
if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "ERROR: release tag must match v<major>.<minor>.<patch>; got ${tag}" >&2
    exit 1
fi
target_major=$((10#${BASH_REMATCH[1]}))
target_minor=$((10#${BASH_REMATCH[2]}))
target_patch=$((10#${BASH_REMATCH[3]}))

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
cd "${repo_root}"

git fetch origin --tags

if git rev-parse -q --verify "refs/tags/${tag}" >/dev/null; then
    echo "ERROR: tag already exists: ${tag}" >&2
    exit 1
fi

previous=""
while IFS= read -r candidate; do
    if [[ "$candidate" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
        previous="$candidate"
        break
    fi
done < <(git tag --list 'v*' --sort=-version:refname)

if [[ -z "$previous" ]]; then
    echo "ERROR: no existing release tag found" >&2
    exit 1
fi

if [[ ! "$previous" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "ERROR: invalid existing release tag: ${previous}" >&2
    exit 1
fi
previous_major=$((10#${BASH_REMATCH[1]}))
previous_minor=$((10#${BASH_REMATCH[2]}))
previous_patch=$((10#${BASH_REMATCH[3]}))

if (( target_major == previous_major && target_minor == previous_minor && target_patch == previous_patch + 1 )); then
    bump="patch"
elif (( target_major == previous_major && target_minor == previous_minor + 1 && target_patch == 0 )); then
    bump="minor"
elif (( target_major == previous_major + 1 && target_minor == 0 && target_patch == 0 )); then
    bump="major"
else
    echo "ERROR: ${tag} is not the next patch, minor, or major bump after ${previous}" >&2
    exit 1
fi

git tag -a "$tag" -m "Release ${tag}"
git push origin "$tag"
printf 'Released %s (%s bump from %s)\n' "$tag" "$bump" "$previous"
