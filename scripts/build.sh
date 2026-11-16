#!/usr/bin/env bash
# Builds commy for macOS, Linux and Windows (amd64 and arm64) into dist/, with a SHA256SUMS file.
# Usage: scripts/build.sh VERSION      (for example: scripts/build.sh 0.1.0)
set -euo pipefail

version="${1:?usage: scripts/build.sh VERSION}"
version="${version#v}"
cd "$(dirname "$0")/.."
rm -rf dist
mkdir dist

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
    os="${target%/*}"
    arch="${target#*/}"
    ext=""
    [ "$os" = windows ] && ext=".exe"
    out="dist/commy_${version}_${os}_${arch}${ext}"
    echo "Building $out"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=${version}" -o "$out" .
done

(cd dist && if command -v sha256sum >/dev/null 2>&1; then sha256sum commy_*; else shasum -a 256 commy_*; fi > SHA256SUMS)
echo "Done: $(ls dist | wc -l) files in dist/"
