#!/usr/bin/env bash
# Checks version recording from the drive's change log: that a helper that
# was stopped catches up from where it left off, notices when the log no
# longer holds what it missed, still works when the drive has no change
# log, and records a bulk copy exactly once per file.
#
# Usage: scripts/recorder-test.sh [--cleanup-only]
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LORE="$ROOT/bin/lorefs"
T="${LOREFS_RTEST_DIR:-/tmp/lorefs-rtest}"
IDX=lorefs-rtest-index; IDX_PORT=16450; IDX_PW=rtest-index-password-xyz
META="redis://127.0.0.1:$IDX_PORT/1"

"$ROOT/scripts/get-juicefs.sh" "$ROOT/.tools" >/dev/null || { echo "could not get JuiceFS"; exit 1; }
JFS="$ROOT/.tools/juicefs"
export LOREFS_JUICEFS="$JFS" LOREFS_NO_SYSTEMD=1 LOREFS_NO_NOTIFY=1
export LOREFS_QUIET=300ms LOREFS_MAX_WAIT=20s LOREFS_JOURNAL_FLUSH=300ms LOREFS_FOLLOW=2s LOREFS_HEALTH=2s

PASS=0; FAIL=0; FAILED=()
ok()  { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
within() { local s="$1"; shift; local end=$((SECONDS+s)); until "$@" >/dev/null 2>&1; do [ $SECONDS -ge $end ] && return 1; sleep 0.3; done; }

A="$T/alpha/Lore"
lore() { LOREFS_CONFIG_DIR="$T/alpha/config" LOREFS_STATE_DIR="$T/alpha/state" "$LORE" "$@"; }
start_helper() { LOREFS_CONFIG_DIR="$T/alpha/config" LOREFS_STATE_DIR="$T/alpha/state" "$@" "$LORE" helper run >>"$T/alpha/helper.log" 2>&1 & echo $! >"$T/alpha/helper.pid"; }
stop_helper() { [ -f "$T/alpha/helper.pid" ] && kill "$(cat "$T/alpha/helper.pid")" 2>/dev/null; rm -f "$T/alpha/helper.pid"; sleep 1; }
versions() { lore history --json "$1" 2>/dev/null | jq '.versions | length' 2>/dev/null || echo 0; }
idx() { docker exec "$IDX" redis-cli -a "$IDX_PW" --no-auth-warning -n 1 "$@" 2>/dev/null; }

cleanup() {
  stop_helper 2>/dev/null
  "$JFS" umount "$A" >/dev/null 2>&1 || fusermount3 -uz "$A" 2>/dev/null
  pkill -f "$JFS mount" 2>/dev/null
  docker rm -f "$IDX" >/dev/null 2>&1
  chmod -R u+rwx "$T" 2>/dev/null; rm -rf "$T"
}
trap 'echo; echo "interrupted"; cleanup; exit 130' INT TERM
cleanup 2>/dev/null
[ "${1:-}" = --cleanup-only ] && { echo "cleaned up $T"; exit 0; }
mkdir -p "$T/store" "$T/alpha"

section "A drive with a change log, one computer"
docker run -d --name "$IDX" -p "127.0.0.1:$IDX_PORT:6379" redis:8-alpine redis-server --requirepass "$IDX_PW" --appendonly no >/dev/null || { echo "docker failed"; exit 1; }
within 20 docker exec "$IDX" redis-cli -a "$IDX_PW" --no-auth-warning ping
META_PASSWORD="$IDX_PW" "$JFS" format --storage file --bucket "$T/store/" --trash-days 7 "$META" rtest >"$T/format.log" 2>&1 || { cat "$T/format.log"; exit 1; }
META_PASSWORD="$IDX_PW" "$JFS" config "$META" --changelog --changelog-max-age 24h --changelog-max-lines 100000 >>"$T/format.log" 2>&1
META_PASSWORD="$IDX_PW" "$LORE" make-token --host 127.0.0.1 --port "$IDX_PORT" --db 1 --drive rtest --no-tls >"$T/token"
lore join --token-file "$T/token" --name alpha --folder "$A" --cache-dir "$T/alpha/cache" >"$T/join.log" 2>&1 || { cat "$T/join.log"; exit 1; }
lore mount pre; lore mount run >"$T/alpha/mount.log" 2>&1 &
within 40 test -e "$A/.config" && ok "the drive mounts" || { bad "the drive mounts" "$(tail -3 "$T/alpha/mount.log")"; cleanup; exit 1; }
start_helper env
within 30 grep -q "following the drive's change log" "$T/alpha/helper.log" && ok "the helper follows the change log" || bad "the helper follows the change log" "$(cat "$T/alpha/helper.log")"
mkdir -p "$A/docs"; echo "one" >"$A/docs/a.md"
within 15 eval '[ "$(versions docs/a.md)" = 1 ]' && ok "a save gets its version from the log" || bad "a save gets its version from the log"

section "A helper that was stopped continues from where it left off"
stop_helper
for i in 1 2 3 4 5; do echo "saved while stopped $i" >"$A/docs/off$i.md"; done
echo "second" >"$A/docs/a.md"
start_helper env
within 40 eval '[ "$(versions docs/off5.md)" = 1 ] && [ "$(versions docs/a.md)" = 2 ]' \
  && ok "files saved while the helper was stopped have their versions after it starts" || bad "files saved while the helper was stopped have their versions" "off5=$(versions docs/off5.md) a=$(versions docs/a.md)"
[ -s "$T/alpha/state/changelog.json" ] && jq -e '.version > 0' "$T/alpha/state/changelog.json" >/dev/null && ok "its position in the log is kept on disk ($(jq -c . "$T/alpha/state/changelog.json"))" || bad "its position in the log is kept on disk"

section "The log no longer holds what the helper missed"
stop_helper
for i in 1 2 3; do echo "saved while stopped, then forgotten $i" >"$A/docs/gone$i.md"; done
# Cut the log as if its retention had passed: keep only the newest entry.
idx ltrim txnLog -1 -1 >/dev/null
echo "an entry that is still there" >"$A/docs/after.md"
: >"$T/alpha/helper.log"; start_helper env
within 30 grep -q "no longer there\|are gone" "$T/alpha/helper.log" && ok "the helper notices that entries are missing and asks for a catch-up" || bad "the helper notices that entries are missing" "$(cat "$T/alpha/helper.log")"
within 60 eval '[ "$(versions docs/gone3.md)" = 1 ] && [ "$(versions docs/after.md)" = 1 ]' \
  && ok "the walk records what the log could not tell it" || bad "the walk records what the log could not tell it" "gone3=$(versions docs/gone3.md) after=$(versions docs/after.md)"

section "A drive without a change log"
stop_helper
lore admin changelog off >"$T/off.out" 2>&1 && grep -q "off" "$T/off.out" && ok "lorefs admin changelog off turns it off" || bad "admin changelog off" "$(cat "$T/off.out")"
lore admin changelog status | grep -q "is off" && ok "and status says so" || bad "admin changelog status says it is off"
: >"$T/alpha/helper.log"; start_helper env LOREFS_WALK_NO_LOG=4s LOREFS_LOG_CHECK=3s
within 30 grep -q "change log is off" "$T/alpha/helper.log" && ok "the helper reports that the log is off and falls back to the walk" || bad "the helper reports that the log is off" "$(cat "$T/alpha/helper.log")"
echo "no log" >"$A/docs/nolog.md"
t0=$SECONDS
within 60 eval '[ "$(versions docs/nolog.md)" = 1 ]' && ok "a file still gets its version, from the walk, in $((SECONDS-t0)) s" || bad "a file still gets its version without the log" "$(tail -3 "$T/alpha/helper.log")"
sleep 3; out=$(lore status 2>&1); code=$?
[ $code = 1 ] && echo "$out" | grep -q "Change log" && echo "$out" | grep -q "lorefs admin changelog on" && ok "status warns about it and says how to turn it on" || bad "status warns about the missing change log" "exit $code: $out"
lore admin changelog on >/dev/null 2>&1 && ok "lorefs admin changelog on turns it back on" || bad "admin changelog on"
within 30 grep -q "following the drive's change log" "$T/alpha/helper.log" && ok "the helper notices and starts following the log again (no restart)" || bad "the helper starts following the log again" "$(tail -3 "$T/alpha/helper.log")"
echo "log again" >"$A/docs/log-again.md"
within 20 eval '[ "$(versions docs/log-again.md)" = 1 ]' && ok "and records from it" || bad "records from the log again"
stop_helper

section "A bulk copy: 2,000 files in 300 folders, eight at a time"
start_helper env
within 30 grep -q "following the drive's change log" "$T/alpha/helper.log"
sleep 2
mkdir -p "$A/bulk"
export A
t0=$SECONDS
seq 1 2000 | xargs -P 8 -I{} sh -c 'd="$A/bulk/f$(( {} % 300 ))"; mkdir -p "$d"; echo "content of file {}" > "$d/file-{}.md"'
copied=$((SECONDS-t0)); tend=$SECONDS
echo "  copying took $copied s"
within 90 eval '[ "$(lore log --json -n 5000 2>/dev/null | jq "[.[] | select(.op == \"save\" and .version and (.path | startswith(\"bulk/\")))] | length")" -ge 2000 ]' \
  && ok "every file had a version within $((SECONDS-tend)) s of the copy ending" || bad "every copied file had a version within 90 s of the copy ending" "$(lore log --json -n 5000 2>/dev/null | jq "[.[] | select(.op == \"save\" and .version and (.path | startswith(\"bulk/\")))] | length") of 2000"
sleep 5
saves=$(lore log --json -n 5000 | jq '[.[] | select(.op == "save" and .version and (.path | startswith("bulk/")))] | length')
distinct=$(lore log --json -n 5000 | jq '[.[] | select(.op == "save" and .version and (.path | startswith("bulk/"))) | .path] | unique | length')
[ "$saves" = 2000 ] && [ "$distinct" = 2000 ] && ok "exactly one version per file (2000 versions of 2000 files)" || bad "exactly one version per file" "$saves versions of $distinct files"
empty=$(find "$A/.lorefs/history/bulk" -type f -size 0 2>/dev/null | wc -l)
[ "$empty" = 0 ] && ok "no version is empty" || bad "no version is empty" "$empty empty versions"
sample=$(lore open --path bulk/f77/file-377.md 1 2>/dev/null); [ "$(cat "$sample" 2>/dev/null)" = "content of file 377" ] && ok "a version holds the file's content" || bad "a version holds the file's content"
stop_helper

echo
printf '\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -gt 0 ] && { printf '  failed: %s\n' "${FAILED[@]}"; }
cleanup
[ "$FAIL" = 0 ]
