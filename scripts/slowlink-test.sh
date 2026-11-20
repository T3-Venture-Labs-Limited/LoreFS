#!/usr/bin/env bash
# Checks how LoreFS behaves when the index is far away: a local drive, one
# computer next to it, and one computer whose every request to the index
# takes half a second longer (scripts/slowlink in between).
#
# A colleague's computer reached the index through a VPN that crossed the
# world twice (510 ms per request). The helper then took many minutes to
# start, said nothing meanwhile, `lorefs status` called it "not running",
# and it could not be stopped. This script keeps that from coming back.
#
# Usage: scripts/slowlink-test.sh [--keep | --cleanup-only]   (about 3 minutes)
#   DELAY=255ms   time added in each direction (default: 510 ms per request)
set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LORE="$ROOT/bin/lorefs"; JFS="$ROOT/.tools/juicefs"
T="${LOREFS_SLOWLINK_DIR:-/tmp/lorefs-slowlink}"
REDIS_NAME=lorefs-slowlink-redis; REDIS_PORT=16381; PROXY_PORT=16391
REDIS_PW=slowlink-password-not-a-real-secret
DELAY="${DELAY:-255ms}"
KEEP=0; CLEAN_ONLY=0
case "${1:-}" in
  "") ;;
  --keep) KEEP=1 ;;
  --cleanup-only) CLEAN_ONLY=1 ;;
  *) echo "usage: $0 [--keep | --cleanup-only]" >&2; exit 2 ;;
esac

"$ROOT/scripts/get-juicefs.sh" "$ROOT/.tools" >/dev/null || { echo "could not get JuiceFS"; exit 1; }
[ -x "$LORE" ] || { echo "build first: make build"; exit 1; }
(cd "$ROOT" && go build -o bin/slowlink ./scripts/slowlink) || { echo "could not build scripts/slowlink"; exit 1; }
export LOREFS_JUICEFS="$JFS" LOREFS_NO_SYSTEMD=1 LOREFS_NO_NOTIFY=1

PASS=0; FAIL=0; FAILED=()
ok()  { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
within() { local s="$1"; shift; local end=$((SECONDS+s)); until "$@" >/dev/null 2>&1; do [ $SECONDS -ge $end ] && return 1; sleep 0.2; done; }
as() { local m="$1"; shift; LOREFS_CONFIG_DIR="$T/$m/config" LOREFS_STATE_DIR="$T/$m/state" "$@"; }
mounted() { grep -q " $T/$1/Lore fuse.juicefs " /proc/self/mounts; }
start_helper() { (as "$1" setsid "$LORE" helper run >>"$T/$1/helper.log" 2>&1 & echo $! >"$T/$1/helper.pid"); sleep 0.3; }
helper_pid() { pgrep -f "^$LORE helper run" | while read -r p; do grep -qz "LOREFS_STATE_DIR=$T/$1/state" "/proc/$p/environ" 2>/dev/null && echo "$p"; done | head -1; }
versions() { as "$1" "$LORE" history --json "$2" 2>/dev/null | jq '.versions | length' 2>/dev/null || echo 0; }

cleanup() {
  pkill -f "^$LORE helper run" 2>/dev/null
  for m in near far; do "$JFS" umount "$T/$m/Lore" >/dev/null 2>&1 || fusermount3 -uz "$T/$m/Lore" 2>/dev/null; done
  pkill -f "$ROOT/bin/slowlink -listen 127.0.0.1:$PROXY_PORT" 2>/dev/null
  docker rm -f "$REDIS_NAME" >/dev/null 2>&1
  chmod -R u+rwx "$T" 2>/dev/null; rm -rf "$T"
}
trap 'echo; echo interrupted; cleanup; exit 130' INT TERM
cleanup 2>/dev/null
[ "$CLEAN_ONLY" = 1 ] && { echo "cleaned up $T"; exit 0; }
mkdir -p "$T/store" "$T/near" "$T/far"

section "A drive, a computer next to it, and a computer far away ($DELAY each way)"
docker run -d --name "$REDIS_NAME" -p "127.0.0.1:$REDIS_PORT:6379" redis:8-alpine \
  redis-server --requirepass "$REDIS_PW" --maxmemory-policy noeviction --appendonly no >/dev/null || { echo "docker failed"; exit 1; }
within 20 docker exec "$REDIS_NAME" redis-cli -a "$REDIS_PW" --no-auth-warning ping
META_PASSWORD="$REDIS_PW" "$JFS" format --storage file --bucket "$T/store/" --trash-days 7 "redis://127.0.0.1:$REDIS_PORT/1" slow >"$T/format.log" 2>&1 || { cat "$T/format.log"; cleanup; exit 1; }
META_PASSWORD="$REDIS_PW" "$JFS" config "redis://127.0.0.1:$REDIS_PORT/1" --changelog --changelog-max-age 24h --changelog-max-lines 100000 >>"$T/format.log" 2>&1
(setsid "$ROOT/bin/slowlink" -listen "127.0.0.1:$PROXY_PORT" -to "127.0.0.1:$REDIS_PORT" -delay "$DELAY" >"$T/proxy.log" 2>&1 &)
META_PASSWORD="$REDIS_PW" "$LORE" make-token --host 127.0.0.1 --port "$REDIS_PORT" --db 1 --drive slow --no-tls >"$T/token.near"
META_PASSWORD="$REDIS_PW" "$LORE" make-token --host 127.0.0.1 --port "$PROXY_PORT" --db 1 --drive slow --no-tls >"$T/token.far"
as near "$LORE" join --token-file "$T/token.near" --name near --folder "$T/near/Lore" --cache-dir "$T/near/cache" >/dev/null 2>&1
as near "$LORE" mount pre 2>/dev/null; (as near setsid "$LORE" mount run >"$T/near/mount.log" 2>&1 &)
within 30 mounted near || { bad "the near computer mounts the drive" "$(tail -3 "$T/near/mount.log")"; cleanup; exit 1; }
N="$T/near/Lore"; F="$T/far/Lore"
for a in $(seq 1 10); do for b in $(seq 1 5); do for c in $(seq 1 5); do mkdir -p "$N/area$a/topic$b/part$c"; done; done; done
find "$N" -mindepth 3 -type d | head -60 | while read -r d; do echo note >"$d/note.md"; done
FOLDERS=$(find "$N" -type d -not -path '*/.lorefs*' | wc -l)
# The near computer records its own files, as every real computer does.
: >"$T/near/helper.log"; start_helper near
within 60 eval '[ "$(as near "$LORE" log --json -n 500 2>/dev/null | jq "[.[] | select(.op == \"save\" and .version)] | length")" -ge 60 ]' \
  && ok "a drive with $FOLDERS folders and 60 notes, each with its version" || bad "the near computer records its 60 notes" "$(tail -2 "$T/near/helper.log")"

t0=$SECONDS
as far "$LORE" join --token-file "$T/token.far" --name far --folder "$F" --cache-dir "$T/far/cache" >"$T/far/join.log" 2>&1 \
  && ok "the far computer joins ($((SECONDS-t0)) s)" || { bad "the far computer joins" "$(tail -3 "$T/far/join.log")"; cleanup; exit 1; }
as far "$LORE" mount pre 2>/dev/null; (as far setsid "$LORE" mount run >"$T/far/mount.log" 2>&1 &)
within 60 mounted far && ok "it mounts the drive ($((SECONDS-t0)) s after starting to join)" || { bad "the far computer mounts the drive" "$(tail -3 "$T/far/mount.log")"; cleanup; exit 1; }
rtt=$(as far "$LORE" status --json 2>/dev/null | jq -r '.checks[] | select(.id == "index") | .detail')
echo "$rtt" | grep -q "slow" && ok "lorefs status there says the index is slow (${rtt#reachable, but })" || bad "lorefs status there says the index is slow" "$rtt"
as far "$LORE" status 2>/dev/null | grep -q "VPN" && ok "and mentions a VPN as a possible cause" || bad "status mentions a VPN as a possible cause"

section "Starting the helper on the far computer"
: >"$T/far/helper.log"; t0=$SECONDS
start_helper far
echo "saved while the helper was starting" >"$F/area3/topic2/part4/early.md"
sleep 4
js=$(as far "$LORE" status --json 2>/dev/null | jq -c '.checks[] | select(.id == "history")')
echo "$js" | jq -e '.level == "ok" and (.detail | test("recording versions|starting"))' >/dev/null \
  && ok "lorefs status says it is recording (or starting), not that it is not running" || bad "lorefs status says the helper is recording" "$js"
if within 60 grep -q "recording versions" "$T/far/helper.log"; then
  took=$((SECONDS-t0)); ok "it is recording versions after $took s with $FOLDERS folders on the drive (it took 290 s when it had to look at every folder)"
else bad "it is recording versions within 60 s" "$(tail -2 "$T/far/helper.log")"; fi
within 20 grep -q "following the drive's change log" "$T/far/helper.log" && ok "it follows the drive's change log" || bad "it follows the drive's change log" "$(cat "$T/far/helper.log")"
tr=$SECONDS
within 150 eval '[ "$(versions far area3/topic2/part4/early.md)" = 1 ]' \
  && ok "a file saved while it was starting has its version ($((SECONDS-tr)) s after the helper was ready)" || bad "a file saved while it was starting has its version within 150 s" "$(versions far area3/topic2/part4/early.md)"
tl=$SECONDS
echo "saved after it was ready" >"$F/area7/topic1/part2/later.md"
within 120 eval '[ "$(versions far area7/topic1/part2/later.md)" = 1 ]' \
  && ok "a file saved afterwards has its version ($((SECONDS-tl)) s, the connection adds half a second to each request)" || bad "a file saved afterwards has its version"
far_versions() { as far "$LORE" log --json -n 2000 2>/dev/null | jq '[.[] | select(.m == "far" and .op == "save" and .version)] | length'; }
within 60 eval '[ "$(far_versions)" -ge 2 ]'; sleep 5; n=$(far_versions)   # the activity log is written a few seconds behind
[ "$n" = 2 ] && ok "it made versions of its own two files only, not of the 60 the other computer had recorded" || bad "a new computer leaves files from before it joined alone" "$n versions made by far: $(as far "$LORE" log --json -n 2000 2>/dev/null | jq -r '[.[] | select(.m == "far" and .op == "save" and .version) | .path + " " + .t] | join("; ")')"

section "Stopping it while it is starting"
p=$(helper_pid far); kill "$p" 2>/dev/null; within 20 eval "! kill -0 $p 2>/dev/null"
: >"$T/far/helper.log"; sleep 12   # let the kernel forget the folders, as after a pause
start_helper far; sleep 0.7
p=$(helper_pid far)
if [ -n "$p" ]; then
  t0=$(date +%s.%N); kill -TERM "$p"
  if within 15 eval "! kill -0 $p 2>/dev/null"; then
    ok "asked to stop right after starting, it stops ($(awk -v s="$t0" -v e="$(date +%s.%N)" 'BEGIN{printf "%.1f", e-s}') s; it used to need killing after 90 s)"
  else bad "asked to stop right after starting, it stops within 15 s"; kill -9 "$p" 2>/dev/null; fi
else bad "the helper was running when asked to stop" "$(tail -1 "$T/far/helper.log")"; fi

section "Stopping it while it catches up"
# On start the helper walks the files changed since it last looked. With half a second
# per request that walk takes a while; stopping must not wait for it.
echo "changed while the helper was off" >"$F/area5/topic3/part1/off.md"
: >"$T/far/helper.log"; start_helper far; sleep 6
p=$(helper_pid far)
if [ -n "$p" ] && ! grep -q "catch-up:" "$T/far/helper.log"; then
  t0=$(date +%s.%N); kill -TERM "$p"
  if within 15 eval "! kill -0 $p 2>/dev/null"; then
    ok "asked to stop in the middle of its catch-up walk, it stops ($(awk -v s="$t0" -v e="$(date +%s.%N)" 'BEGIN{printf "%.1f", e-s}') s)"
  else bad "asked to stop in the middle of its catch-up walk, it stops within 15 s"; kill -9 "$p" 2>/dev/null; fi
else
  echo "  (the catch-up walk had already finished after 6 s, so there was nothing to interrupt)"; [ -n "$p" ] && kill -TERM "$p" 2>/dev/null; sleep 3
fi

section "Stopping it while it removes old versions"
: >"$T/far/helper.log"
( export LOREFS_PRUNE_FIRST=2s; start_helper far )
within 120 grep -q "recording versions" "$T/far/helper.log"; sleep 4
p=$(helper_pid far)
if [ -n "$p" ] && ! grep -q "removed .* old version" "$T/far/helper.log"; then
  t0=$(date +%s.%N); kill -TERM "$p"
  if within 30 eval "! kill -0 $p 2>/dev/null"; then
    ok "asked to stop while removing old versions, it stops ($(awk -v s="$t0" -v e="$(date +%s.%N)" 'BEGIN{printf "%.1f", e-s}') s; it took 60 s before that could be interrupted)"
  else bad "asked to stop while removing old versions, it stops within 30 s"; kill -9 "$p" 2>/dev/null; fi
else bad "the helper was removing old versions when asked to stop" "pid '$p': $(tail -1 "$T/far/helper.log")"; fi

printf '\n\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
for f in "${FAILED[@]:-}"; do [ -n "$f" ] && echo "  failed: $f"; done
if [ "$KEEP" = 1 ]; then echo "kept: $T (near: $N, far: $F). Clean up by running this script again or: docker rm -f $REDIS_NAME"; else cleanup; fi
[ "$FAIL" = 0 ]
