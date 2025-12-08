#!/bin/bash
# Build wrapper that sets CGO_CFLAGS to use vendored systemd headers.
# Use this instead of 'go build' directly, or use 'make'.
#
# Examples:
#   ./build.sh ./cmd/swash/...
#   ./build.sh -o bin/swash ./cmd/swash/
#   ./build.sh ./...

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export CGO_CFLAGS="-I${SCRIPT_DIR}/cvendor ${CGO_CFLAGS:-}"

exec go build "$@"
