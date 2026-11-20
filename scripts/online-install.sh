#!/bin/sh
# The one-line installer for LoreFS (Linux). It is published with each release:
#
#   curl -fsSL https://github.com/T3-Venture-Labs-Limited/LoreFS/releases/latest/download/install.sh | sh
#
# It finds the newest release, downloads the archive for this processor,
# checks it against the published checksums and runs the installer inside
# it. Nothing is installed unless the checks pass. Everything goes under
# your home folder; no administrator password is needed.
#
# LOREFS_RELEASE_URL points it at another place (a mirror, a test folder).
set -eu

BASE="${LOREFS_RELEASE_URL:-https://github.com/T3-Venture-Labs-Limited/LoreFS/releases/latest/download}"
BASE="${BASE%/}"

fail() { printf '\n%s\n' "$1" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || fail "LoreFS runs on Linux only for now."
case "$(uname -m)" in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "There is no LoreFS release for this processor ($(uname -m))." ;;
esac
for tool in curl tar sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || fail "The installer needs the program \"$tool\", which is not installed. Nothing was installed."
done

# FUSE 3 is the one thing that needs an administrator to install. Say so
# before downloading anything.
if ! command -v fusermount3 >/dev/null 2>&1; then
  hint="sudo pacman -S fuse3"
  command -v apt-get >/dev/null 2>&1 && hint="sudo apt-get install fuse3"
  command -v dnf >/dev/null 2>&1 && hint="sudo dnf install fuse3"
  fail "LoreFS needs FUSE 3, which is not installed. Nothing was installed.
Run this once, then run the installer again:

  $hint"
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

curl -fsSL --retry 3 -o "$work/SHA256SUMS" "$BASE/SHA256SUMS" \
  || fail "Could not reach the LoreFS releases at $BASE. Nothing was installed."

# The newest archive for this processor named in the list of checksums.
line="$(grep -E "  lorefs-[^ ]+-linux-$arch\.tar\.gz\$" "$work/SHA256SUMS" | sort -k2 -V | tail -n 1 || true)"
[ -n "$line" ] || fail "The release lists no archive for $arch. Nothing was installed."
name="${line##*  }"

echo "Downloading $name..."
curl -fsSL --retry 3 -o "$work/$name" "$BASE/$name" \
  || fail "The download failed. Nothing was installed."
( cd "$work" && printf '%s\n' "$line" | sha256sum -c - >/dev/null 2>&1 ) \
  || fail "The downloaded file does not match its published checksum, so it was not installed. Nothing was changed."

tar -xzf "$work/$name" -C "$work" || fail "The downloaded file could not be unpacked. Nothing was changed."
dir="$work/${name%.tar.gz}"
[ -x "$dir/install.sh" ] || fail "The release has no installer. Nothing was changed."
cd "$dir"
./install.sh
