#!/usr/bin/env bash
# Builds the unpackers for every platform, embeds them into the packer, then
# builds the packer for every platform into dist/.
#
#   ./build.sh            all platforms
#   ./build.sh host       only the packer for this machine (still embeds all unpackers)
set -euo pipefail
cd "$(dirname "$0")"

# Preflight: make sure Go is there and new enough, then fetch the libraries.
# The Go version and the libraries are listed in go.mod and go.sum.
fail() { echo "error: $*" >&2; exit 1; }

command -v go >/dev/null 2>&1 || fail "Go was not found on your PATH.
Install Go from https://go.dev/dl/ (Linux: your package manager or the tarball
from that page, macOS: 'brew install go'), then open a new terminal."

# Turns 1.26 into 1.26.0 so versions compare properly.
norm() { awk -F. '{ printf "%s.%s.%s\n", $1, $2, ($3 == "" ? 0 : $3) }'; }
required=$(awk '$1 == "go" { print $2; exit }' go.mod | norm)
have=$(go env GOVERSION 2>&1) || fail "Go could not start: $have
If it is trying to download a newer Go, check your internet connection, or install
Go $required or newer yourself from https://go.dev/dl/."
have=$(printf '%s' "$have" | sed 's/^go//; s/[^0-9.].*$//' | norm)
echo "Go $have found, this project needs Go $required or newer"
if [ "$(printf '%s\n%s\n' "$required" "$have" | sort -V | head -n 1)" != "$required" ]; then
  if [ "$(go env GOTOOLCHAIN)" = "local" ]; then
    fail "Go $have is too old and GOTOOLCHAIN is set to local. Install Go $required or newer."
  fi
  echo "Go will download the newer toolchain itself on the first build (needs internet)."
fi

echo "Checking libraries"
go mod download || fail "could not download the libraries. Check your internet connection
and proxy settings (GOPROXY is $(go env GOPROXY))."
go mod verify >/dev/null || fail "a downloaded library does not match go.sum. Delete the Go module cache
('go clean -modcache') and try again."

TARGETS=(windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64)
FLAGS=(-trimpath -buildvcs=false -ldflags "-s -w")
export CGO_ENABLED=0

rm -f internal/stubs/bin/*.gz internal/stubs/bin/MANIFEST
mkdir -p dist

# Small helper that compresses and checksums the same way on every system.
go build -o dist/.stubpack ./tools/stubpack

echo "Building unpackers"
for t in "${TARGETS[@]}"; do
  os=${t%/*}; arch=${t#*/}
  GOOS=$os GOARCH=$arch go build "${FLAGS[@]}" -o "dist/.stub" ./cmd/unpacker
  dist/.stubpack gz dist/.stub "internal/stubs/bin/unpacker-$os-$arch.gz"
done
rm -f dist/.stub

# The packer checks the embedded unpackers against this list before using them.
dist/.stubpack manifest internal/stubs/bin
rm -f dist/.stubpack

echo "Building packers"
if [ "${1:-}" = "host" ]; then
  go build "${FLAGS[@]}" -o dist/ouroboros ./cmd/ouroboros
else
  for t in "${TARGETS[@]}"; do
    os=${t%/*}; arch=${t#*/}
    ext=""; [ "$os" = windows ] && ext=".exe"
    GOOS=$os GOARCH=$arch go build "${FLAGS[@]}" -o "dist/ouroboros-$os-$arch$ext" ./cmd/ouroboros
  done
fi
ls -lh dist
