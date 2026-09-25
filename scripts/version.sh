#!/usr/bin/env bash
set -euo pipefail

tag="${CI_COMMIT_TAG:-}"
if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "ERROR: CI_COMMIT_TAG must match v<major>.<minor>.<patch>; got ${tag:-<empty>}" >&2
    exit 1
fi

printf '%s\n' "${tag#v}"
