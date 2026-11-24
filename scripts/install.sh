#!/bin/sh
#
# install.sh - install the latest release of Bits on macOS or Linux:
#
#     curl -fsSL https://raw.githubusercontent.com/datadog-labs/bits-cli/HEAD/scripts/install.sh | sh
#
# It needs curl or wget, tar, and sha256sum or shasum. The release is
# unpacked into ${XDG_DATA_HOME:-~/.local/share}/bits/versions/<version>, and
# the installer makes it current and links ~/.local/bin/bits to it.
# BITS_VERSION installs another release, v0.0.15 or later.

# Definitions are read before main, so a truncated download runs no install.

releases="${BITS_RELEASES_URL:-https://github.com/datadog-labs/bits-cli/releases}"
# Redirects keep to the scheme of the releases, https unless overridden.
scheme="${releases%%://*}"

say() {
    printf '%s\n' "$*" >&2
}

fail() {
    say "bits install: $*"
    exit 1
}

# wget can report redirects it followed even when it cannot restrict their
# schemes. Reject any downgrade before consuming the downloaded bytes.
check_redirects() {
    [ "$scheme" = https ] || return 0
    printf '%s\n' "$1" | awk '
        tolower($1) == "location:" {
            url = tolower($2)
            if (url ~ /^[a-z][a-z0-9+.-]*:/ && url !~ /^https:/) exit 1
        }' || fail 'refusing an insecure redirect'
}

download() {
    if command -v curl >/dev/null 2>&1; then
        curl --fail --silent --show-error --location --proto-redir "=$scheme" --retry 3 --output "$2" "$1" || fail "could not download $1"
    else
        headers="$(wget --quiet --server-response --output-document="$2" "$1" 2>&1)" || fail "could not download $1: $headers"
        check_redirects "$headers"
    fi
}

# latest prints the version releases/latest redirects to, the way bits update
# finds it: the tag page of the latest release.
latest() {
    if command -v curl >/dev/null 2>&1; then
        url="$(curl --fail --silent --show-error --location --proto-redir "=$scheme" --retry 3 \
            --head --output /dev/null --write-out '%{url_effective}' "$releases/latest")"
    else
        # wget prints each response's headers; the last Location is the tag page.
        headers="$(wget --spider --server-response "$releases/latest" 2>&1)" || fail "could not find the latest release: $headers"
        check_redirects "$headers"
        url="$(printf '%s\n' "$headers" | sed -n 's/^ *Location: *\([^ ]*\).*/\1/p' | tr -d '\r' | tail -n 1)"
    fi || fail "could not find the latest release at $releases/latest"
    case "$url" in
    */releases/tag/v*) printf '%s\n' "${url##*/}" ;;
    *) fail "could not find the latest release at $releases/latest" ;;
    esac
}

sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sum="$(sha256sum <"$1")"
    else
        sum="$(shasum -a 256 <"$1")"
    fi || fail "could not compute a SHA-256; install sha256sum or shasum"
    printf '%s\n' "${sum%% *}"
}

stable() {
    case "$1" in *'
'*) return 1 ;; esac
    printf '%s\n' "$1" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
}

# Compare decimal components as strings, without awk's floating-point rounding.
older() {
    awk -v a="${1#v}" -v b="${2#v}" 'BEGIN {
        split(a, x, "."); split(b, y, ".")
        for (i = 1; i <= 3; i++) {
            if (length(x[i]) != length(y[i])) exit !(length(x[i]) < length(y[i]))
            if ("x" x[i] != "x" y[i]) exit !("x" x[i] < "x" y[i])
        }
        exit 1
    }'
}

replace_link() {
    # Both variants rename the link itself over current, without following a
    # current that points at a directory. Staging is on the same filesystem.
    if mv -Tf "$1" "$2" 2>/dev/null; then return; fi
    mv -fh "$1" "$2"
}

active_version() {
    if [ -L "$root/current" ]; then
        target="$(readlink "$root/current")"
        printf '%s\n' "${target##*/}"
    elif [ -e "$root/current" ]; then
        fail "$root/current is not an installation link"
    fi
}

clean_up() {
    # A reinstall cannot tell which older release is the rollback copy.
    # NOTE: sessions spanning two upgrades can lose their helpers, as with brew cleanup.
    if [ "$previous" != "$version" ]; then
        for old in "$root"/versions/*; do
            [ -d "$old" ] && [ ! -L "$old" ] || continue
            old_version="${old##*/}"
            stable "$old_version" || continue
            [ "$old_version" != "$previous" ] || continue
            older "$old_version" "$version" || continue
            # Rename before deletion, so a concurrent install only sees complete versions.
            trash="$(mktemp -d "$root/.staging-XXXXXX")" || return 1
            mv "$old" "$trash/$old_version" && rm -rf "$trash" || return 1
        done
    fi
    # Do not sweep staging that may belong to a concurrent download.
    find "$root" -mindepth 1 -maxdepth 1 -type d -name '.staging-*' -mtime +0 -exec rm -rf {} +
}

set_up_command() {
    bin="$HOME/.local/bin"
    link="$bin/bits"
    command_target="$root/current/bin/bits"
    created=false
    if [ -L "$link" ]; then
        existing="$(readlink "$link")"
        case "$existing" in
        "$command_target" | */bits/current/bin/bits) ;;
        *) fail "$link already exists and the installer did not create it; remove it and install again" ;;
        esac
        if [ "$existing" != "$command_target" ]; then
            # XDG_DATA_HOME may be on another filesystem. Stage beside the
            # command link too, so replacing a moved root's link is atomic.
            command_staging="$(mktemp -d "$bin/.bits-XXXXXX")"
            ln -s "$command_target" "$command_staging/command"
            replace_link "$command_staging/command" "$link"
            created=true
        fi
    elif [ -e "$link" ]; then
        fail "$link already exists and the installer did not create it; remove it and install again"
    else
        mkdir -p "$bin"
        # A concurrent installer may create the same link first.
        ln -s "$command_target" "$link" 2>/dev/null ||
            [ "$(readlink "$link")" = "$command_target" ] || fail "could not link $link"
        created=true
    fi
    first="$(command -v bits || true)"
    if [ "$first" = "$link" ]; then
        if [ "$created" = true ]; then say 'Run bits to get started.'; fi
    elif [ -n "$first" ]; then
        say "$first comes first on your PATH, so run $link to use this one."
    else
        advice=''
        # These are commands printed for the user's shell to expand later.
        # shellcheck disable=SC2016,SC2088
        case "${SHELL:-}" in
        */zsh | zsh) advice='echo '\''export PATH="$HOME/.local/bin:$PATH"'\'' >> "${ZDOTDIR:-$HOME}/.zshrc"' ;;
        */bash | bash)
            if [ "$os" = darwin ]; then rc='~/.bash_profile'; else rc='~/.bashrc'; fi
            advice="echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> $rc"
            ;;
        */fish | fish) advice='fish_add_path ~/.local/bin' ;;
        esac
        if [ -n "$advice" ]; then
            say "$bin is not on your PATH. Add it with:"
            say ""
            say "  $advice"
            say ""
            say "Then open a new terminal and run bits."
        else
            say "Add $bin to your PATH, then open a new terminal and run bits."
        fi
    fi
}

main() (
    set -eu
    root=''
    version="${BITS_VERSION:-}"
    update_from=''
    while [ "$#" -gt 0 ]; do
        [ "$#" -ge 2 ] || fail "$1 needs a value"
        case "$1" in
        --root) root="$2" ;;
        --version) version="$2" ;;
        --update-from) update_from="$2" ;;
        *) fail "unknown option: $1" ;;
        esac
        shift 2
    done
    [ -n "$version" ] || version="$(latest)"
    version="v${version#v}"
    stable "$version" || fail "$version is not a release version, such as v0.1.0"
    older "$version" v0.0.15 && fail "Bits $version predates this installer; install v0.0.15 or later"
    if [ -n "$update_from" ]; then
        stable "$update_from" || fail "invalid originating version: $update_from"
        older "$update_from" "$version" || fail 'an update must select a newer version'
        [ -n "$root" ] || fail 'an update needs its installation root'
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

    if [ -z "$root" ]; then
        case "${XDG_DATA_HOME:-}" in
        /*) root="$XDG_DATA_HOME/bits" ;;
        *) root="$HOME/.local/share/bits" ;;
        esac
    fi
    case "$root" in /*) ;; *) fail 'the installation root must be absolute' ;; esac
    mkdir -p "$root/versions"
    root="$(cd -P "$root" && pwd)"
    previous="$(active_version)"
    if [ -n "$update_from" ] && [ "$previous" != "$update_from" ]; then
        say 'The active Bits installation changed; run bits update again if needed.'
        return
    fi
    archive="bits_${version#v}_${os}_${arch}.tar.gz"
    dir="$root/versions/$version"

    mkdir -p "$root/versions"
    staging="$(mktemp -d "$root/.staging-XXXXXX")"
    trap 'rm -rf "$staging"; if [ -n "${command_staging:-}" ]; then rm -rf "$command_staging"; fi' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM

    say "Downloading Bits $version for $os/$arch"
    download "$releases/download/$version/bits_${version#v}_checksums.txt" "$staging/checksums.txt"
    download "$releases/download/$version/$archive" "$staging/$archive"
    expected="$(awk -v name="$archive" '$2 == name || $2 == "*" name { print $1 }' "$staging/checksums.txt")"
    [ -n "$expected" ] || fail "the release has no checksum for $archive"
    actual="$(sha256 "$staging/$archive")"
    [ "$actual" = "$expected" ] || fail "$archive does not match its checksum"

    # Only regular files and directories belong to a release. Validate before
    # extraction; archive paths must stay within the staged prefix.
    tar -tzf "$staging/$archive" >"$staging/names"
    awk '/^\// || /(^|\/)\.\.(\/|$)/ || /\\/ { exit 1 }' "$staging/names" || fail 'unsafe archive path'
    tar -tvzf "$staging/$archive" >"$staging/types"
    awk '(substr($0, 1, 1) != "-" && substr($0, 1, 1) != "d") || substr($0, 1, 10) ~ /[sStT]/ { exit 1 }' "$staging/types" || fail 'unsupported archive entry'
    prefix="$staging/prefix"
    mkdir "$prefix"
    (cd "$prefix" && tar --no-same-owner --no-same-permissions -xzf "$staging/$archive")
    [ -f "$prefix/bin/bits" ] && [ -x "$prefix/bin/bits" ] || fail 'the archive has no executable bin/bits'
    # The lock file stays in place: unlinking it could let another installer
    # lock a different inode. Kernel-owned locks disappear when processes exit.
    exec 9>"$root/install.lock"
    if [ "$os" = darwin ]; then
        /usr/bin/perl -e 'flock(STDIN, 2) or die $!;' <&9
    else
        flock 9
    fi
    previous="$(active_version)"
    if [ -n "$update_from" ] && [ "$previous" != "$update_from" ]; then
        say 'The active Bits installation changed; run bits update again if needed.'
        return
    fi
    # Check the executable that will actually be selected. A complete existing
    # version wins over a fresh download, including on same-version reinstalls.
    binary="$prefix/bin/bits"
    if [ -e "$dir" ]; then
        [ -f "$dir/.install-complete" ] || fail "$dir is incomplete; remove it and try again"
        binary="$dir/bin/bits"
    fi
    actual_version="$("$binary" --version)" || fail "could not run $binary"
    [ "$actual_version" = "bits version $version" ] || fail "expected Bits $version; $binary reported $actual_version"
    : >"$prefix/.install-complete"
    if [ ! -e "$dir" ]; then mv "$prefix" "$dir"; fi
    ln -s "versions/$version" "$staging/current"
    replace_link "$staging/current" "$root/current"
    clean_up || say 'Warning: could not clean up old Bits releases; installation succeeded.'
    say "Installed Bits $version in $root"
    set_up_command
)

{ main "$@"; }
