#!/usr/bin/env bash
# Builds the unpackers for every platform, embeds them into the packer, then
# builds the packer for every platform into dist/.
#
#   ./build.sh            all platforms
#   ./build.sh host       only the packer for this machine (still embeds all unpackers)
set -euo pipefail
cd "$(dirname "$0")"

TARGETS=(windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64)
FLAGS=(-trimpath -ldflags "-s -w")
export CGO_ENABLED=0

rm -f internal/stubs/bin/*.gz
mkdir -p dist

echo "Building unpackers"
for t in "${TARGETS[@]}"; do
  os=${t%/*}; arch=${t#*/}
  GOOS=$os GOARCH=$arch go build "${FLAGS[@]}" -o "dist/.stub" ./cmd/unpacker
  gzip -9 -c dist/.stub > "internal/stubs/bin/unpacker-$os-$arch.gz"
done
rm -f dist/.stub

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
