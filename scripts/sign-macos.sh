#!/usr/bin/env bash
#
# sign-macos.sh - sign a macOS binary with a Developer ID certificate and
# notarize it with Apple, using quill, which runs on any platform.
#
# Credentials, where the p12 and the key are a file path or base64 content:
#   QUILL_SIGN_P12, QUILL_SIGN_PASSWORD
#   QUILL_NOTARY_KEY, QUILL_NOTARY_KEY_ID, QUILL_NOTARY_ISSUER

set -euo pipefail

QUILL_VERSION="v0.7.1"

if [[ $# -ne 1 || ! -f "$1" ]]; then
    echo "usage: $0 <binary>" >&2
    exit 1
fi

for var in QUILL_SIGN_P12 QUILL_SIGN_PASSWORD QUILL_NOTARY_KEY QUILL_NOTARY_KEY_ID QUILL_NOTARY_ISSUER; do
    if [[ -z "${!var:-}" ]]; then
        echo "ERROR: $var is not set" >&2
        exit 1
    fi
done

go run "github.com/anchore/quill/cmd/quill@$QUILL_VERSION" sign-and-notarize --verbose "$1"
