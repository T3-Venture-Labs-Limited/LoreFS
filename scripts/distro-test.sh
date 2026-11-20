#!/usr/bin/env bash
# Runs the installer and the core checks in four distributions' containers
# (Ubuntu LTS, Debian stable, Fedora, Arch) with FUSE passed through, from a
# release served over HTTP on this computer. With "walkthrough", runs only
# the first section of the README in a fresh Ubuntu container, and checks it
# takes under five minutes.
#
# Usage: scripts/distro-test.sh [walkthrough | --cleanup-only]
set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
T="${LOREFS_DTEST_DIR:-/tmp/lorefs-dtest}"
PORT=18090
MODE="${1:-matrix}"
export PATH="$HOME/.local/share/mise/installs/go/1.27.4/bin:$PATH"; unset GOROOT

cleanup() { [ -f "$T/server.pid" ] && kill "$(cat "$T/server.pid")" 2>/dev/null; rm -rf "$T"; }
trap 'echo interrupted; cleanup; exit 130' INT TERM
cleanup 2>/dev/null
[ "$MODE" = --cleanup-only ] && { echo "cleaned up $T"; exit 0; }
mkdir -p "$T"
echo "Building a release to install from..."
(cd "$ROOT" && scripts/make-release.sh 0.0.9 >"$T/make.log" 2>&1) || { cat "$T/make.log"; exit 1; }
mkdir -p "$T/rel"; cp "$ROOT"/dist/release/* "$T/rel/"
(cd "$T/rel" && exec python3 -m http.server "$PORT" --bind 127.0.0.1 >"$T/http.log" 2>&1) & echo $! >"$T/server.pid"
sleep 1
URL="http://127.0.0.1:$PORT"

# The commands of the README's first section.
awk '/^## Try it in two minutes/{f=1} f&&/^```/{n++; next} f&&n==1{print} n==2{exit}' "$ROOT/README.md" \
  | sed "s#https://github.com/T3-Venture-Labs-Limited/LoreFS/releases/latest/download#$URL#g" \
  | grep -v '^lorefs history\|^lorefs status' >"$T/readme-commands.sh"

run() { # run IMAGE MODE
  docker run --rm --network host --device /dev/fuse --cap-add SYS_ADMIN --security-opt apparmor:unconfined \
    -e "LOREFS_RELEASE_URL=$URL" -v "$ROOT/scripts/distro-inner.sh:/inner.sh:ro" -v "$T/readme-commands.sh:/tmp/readme-commands.sh:ro" \
    "$1" sh /inner.sh "$2"
}
fail=0
if [ "$MODE" = walkthrough ]; then
  echo "== README walkthrough in a fresh ubuntu:24.04 container"
  cat "$T/readme-commands.sh"
  start=$(date +%s)
  run ubuntu:24.04 walkthrough 2>&1 | sed 's/^/   /' | tee "$T/walk.out"; [ "${PIPESTATUS[0]}" = 0 ] || fail=1
  took=$(( $(date +%s) - start ))
  echo "== the whole walkthrough, packages included, took $took s"
  [ "$took" -lt 300 ] && echo "ok  under five minutes" || { echo "FAIL over five minutes"; fail=1; }
else
  for img in ubuntu:24.04 debian:stable fedora:latest archlinux:latest; do
    echo "== $img"
    out=$(run "$img" core 2>&1); code=$?
    echo "$out" | sed 's/^/   /'
    [ $code = 0 ] && echo "   PASS $img" || { echo "   FAILED $img (exit $code)"; fail=1; }
  done
fi
cleanup
[ $fail = 0 ]
