#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
  echo "error: 'go' is not installed. Install Go 1.25+ from https://go.dev/dl/" >&2
  exit 2
fi

GO_VERSION=$(go version | awk '{print $3}' | sed 's/^go//')
# Keep in sync with go.mod (`go 1.25.7`). Older toolchains fail the build
# later with a cryptic error; gate here with a clear message.
MIN="1.25"
if [ "$(printf '%s\n%s\n' "$MIN" "$GO_VERSION" | sort -V | head -1)" != "$MIN" ]; then
  echo "error: go ${GO_VERSION} is too old; need ${MIN}+" >&2
  exit 2
fi

if ! command -v terraform >/dev/null 2>&1; then
  echo "warning: terraform not on PATH — required at probe time, not install time" >&2
fi

mkdir -p bin
VERSION=$(cat VERSION)
go build -ldflags "-X github.com/mgt-tool/mgtt/sdk/provider.Version=${VERSION}" -o bin/mgtt-provider-terraform .
echo "✓ built bin/mgtt-provider-terraform ${VERSION}"
