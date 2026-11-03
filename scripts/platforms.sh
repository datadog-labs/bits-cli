# shellcheck shell=bash
# platforms.sh - the os/arch targets bits is released for, sourced by the
# release scripts. The CI build matrix covers the same targets.

# shellcheck disable=SC2034
PLATFORMS=(darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64)
