#!/usr/bin/env bash
#
# package.sh - build the portable release archives from pre-built binaries.
#
# Each archive carries the platform binary plus a cli.yaml manifest
# (name/team/description/version) that a package index needs to register
# the CLI. Purely offline: no downloads, no uploads.

set -euo pipefail

usage() {
    echo "Usage: $0 --version VERSION [--dist-dir DIR]" >&2
    echo "Creates bits-<platform>.tar.gz release archives under --dist-dir." >&2
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
version=""
dist_dir="${repo_root}/dist"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --version)
            [[ $# -ge 2 ]] || { echo "ERROR: --version requires a value" >&2; exit 1; }
            version="$2"
            shift 2
            ;;
        --dist-dir)
            [[ $# -ge 2 ]] || { echo "ERROR: --dist-dir requires a value" >&2; exit 1; }
            dist_dir="$2"
            shift 2
            ;;
        --help|-h)
            usage
            exit 0
            ;;
        *)
            echo "ERROR: unknown argument: $1" >&2
            usage
            exit 1
            ;;
    esac
done

if [[ -z "${version}" ]]; then
    echo "ERROR: --version is required" >&2
    exit 1
fi
if [[ ! "${version}" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "ERROR: release version must be <major>.<minor>.<patch>; got ${version}" >&2
    exit 1
fi
if [[ "${dist_dir}" != /* ]]; then
    dist_dir="${repo_root}/${dist_dir}"
fi
[[ -s "${repo_root}/cli.yaml" ]] || { echo "ERROR: missing cli.yaml" >&2; exit 1; }

work_dir="$(mktemp -d)"
trap 'rm -rf "${work_dir}"' EXIT
mkdir -p "${dist_dir}"

for platform in darwin-arm64 linux-amd64 linux-arm64; do
    binary="bits-${platform}"
    source="${dist_dir}/${binary}"
    archive="${dist_dir}/${binary}.tar.gz"
    contents="${work_dir}/${platform}"

    [[ -s "${source}" ]] || {
        echo "ERROR: missing or empty binary: ${source}" >&2
        exit 1
    }

    mkdir -p "${contents}"
    sed "s/^version:.*/version: ${version}/" "${repo_root}/cli.yaml" > "${contents}/cli.yaml"
    grep -q "^version: ${version}$" "${contents}/cli.yaml" || {
        echo "ERROR: generated cli.yaml has the wrong version" >&2
        exit 1
    }
    # install (not cp) also restores the executable bit that artifact
    # upload/download normalizes to 0644.
    install -m 0755 "${source}" "${contents}/${binary}"
    (cd "${contents}" && tar -czf "${archive}" cli.yaml "${binary}")

    [[ -s "${archive}" ]] || {
        echo "ERROR: missing or empty archive: ${archive}" >&2
        exit 1
    }
    entries="$(tar -tzf "${archive}")"
    expected=$'cli.yaml\n'"${binary}"
    [[ "${entries}" == "${expected}" ]] || {
        echo "ERROR: unexpected contents in ${archive}:" >&2
        printf '%s\n' "${entries}" >&2
        exit 1
    }
    printf '%s\n' "${archive}"
done
