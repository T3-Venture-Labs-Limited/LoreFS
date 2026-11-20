#!/usr/bin/env bash
# Rehearses the storage provider going away: MinIO sits behind a forwarder
# (scripts/forwarder) that can refuse connections or stop answering. Records
# how long an uncached read and a save take to fail, that listings and cached
# files keep working, and how long everything takes to recover without
# reconnecting. Takes about 20 minutes.
#
# Targets: an uncached read fails within 3 minutes, a save within 6 minutes,
# recovery within 60 seconds.
#
# Usage: scripts/storage-outage-test.sh [--cleanup-only]
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LORE="$ROOT/bin/lorefs"
T="${LOREFS_OTEST_DIR:-/tmp/lorefs-otest}"
MINIO=lorefs-otest-minio; MINIO_PORT=19300; FWD_PORT=19301
IDX=lorefs-otest-index; IDX_PORT=16470; IDX_PW=otest-index-password-xyz
S_KEY=otestaccesskey; S_SECRET=otest-storage-secret-123

"$ROOT/scripts/get-juicefs.sh" "$ROOT/.tools" >/dev/null || { echo "could not get JuiceFS"; exit 1; }
JFS="$ROOT/.tools/juicefs"
export LOREFS_JUICEFS="$JFS" LOREFS_NO_SYSTEMD=1 LOREFS_NO_NOTIFY=1
PASS=0; FAIL=0; FAILED=()
ok()  { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
within() { local s="$1"; shift; local end=$((SECONDS+s)); until "$@" >/dev/null 2>&1; do [ $SECONDS -ge $end ] && return 1; sleep 0.5; done; }

A="$T/alpha/Lore"; B="$T/beta/Lore"
lore() { LOREFS_CONFIG_DIR="$T/alpha/config" LOREFS_STATE_DIR="$T/alpha/state" "$LORE" "$@"; }
loreb() { LOREFS_CONFIG_DIR="$T/beta/config" LOREFS_STATE_DIR="$T/beta/state" "$LORE" "$@"; }
setmode() { echo "$1" >"$T/mode"; sleep 0.6; }

cleanup() {
  [ -f "$T/fwd.pid" ] && kill "$(cat "$T/fwd.pid")" 2>/dev/null
  for m in "$A" "$B"; do "$JFS" umount "$m" >/dev/null 2>&1 || fusermount3 -uz "$m" 2>/dev/null; done
  sleep 1; pkill -f "$JFS mount redis://127.0.0.1:$IDX_PORT" 2>/dev/null
  docker rm -f "$MINIO" "$IDX" >/dev/null 2>&1
  chmod -R u+rwx "$T" 2>/dev/null; rm -rf "$T"
}
trap 'echo; echo interrupted; cleanup; exit 130' INT TERM
cleanup 2>/dev/null
[ "${1:-}" = --cleanup-only ] && { echo "cleaned up $T"; exit 0; }
mkdir -p "$T/minio" "$T/alpha" "$T/beta"
(cd "$ROOT" && go build -o bin/forwarder ./scripts/forwarder) || { echo "could not build the forwarder"; exit 1; }

section "A drive whose storage is reached through a forwarder"
docker run -d --name "$MINIO" --user "$(id -u):$(id -g)" -p "127.0.0.1:$MINIO_PORT:9000" -v "$T/minio:/data" \
  -e MINIO_ROOT_USER="$S_KEY" -e MINIO_ROOT_PASSWORD="$S_SECRET" cgr.dev/chainguard/minio:latest server /data >/dev/null || exit 1
docker run -d --name "$IDX" -p "127.0.0.1:$IDX_PORT:6379" redis:8-alpine redis-server --requirepass "$IDX_PW" --appendonly no >/dev/null
within 30 curl -sf "http://127.0.0.1:$MINIO_PORT/minio/health/live" || { echo "MinIO did not start"; exit 1; }
within 20 docker exec "$IDX" redis-cli -a "$IDX_PW" --no-auth-warning ping
echo ok >"$T/mode"
(setsid "$ROOT/bin/forwarder" -listen "127.0.0.1:$FWD_PORT" -to "127.0.0.1:$MINIO_PORT" -mode-file "$T/mode" >"$T/fwd.log" 2>&1 & echo $! >"$T/fwd.pid")
sleep 1
LOREFS_STORAGE_KEY="$S_KEY" LOREFS_STORAGE_SECRET="$S_SECRET" LOREFS_INDEX_PASSWORD="$IDX_PW" lore create \
  --bucket "http://127.0.0.1:$FWD_PORT/otestbucket" --index "redis://127.0.0.1:$IDX_PORT/1" --allow-unencrypted-index \
  --name otest-drive --machine alpha --folder "$A" --connect </dev/null >"$T/create.out" 2>"$T/create.err" && ok "the drive is created through the forwarder" || { bad "the drive is created" "$(cat "$T/create.err")"; cleanup; exit 1; }
lore mount pre; lore mount run >"$T/alpha/mount.log" 2>&1 &
within 40 test -e "$A/.config" && ok "the drive mounts" || { bad "the drive mounts"; cleanup; exit 1; }
# A second computer reads files its mount has never fetched: those are the uncached ones.
grep '^lorefs2_' "$T/create.out" | loreb join --token-stdin --name beta --folder "$B" --cache-dir "$T/beta/cache" >/dev/null 2>&1 && ok "a second computer joins" || { bad "a second computer joins"; cleanup; exit 1; }
loreb mount pre; loreb mount run >"$T/beta/mount.log" 2>&1 &
within 40 test -e "$B/.config" && ok "and mounts the drive" || { bad "the second computer mounts the drive"; cleanup; exit 1; }
mkdir -p "$A/data"; echo "small and cached" >"$A/data/cached.txt"; sync; sleep 2
within 20 test -e "$B/data/cached.txt"; cat "$B/data/cached.txt" >/dev/null
fresh() { # fresh NAME: a 5 MB file written on alpha that beta has never read
  head -c 5000000 /dev/urandom >"$A/data/$1"; sync; within 30 test -e "$B/data/$1"; }
fresh probe.bin
cat "$B/data/probe.bin" >/dev/null && ok "an uncached read works while the storage is up" || bad "an uncached read works while the storage is up"

outage() { # outage MODE
  local m="$1"
  section "The storage $( [ "$m" = refuse ] && echo refuses connections || echo stops answering )"
  fresh "uncached-$m.bin"
  setmode "$m"
  t0=$SECONDS
  timeout 400 cat "$B/data/uncached-$m.bin" >/dev/null 2>"$T/read.err"; rc=$?
  took=$((SECONDS-t0))
  [ $rc -ne 0 ] && [ $rc -ne 124 ] && [ $took -le 180 ] && ok "an uncached read fails with an error after $took s (target: within 180 s)" || bad "an uncached read fails within 180 s" "rc=$rc after $took s: $(cat "$T/read.err")"
  t0=$SECONDS; timeout 5 ls -la "$B/data" >/dev/null 2>&1 && ok "a folder listing still works ($((SECONDS-t0)) s)" || bad "a folder listing still works"
  [ "$(timeout 10 cat "$B/data/cached.txt" 2>/dev/null)" = "small and cached" ] && ok "a cached file can still be read" || bad "a cached file can still be read"
  t0=$SECONDS
  timeout 600 sh -c "head -c 200000 /dev/urandom >'$A/data/save-$m.bin' && sync" 2>"$T/save.err"; rc=$?
  took=$((SECONDS-t0))
  [ $rc -ne 0 ] && [ $rc -ne 124 ] && [ $took -le 360 ] && ok "a save fails with an error after $took s (target: within 360 s)" || bad "a save fails within 360 s" "rc=$rc after $took s: $(cat "$T/save.err")"
  setmode ok
  t0=$SECONDS
  if within 60 sh -c "cat '$B/data/uncached-$m.bin' >/dev/null 2>&1"; then ok "an uncached read works again $((SECONDS-t0)) s after the storage is back, without reconnecting (target: within 60 s)"; else bad "an uncached read works again within 60 s" "still failing after 60 s"; fi
  t0=$SECONDS
  if within 60 sh -c "echo recovered-$m >'$A/data/after-$m.txt' && sync"; then ok "a save works again $((SECONDS-t0)) s after the storage is back"; else bad "a save works again within 60 s"; fi
  [ "$(cat "$A/data/after-$m.txt" 2>/dev/null)" = "recovered-$m" ] && ok "and what was saved reads back" || bad "what was saved reads back"
}
outage refuse
outage silent
lore status --json 2>/dev/null | jq -e '.checks[] | select(.id == "storage") | .level == "ok"' >/dev/null && ok "status shows the storage as reachable again" || bad "status shows the storage as reachable again"

echo
printf '\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -gt 0 ] && { printf '  failed: %s\n' "${FAILED[@]}"; }
cleanup
[ "$FAIL" = 0 ]
