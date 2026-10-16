#!/usr/bin/env bash
#
# try-update.sh - exercise the update prompt end to end without Homebrew or
# a release. It renders the real formula template for v0.0.2, serves it on a
# local port, and installs a v0.0.1 build, pointed at that port, into a fake
# Homebrew prefix whose brew "upgrades" to a v0.0.2 build. Bits runs on the
# fake assistant backend. The check runs in the background, so the first
# launch only fills the cache and the second one prompts. Relaunch after each
# exit to see what persisted. FAIL=1 makes brew fail.
#
# The cache lives under this demo's temporary config directory.

set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX="$(mktemp -d)"
CONFIG_DIR="$PREFIX/config"
STATE="$CONFIG_DIR/update.json"
SERVER_PID=""

restore() {
    [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" 2>/dev/null
    rm -rf "$PREFIX"
}
trap restore EXIT

# Render the formula the release would publish, with placeholder checksums.
mkdir -p "$PREFIX/www/Formula"
ruby -rerb -e '
  values = { "version" => "0.0.2" }
  %w[darwin_amd64 darwin_arm64 linux_amd64 linux_arm64].each { |p| values["sha256_#{p}"] = "0" * 64 }
  print ERB.new(File.read(ARGV[0])).result(binding)
' "$SRC_DIR/.github/homebrew/bits.rb.erb" > "$PREFIX/www/Formula/bits.rb"

PORT="$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$PREFIX/www" > "$PREFIX/server.log" 2>&1 &
SERVER_PID=$!
URL="http://127.0.0.1:$PORT/Formula/bits.rb"

for version in 0.0.1 0.0.2; do
    keg="$PREFIX/Cellar/bits/$version"
    mkdir -p "$keg/bin"
    built="$(VERSION="$version" BUILD_DIR="$PREFIX/build-$version" \
        EXTRA_LDFLAGS="-X github.com/datadog-labs/bits-cli/internal/update.formulaURL=$URL" \
        "$SRC_DIR/scripts/build.sh")"
    mv "$built" "$keg/bin/bits"
    echo '{"source":{"tap":"datadog-labs/pack"}}' > "$keg/INSTALL_RECEIPT.json"
done

mkdir -p "$PREFIX/bin" "$PREFIX/opt"
ln -s ../Cellar/bits/0.0.1 "$PREFIX/opt/bits"
ln -s ../Cellar/bits/0.0.1/bin/bits "$PREFIX/bin/bits"
cat > "$PREFIX/bin/brew" <<'BREW'
#!/bin/sh
echo "[fake brew] $* (HOMEBREW_AUTO_UPDATE_SECS=$HOMEBREW_AUTO_UPDATE_SECS)"
[ -n "$FAIL" ] && { echo "[fake brew] Error: simulated failure" >&2; exit 1; }
sleep 1
prefix="$(cd "$(dirname "$0")/.." && pwd)"
ln -sfn ../Cellar/bits/0.0.2 "$prefix/opt/bits"
ln -sfn ../Cellar/bits/0.0.2/bin/bits "$prefix/bin/bits"
echo "[fake brew] bits 0.0.1 -> 0.0.2"
BREW
chmod +x "$PREFIX/bin/brew"

# Start without a cache so the first launch performs the real check.
rm -f "$STATE"
echo "--- serving $URL ($(grep -m1 'releases/download/v' "$PREFIX/www/Formula/bits.rb" | sed -E 's|.*releases/download/(v[^/]+)/.*|\1|'))"

while true; do
    echo "--- launching $("$PREFIX/bin/bits" --version)"
    BITS_CONFIG_DIR="$CONFIG_DIR" BITS_FAKE_BACKEND=1 "$PREFIX/bin/bits" || echo "--- bits exited with status $?"
    echo "--- update.json: $(cat "$STATE" 2>/dev/null || echo '(none)')"
    echo "--- formula requests: $(grep -c 'GET /Formula/bits.rb' "$PREFIX/server.log" || true)"
    read -r -p "--- Enter to relaunch, ctrl-c to stop: " _ || break
done
