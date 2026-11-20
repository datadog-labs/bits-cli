#!/bin/sh
#
# install.sh - install Bits on macOS or Linux from a GitHub release:
#
#     curl -fsSL https://github.com/datadog-labs/bits-cli/releases/latest/download/install.sh | sh
#
# It needs curl or wget, tar, and sha256sum or shasum. The release is
# unpacked into ${XDG_DATA_HOME:-~/.local/share}/bits/versions/<version>, and
# its `bits install` makes it current and links ~/.local/bin/bits to it.
# BITS_VERSION installs another release, v0.0.15 or later.

set -eu

# The release this script installs; package.sh sets it in release assets.
release=

releases="${BITS_RELEASES_URL:-https://github.com/datadog-labs/bits-cli/releases}"

say() {
    printf '%s\n' "$*" >&2
}

fail() {
    say "bits install: $*"
    exit 1
}

download() {
    if command -v curl >/dev/null 2>&1; then
        curl --fail --silent --show-error --location --proto-redir '=https' --retry 3 --output "$2" "$1"
    else
        wget --quiet --output-document="$2" "$1"
    fi || fail "could not download $1"
}

sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sum="$(sha256sum "$1")"
    else
        sum="$(shasum -a 256 "$1")"
    fi || fail "could not compute a SHA-256; install sha256sum or shasum"
    printf '%s\n' "${sum%% *}"
}

# Everything runs from main, so a download cut short runs nothing.
main() {
    version="${BITS_VERSION:-$release}"
    [ -n "$version" ] || fail "set BITS_VERSION to the release to install, such as v0.1.0"
    version="v${version#v}"
    printf '%s\n' "$version" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' ||
        fail "$version is not a release version, such as v0.1.0"
    # Older releases have no `bits install`.
    if printf '%s\n' "${version#v}" | awk -F. '{ exit !($1 == 0 && $2 == 0 && $3 < 15) }'; then
        fail "Bits $version predates this installer; install v0.0.15 or later"
    fi

    case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail "$(uname -s) is not supported; on Windows, use install.ps1" ;;
    esac
    case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail "$(uname -m) processors are not supported" ;;
    esac
    # An x86_64 shell under Rosetta still gets the native build.
    if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null)" = 1 ]; then
        arch=arm64
    fi

    case "${XDG_DATA_HOME:-}" in
    /*) root="$XDG_DATA_HOME/bits" ;;
    *) root="$HOME/.local/share/bits" ;;
    esac
    archive="bits_${version#v}_${os}_${arch}.tar.gz"
    dir="$root/versions/$version"

    mkdir -p "$root/versions"
    staging="$(mktemp -d "$root/.staging-XXXXXX")"
    trap 'rm -rf "$staging"' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM

    say "Downloading Bits $version for $os/$arch"
    download "$releases/download/$version/bits_${version#v}_checksums.txt" "$staging/checksums.txt"
    download "$releases/download/$version/$archive" "$staging/$archive"
    expected="$(awk -v name="$archive" '$2 == name || $2 == "*" name { print $1 }' "$staging/checksums.txt")"
    [ -n "$expected" ] || fail "the release has no checksum for $archive"
    actual="$(sha256 "$staging/$archive")"
    [ "$actual" = "$expected" ] || fail "$archive does not match its checksum"

    mkdir "$staging/prefix"
    tar -xzf "$staging/$archive" -C "$staging/prefix"
    : >"$staging/prefix/.install-complete"
    # A complete directory is this version from an earlier or concurrent
    # install, which may be running: never delete it. An incomplete one is a
    # removal that stopped halfway, which moves aside into staging.
    if [ ! -f "$dir/.install-complete" ]; then
        if [ -d "$dir" ]; then
            mv "$dir" "$staging/incomplete"
        fi
        mv "$staging/prefix" "$dir"
        # When a concurrent install creates $dir first, mv moves into it.
        rm -rf "$dir/prefix"
    fi
    "$dir/bin/bits" install
}

main "$@"
