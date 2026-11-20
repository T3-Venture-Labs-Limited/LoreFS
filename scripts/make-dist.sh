#!/usr/bin/env bash
# Builds the release tarball that is given to another computer:
#   dist/lorefs-<version>-linux-<arch>.tar.gz  and its .sha256
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${1:-dev}"
case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; *) echo "unsupported processor" >&2; exit 1 ;; esac
NAME="lorefs-$VERSION-linux-$ARCH"
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/$NAME" "$ROOT/dist"
install -m 0755 "$ROOT/bin/lorefs" "$WORK/$NAME/lorefs"
install -m 0755 "$ROOT/install.sh" "$WORK/$NAME/install.sh"
install -m 0755 "$ROOT/scripts/get-juicefs.sh" "$WORK/$NAME/get-juicefs.sh"
cat > "$WORK/$NAME/README.txt" <<TXT
LoreFS $VERSION

1. Run:   ./install.sh
2. Run:   lorefs join      (and paste the join code you were given)

To check the drive at any time:   lorefs status
To upgrade later: unpack the newer release and run ./install.sh again.
TXT
tar -C "$WORK" -czf "$ROOT/dist/$NAME.tar.gz" "$NAME"
( cd "$ROOT/dist" && sha256sum "$NAME.tar.gz" > "$NAME.tar.gz.sha256" )
echo "dist/$NAME.tar.gz"
