#!/usr/bin/env bash
#
# sbom.sh - write the SPDX SBOM of a staged release archive directory, listing
# the Go modules compiled into the binary, using syft.

set -euo pipefail

SYFT_VERSION="v1.54.1"

if [[ $# -ne 3 || ! -d "$1" ]]; then
    echo "usage: $0 <directory> <version> <output>" >&2
    exit 1
fi

go run "github.com/anchore/syft/cmd/syft@$SYFT_VERSION" scan "dir:$1" \
    --source-name bits --source-version "$2" --output "spdx-json=$3" --quiet
