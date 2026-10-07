#!/usr/bin/env bash
set -euo pipefail

usage() {
    echo "Usage: $0 v<major>.<minor>.<patch>" >&2
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

printf '%s\n' "${tag#v}"
