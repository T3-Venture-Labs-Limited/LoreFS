#!/usr/bin/env bash
# Compatibility with the earlier program (the private build named `lore`):
# a computer that was joined with it keeps working when `lorefs` is installed
# beside it, without joining again, and a `lorefs1_` join code made by the
# earlier program joins a new computer. Needs the earlier program; set
# LOREFS_EARLIER_LORE to its path (default ~/development/lorefs/bin/lore).
#
# Usage: scripts/compat-test.sh [--cleanup-only]
set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EARLIER="${LOREFS_EARLIER_LORE:-$HOME/development/lorefs/bin/lore}"
NEW="$ROOT/bin/lorefs"
T="${LOREFS_COMPAT_DIR:-/tmp/lorefs-compat}"
IDX=lorefs-compat-index; PORT=16495; PW=compat-index-password-xyz
"$ROOT/scripts/get-juicefs.sh" "$ROOT/.tools" >/dev/null || { echo "could not get JuiceFS"; exit 1; }
JFS="$ROOT/.tools/juicefs"
export LOREFS_NO_SYSTEMD=1 LOREFS_NO_NOTIFY=1
PASS=0; FAIL=0; FAILED=()
ok()  { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
within() { local s="$1"; shift; local end=$((SECONDS+s)); until "$@" >/dev/null 2>&1; do [ $SECONDS -ge $end ] && return 1; sleep 0.3; done; }

cleanup() {
  for h in a c; do fusermount3 -uz "$T/$h/Lore" 2>/dev/null; done
  sleep 1; pkill -f "juicefs mount redis://127.0.0.1:$PORT" 2>/dev/null
  docker rm -f "$IDX" >/dev/null 2>&1
  chmod -R u+rwx "$T" 2>/dev/null; rm -rf "$T"
}
trap 'cleanup; exit 130' INT TERM
cleanup 2>/dev/null
[ "${1:-}" = --cleanup-only ] && { echo "cleaned up $T"; exit 0; }
[ -x "$EARLIER" ] || { echo "The earlier program is not at $EARLIER; set LOREFS_EARLIER_LORE. Skipped."; exit 77; }
mkdir -p "$T/a" "$T/c" "$T/store"

section "A computer joined with the earlier program"
docker run -d --name "$IDX" -p "127.0.0.1:$PORT:6379" redis:8-alpine redis-server --requirepass "$PW" --appendonly no >/dev/null || exit 1
within 20 docker exec "$IDX" redis-cli -a "$PW" --no-auth-warning ping
META_PASSWORD="$PW" "$JFS" format --storage file --bucket "$T/store/" --trash-days 7 "redis://127.0.0.1:$PORT/1" compat >/dev/null 2>&1 || { echo "format failed"; exit 1; }
code=$(META_PASSWORD="$PW" "$EARLIER" make-token --host 127.0.0.1 --port "$PORT" --db 1 --drive compat --no-tls)
echo "$code" | grep -q '^lorefs1_' && ok "the earlier program makes a lorefs1_ join code" || bad "the earlier program makes a lorefs1_ join code"
echo "$code" >"$T/code1"
HOME="$T/a" LOREFS_JUICEFS="$JFS" "$EARLIER" join --token-file "$T/code1" --name earlier --folder "$T/a/Lore" --cache-dir "$T/a/cache" >"$T/join-a.log" 2>&1 \
  && ok "it joins computer A" || { bad "the earlier program joins computer A" "$(tail -3 "$T/join-a.log")"; cleanup; exit 1; }
HOME="$T/a" LOREFS_JUICEFS="$JFS" "$EARLIER" mount pre >/dev/null 2>&1
(HOME="$T/a" LOREFS_JUICEFS="$JFS" setsid "$EARLIER" mount run >"$T/mount-a.log" 2>&1 &)
within 40 test -e "$T/a/Lore/.config" && ok "computer A's drive is connected by the earlier program" || { bad "computer A's drive is connected" "$(tail -3 "$T/mount-a.log")"; cleanup; exit 1; }
echo "written by the earlier program" >"$T/a/Lore/old.txt"

section "lorefs installed beside it"
mkdir -p "$T/a/.local/bin"; cp "$NEW" "$T/a/.local/bin/lorefs"
before=$(sha256sum "$T/a/.config/lorefs/config.toml" | cut -d' ' -f1)
out=$(HOME="$T/a" LOREFS_JUICEFS="$JFS" "$T/a/.local/bin/lorefs" status 2>&1); rc=$?
echo "$out" | grep -q "mounted at $T/a/Lore" && ok "lorefs status shows the drive connected, without joining again" || bad "lorefs status shows the drive connected" "exit $rc: $out"
echo "$out" | grep -q "Index  *reachable" && ok "and reaches the index with the old settings file" || bad "and reaches the index"
[ "$(sha256sum "$T/a/.config/lorefs/config.toml" | cut -d' ' -f1)" = "$before" ] && ok "status leaves the settings file as the earlier program wrote it" || bad "status leaves the settings file alone"
HOME="$T/a" LOREFS_JUICEFS="$JFS" "$T/a/.local/bin/lorefs" invite --quiet >"$T/code2" 2>/dev/null; grep -q '^lorefs2_' "$T/code2" && ok "lorefs invite prints a lorefs2_ code from the old settings" || bad "lorefs invite prints a lorefs2_ code" "$(cat "$T/code2")"
HOME="$T/a" LOREFS_JUICEFS="$JFS" "$EARLIER" status >"$T/earlier-status.out" 2>&1; grep -q "mounted" "$T/earlier-status.out" && ok "the earlier program still reads the same settings (the way back)" || bad "the earlier program still reads the settings" "$(head -3 "$T/earlier-status.out")"

section "A lorefs1_ code from the earlier program joins a new computer with lorefs"
HOME="$T/c" LOREFS_JUICEFS="$JFS" "$NEW" join --token-file "$T/code1" --name third --folder "$T/c/Lore" --cache-dir "$T/c/cache" >"$T/join-c.log" 2>&1 \
  && ok "lorefs join accepts the earlier program's code" || bad "lorefs join accepts the earlier program's code" "$(tail -3 "$T/join-c.log")"
HOME="$T/c" LOREFS_JUICEFS="$JFS" "$NEW" mount pre >/dev/null 2>&1
(HOME="$T/c" LOREFS_JUICEFS="$JFS" setsid "$NEW" mount run >"$T/mount-c.log" 2>&1 &)
within 40 test -e "$T/c/Lore/.config" && ok "the new computer mounts the drive" || bad "the new computer mounts the drive" "$(tail -3 "$T/mount-c.log")"
within 10 grep -q "earlier program" "$T/c/Lore/old.txt" && ok "and reads what the earlier program wrote" || bad "and reads what the earlier program wrote"
echo "written by lorefs" >"$T/c/Lore/new.txt"
within 10 grep -q "written by lorefs" "$T/a/Lore/new.txt" && ok "and what it writes reaches the computer that still runs the earlier program" || bad "what lorefs writes reaches the earlier computer"

echo
printf '\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -gt 0 ] && { printf '  failed: %s\n' "${FAILED[@]}"; }
cleanup
[ "$FAIL" = 0 ]
