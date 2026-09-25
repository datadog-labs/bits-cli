#!/usr/bin/env bash
set -euo pipefail

usage() {
    echo "Usage: $0 --version VERSION [--dist-dir DIR] [--target TARGET] [--publish]" >&2
    echo "Build Dogbrew tarballs; --publish also downloads ADMS and uploads them." >&2
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
version=""
dist_dir="${repo_root}/dist"
target="${ADMS_TARGET:-ci}"
publish=false

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
        --target)
            [[ $# -ge 2 ]] || { echo "ERROR: --target requires a value" >&2; exit 1; }
            target="$2"
            shift 2
            ;;
        --publish)
            publish=true
            shift
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
    version="$("${script_dir}/version.sh")"
fi
if [[ "${dist_dir}" != /* ]]; then
    dist_dir="${repo_root}/${dist_dir}"
fi
if [[ ! "${version}" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "ERROR: release version must be <major>.<minor>.<patch>; got ${version}" >&2
    exit 1
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
    cp "${source}" "${contents}/${binary}"
    (cd "${contents}" && tar -czf "${archive}" cli.yaml "${binary}")

    [[ -s "${archive}" ]] || {
        echo "ERROR: missing or empty tarball: ${archive}" >&2
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

if [[ "${publish}" != true ]]; then
    exit 0
fi

api="https://cli-mgmt-api.us1.ddbuild.io"
rel="$(curl -fsS "${api}/internal/api/v1/clis/adms/channels/dev/releases/latest?platform=linux-amd64")"
_field() { printf '%s' "${rel}" | grep -o "\"$1\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" | head -1 | sed 's/.*:[[:space:]]*"//;s/"$//'; }
url="$(_field download_url)"
sha="$(_field sha256)"
sha="${sha#sha256:}"
[[ -n "${url}" ]] || { echo "ERROR: no ADMS download URL" >&2; exit 1; }
[[ -n "${sha}" ]] || { echo "ERROR: no ADMS SHA-256 checksum" >&2; exit 1; }
[[ "${sha}" =~ ^[[:xdigit:]]{64}$ ]] || { echo "ERROR: invalid ADMS SHA-256 checksum" >&2; exit 1; }
curl -fsSLo /tmp/adms.tar.gz "${url}"
printf '%s  %s\n' "${sha}" /tmp/adms.tar.gz | sha256sum -c -
tar -xzf /tmp/adms.tar.gz -C /tmp
adms_bin=/tmp/adms-linux-amd64
[[ -f "${adms_bin}" ]] || adms_bin=/tmp/adms
adms_cmd="${work_dir}/adms"
install -m 0755 "${adms_bin}" "${adms_cmd}"

for platform in darwin-arm64 linux-amd64 linux-arm64; do
    "${adms_cmd}" first-party cli upload "${dist_dir}/bits-${platform}.tar.gz" --target="${target}"
done
