#!/usr/bin/env bash
# Downloads the pinned JuiceFS release, checks its SHA-256 and places the
# `juicefs` binary in the directory given as the first argument.
# Installs nothing if the checksum does not match.
set -euo pipefail

JUICEFS_VERSION="1.4.2"
declare -A SHA256=(
  [amd64]="79648a0a67145bf42ca4e8336578c13a5bbc9a0bd34df340b3b93dac1e975247"
  [arm64]="19f72a30d3642a2f7971c8c4a31d2647de62f19520716e85c3a777de5f4c9036"
)

dest="${1:?usage: get-juicefs.sh <directory>}"
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64) arch=arm64 ;;
  *) echo "Unsupported processor: $(uname -m)" >&2; exit 1 ;;
esac

if [ -x "$dest/juicefs" ] && "$dest/juicefs" version 2>/dev/null | grep -q "version ${JUICEFS_VERSION}+"; then
  echo "JuiceFS ${JUICEFS_VERSION} is already installed in $dest"
  exit 0
fi

name="juicefs-${JUICEFS_VERSION}-linux-${arch}.tar.gz"
url="${JUICEFS_URL_BASE:-https://github.com/juicedata/juicefs/releases/download/v${JUICEFS_VERSION}}/${name}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "Downloading JuiceFS ${JUICEFS_VERSION}..."
curl -fsSL --retry 3 -o "$work/$name" "$url"

got="$(sha256sum "$work/$name" | cut -d' ' -f1)"
want="${JUICEFS_SHA256_OVERRIDE:-${SHA256[$arch]}}"
if [ "$got" != "$want" ]; then
  echo "The JuiceFS download does not match its expected checksum. Nothing was installed." >&2
  echo "  expected $want" >&2
  echo "  got      $got" >&2
  exit 1
fi

tar -xzf "$work/$name" -C "$work" juicefs
mkdir -p "$dest"
install -m 0755 "$work/juicefs" "$dest/juicefs.new"
mv -f "$dest/juicefs.new" "$dest/juicefs"
echo "Installed JuiceFS ${JUICEFS_VERSION} in $dest"
