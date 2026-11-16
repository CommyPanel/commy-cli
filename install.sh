#!/usr/bin/env bash
# Installs commy for your system from a GitHub release, after checking its checksum.
#   COMMY_VERSION=0.1.0       install this version instead of the latest
#   COMMY_INSTALL_DIR=...     where to put it (default: ~/.local/bin)
set -euo pipefail

repo="commypanel/commy-cli"
dir="${COMMY_INSTALL_DIR:-$HOME/.local/bin}"
version="${COMMY_VERSION:-latest}"

case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) echo "Unsupported system: $(uname -s). Download a binary from https://github.com/$repo/releases" >&2; exit 1 ;;
esac

case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) echo "Unsupported processor: $(uname -m)" >&2; exit 1 ;;
esac

command -v curl >/dev/null 2>&1 || { echo "curl is required." >&2; exit 1; }

if [ "$version" = latest ]; then
    version="$(curl -fsSL --proto '=https' --tlsv1.2 -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest" | sed 's|.*/tag/v||')"
    [ -n "$version" ] || { echo "Could not find the latest version." >&2; exit 1; }
fi

version="${version#v}"
case "$version" in *[!0-9A-Za-z._-]*|"") echo "Not a valid version: $version" >&2; exit 1 ;; esac

name="commy_${version}_${os}_${arch}"
base="https://github.com/$repo/releases/download/v${version}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "Downloading commy $version for $os/$arch"
curl -fsSL --proto '=https' --tlsv1.2 -o "$work/$name" "$base/$name"
curl -fsSL --proto '=https' --tlsv1.2 -o "$work/SHA256SUMS" "$base/SHA256SUMS"

expected="$(awk -v n="$name" '$2 == n { print $1 }' "$work/SHA256SUMS")"
[ -n "$expected" ] || { echo "No checksum for $name in the release." >&2; exit 1; }

if command -v sha256sum >/dev/null 2>&1; then actual="$(sha256sum "$work/$name" | awk '{print $1}')"; else actual="$(shasum -a 256 "$work/$name" | awk '{print $1}')"; fi
[ "$actual" = "$expected" ] || { echo "The checksum does not match: the download is damaged or has been tampered with. Nothing was installed." >&2; exit 1; }

mkdir -p "$dir"
install -m 0755 "$work/$name" "$dir/commy"
echo "Installed $dir/commy"

case ":$PATH:" in *":$dir:"*) ;; *) echo "Add $dir to your PATH to run it as: commy" ;; esac
"$dir/commy" version
