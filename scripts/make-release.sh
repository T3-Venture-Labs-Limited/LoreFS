#!/usr/bin/env bash
# Builds the files of a release into dist/release/:
#   lorefs-<version>-linux-amd64.tar.gz, lorefs-<version>-linux-arm64.tar.gz
#   install.sh        the one-line installer (scripts/online-install.sh)
#   SHA256SUMS        checksums of all of the above
# Each archive holds the lorefs program, the pinned juicefs program, the
# archive's installer, the licence and the notice. Programs are static, so
# both architectures are built here whatever the machine's own processor.
#
# Usage: scripts/make-release.sh <version>      (for example 0.1.0, without "v")
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${1:?usage: make-release.sh <version>}"; VERSION="${VERSION#v}"
OUT="$ROOT/dist/release"
COMMIT_TIME="$(git -C "$ROOT" log -1 --format=%ct 2>/dev/null || echo 0)"
rm -rf "$OUT"; mkdir -p "$OUT"
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT

for arch in amd64 arm64; do
  name="lorefs-$VERSION-linux-$arch"
  d="$work/$name"; mkdir -p "$d"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=v$VERSION -X main.commitTime=$COMMIT_TIME" -o "$d/lorefs" ./cmd/lorefs)
  JUICEFS_ARCH="$arch" "$ROOT/scripts/get-juicefs.sh" "$d" >/dev/null
  install -m 0755 "$ROOT/install.sh" "$d/install.sh"
  install -m 0755 "$ROOT/scripts/get-juicefs.sh" "$d/get-juicefs.sh"
  install -m 0644 "$ROOT/LICENSE" "$ROOT/NOTICE" "$d/"
  cat > "$d/README.txt" <<TXT
LoreFS $VERSION ($arch)

1. Run:   ./install.sh
2. Run:   lorefs create --local      (to try it on this computer)
   or:    lorefs join               (and paste a join code you were given)

To check the drive at any time:   lorefs status
To upgrade later:                 lorefs update
TXT
  tar -C "$work" --owner=0 --group=0 --numeric-owner -czf "$OUT/$name.tar.gz" "$name"
done
install -m 0755 "$ROOT/scripts/online-install.sh" "$OUT/install.sh"
( cd "$OUT" && sha256sum lorefs-*.tar.gz install.sh > SHA256SUMS )
ls -l "$OUT"
