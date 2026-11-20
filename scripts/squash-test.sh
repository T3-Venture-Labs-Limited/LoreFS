#!/usr/bin/env bash
# Two users with different numeric ids work on one drive. One mounts it on
# this computer (as the invoking user); the other mounts it inside a
# container as user 2000 (FUSE passed through). With every mount acting as
# the drive's owner (--all-squash) each side can create and change the
# other's files; without the option the second user cannot, which proves
# that the test notices the difference. Then the side effects of the option
# are checked from the second user: sandboxes, version recording, the trash,
# cp -p, rsync -a and an editor's save by rename.
#
# Usage: scripts/squash-test.sh [--cleanup-only]
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LORE="$ROOT/bin/lorefs"
T="${LOREFS_QTEST_DIR:-/tmp/lorefs-qtest}"
IDX=lorefs-qtest-index; IDX_PORT=16430; IDX_PW=qtest-index-password-xyz
CT=lorefs-qtest-box
OTHER_UID=2000

"$ROOT/scripts/get-juicefs.sh" "$ROOT/.tools" >/dev/null || { echo "could not get JuiceFS"; exit 1; }
JFS="$ROOT/.tools/juicefs"
export LOREFS_JUICEFS="$JFS" LOREFS_NO_SYSTEMD=1 LOREFS_NO_NOTIFY=1
export LOREFS_QUIET=300ms LOREFS_MAX_WAIT=20s LOREFS_JOURNAL_FLUSH=300ms LOREFS_FOLLOW=2s LOREFS_HEALTH=2s

PASS=0; FAIL=0; FAILED=()
ok()  { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
within() { local s="$1"; shift; local end=$((SECONDS+s)); until "$@" >/dev/null 2>&1; do [ $SECONDS -ge $end ] && return 1; sleep 0.3; done; }

H="$T/host"                # this computer's user
HL="$H/Lore"
as_host() { LOREFS_CONFIG_DIR="$H/config" LOREFS_STATE_DIR="$H/state" LOREFS_DATA_DIR="$H/data" "$@"; }
# Run something as the second user inside the container, with its own settings.
box() { docker exec -u "$OTHER_UID:$OTHER_UID" -e HOME=/home/other -e LOREFS_CONFIG_DIR=/home/other/config -e LOREFS_STATE_DIR=/home/other/state \
  -e LOREFS_JUICEFS=/opt/juicefs -e LOREFS_NO_SYSTEMD=1 -e LOREFS_NO_NOTIFY=1 -e LOREFS_QUIET=300ms -e LOREFS_JOURNAL_FLUSH=300ms \
  -e LOREFS_FOLLOW=2s -e LOREFS_HEALTH=2s "$CT" "$@"; }
boxsh() { box sh -c "$1"; }
BL=/home/other/Lore

stop_host() { "$JFS" umount "$HL" >/dev/null 2>&1 || fusermount3 -uz "$HL" 2>/dev/null; sleep 1; }
cleanup() {
  [ -f "$H/helper.pid" ] && kill "$(cat "$H/helper.pid")" 2>/dev/null
  stop_host 2>/dev/null
  # Files made by the container user are not the host user's to remove.
  docker exec "$CT" sh -c "rm -rf $T/store/*" >/dev/null 2>&1
  docker rm -f "$CT" "$IDX" >/dev/null 2>&1
  pkill -f "$JFS mount" 2>/dev/null
  chmod -R u+rwx "$T" 2>/dev/null; rm -rf "$T" 2>/dev/null
  [ -e "$T" ] && docker run --rm -v "$T:$T" alpine:latest sh -c "rm -rf $T/*" >/dev/null 2>&1
  rm -rf "$T"
}
trap 'echo; echo "interrupted"; cleanup; exit 130' INT TERM
cleanup 2>/dev/null
[ "${1:-}" = --cleanup-only ] && { echo "cleaned up $T"; exit 0; }
mkdir -p "$H" "$T/store"; chmod 777 "$T/store"

section "Setting up: one drive, this user (uid $(id -u)) and a container user (uid $OTHER_UID)"
docker run -d --name "$IDX" --network host redis:8-alpine redis-server --port "$IDX_PORT" --requirepass "$IDX_PW" --appendonly no >/dev/null || { echo "docker failed"; exit 1; }
within 20 docker exec "$IDX" redis-cli -p "$IDX_PORT" -a "$IDX_PW" --no-auth-warning ping
META_PASSWORD="$IDX_PW" "$JFS" format --storage file --bucket "$T/store/" --trash-days 7 "redis://127.0.0.1:$IDX_PORT/1" qtest >/dev/null 2>&1 || { echo "format failed"; exit 1; }
META_PASSWORD="$IDX_PW" "$JFS" config "redis://127.0.0.1:$IDX_PORT/1" --changelog --changelog-max-age 24h --changelog-max-lines 100000 >/dev/null 2>&1
META_PASSWORD="$IDX_PW" "$LORE" make-token --host 127.0.0.1 --port "$IDX_PORT" --db 1 --drive qtest --no-tls >"$T/token"
as_host "$LORE" join --token-file "$T/token" --name hostuser --folder "$HL" --cache-dir "$H/cache" >"$T/join-host.log" 2>&1 || { echo "join failed"; cat "$T/join-host.log"; exit 1; }
as_host "$LORE" mount pre; (umask 000; as_host "$LORE" mount run >"$H/mount.log" 2>&1) &
within 40 test -e "$HL/.config" && ok "this user mounts the drive" || { bad "this user mounts the drive" "$(tail -3 "$H/mount.log")"; cleanup; exit 1; }
as_host "$LORE" helper run >>"$H/helper.log" 2>&1 & echo $! >"$H/helper.pid"

# A container with FUSE, the lorefs and juicefs programs, running as user 2000.
docker run -d --name "$CT" --network host --device /dev/fuse --cap-add SYS_ADMIN --security-opt apparmor:unconfined \
  -v "$ROOT/bin:/opt/lorefs:ro" -v "$JFS:/opt/juicefs:ro" -v "$T/store:$T/store" --user 0:0 alpine:latest sh -c \
  "apk add --no-cache fuse3 coreutils >/dev/null 2>&1; adduser -D -u $OTHER_UID -h /home/other other; chmod u+s \$(command -v fusermount3); chmod 666 /dev/fuse; sleep infinity" >/dev/null 2>&1 || { bad "starting the container"; cleanup; exit 1; }
within 120 docker exec "$CT" sh -c 'command -v fusermount3 && [ -d /home/other ] && [ "$(readlink /bin/cp)" = coreutils ]' || { bad "the container has FUSE tools" "$(docker logs "$CT" 2>&1 | tail -3)"; cleanup; exit 1; }
box test "$(id -u)" = "$OTHER_UID" 2>/dev/null; [ "$(box id -u)" = "$OTHER_UID" ] && ok "the container user's number ($OTHER_UID) differs from this user's ($(id -u))" || bad "the container user has another number"
docker cp "$T/token" "$CT:/tmp/token" && docker exec "$CT" chmod 644 /tmp/token
box /opt/lorefs/lorefs join --token-file /tmp/token --name boxuser --folder "$BL" --cache-dir /home/other/cache >"$T/join-box.log" 2>&1 \
  && ok "the second user joins (and is not asked about user numbers)" || { bad "the second user joins" "$(cat "$T/join-box.log")"; cleanup; exit 1; }
grep -qi "user number" "$T/join-box.log" && bad "join no longer warns about user numbers" || ok "join no longer warns about user numbers"
box /opt/lorefs/lorefs mount pre
docker exec -d -u "$OTHER_UID:$OTHER_UID" -e HOME=/home/other -e LOREFS_CONFIG_DIR=/home/other/config -e LOREFS_STATE_DIR=/home/other/state \
  -e LOREFS_JUICEFS=/opt/juicefs -e LOREFS_NO_SYSTEMD=1 "$CT" sh -c 'umask 000; /opt/lorefs/lorefs mount run >/home/other/mount.log 2>&1'
within 60 box test -e "$BL/.config" && ok "the second user mounts the drive in the container" || { bad "the second user mounts the drive" "$(box cat /home/other/mount.log | tail -3)"; cleanup; exit 1; }
docker exec -d -u "$OTHER_UID:$OTHER_UID" -e HOME=/home/other -e LOREFS_CONFIG_DIR=/home/other/config -e LOREFS_STATE_DIR=/home/other/state \
  -e LOREFS_JUICEFS=/opt/juicefs -e LOREFS_NO_SYSTEMD=1 -e LOREFS_QUIET=300ms -e LOREFS_JOURNAL_FLUSH=300ms -e LOREFS_FOLLOW=2s "$CT" sh -c '/opt/lorefs/lorefs helper run >>/home/other/helper.log 2>&1'

section "Each side changes the other's files"
mkdir -p "$HL/work"; echo "made by the host user" >"$HL/work/host.txt"
within 10 boxsh "[ -f $BL/work/host.txt ]" && ok "the container user sees the host user's file" || bad "the container user sees the host user's file"
boxsh "echo 'edited by the container user' >> $BL/work/host.txt" 2>"$T/err1" && within 10 grep -q "container user" "$HL/work/host.txt" && ok "the container user can edit the host user's file" || bad "the container user can edit the host user's file" "$(cat "$T/err1")"
boxsh "echo 'made by the container user' > $BL/work/box.txt" && within 10 test -f "$HL/work/box.txt" && echo "edited by the host user" >>"$HL/work/box.txt" && grep -q "host user" "$HL/work/box.txt" && ok "the host user can edit the container user's file" || bad "the host user can edit the container user's file"
boxsh "mkdir $BL/work/boxdir && echo x > $BL/work/boxdir/f" && within 10 test -f "$HL/work/boxdir/f" && rm -r "$HL/work/boxdir" && ok "each side can delete what the other made" || bad "each side can delete what the other made"
boxsh "rm $BL/work/host.txt" && ok "the container user can delete the host user's file" || bad "the container user can delete the host user's file"
echo "back" >"$HL/work/host.txt"; within 10 boxsh "[ -f $BL/work/host.txt ]"
owner=$(boxsh "stat -c %u $BL/work/host.txt"); echo "files show the drive owner's number ($owner), not the local one"

section "Side effects of the option, seen from the second user"
echo "original text" >"$HL/work/plan.md"; within 10 boxsh "[ -f $BL/work/plan.md ]"
within 20 eval '[ "$(as_host "$LORE" history --json work/plan.md 2>/dev/null | jq ".versions | length")" = 1 ]' && ok "the host user's recorder gives the new file a version" || bad "the host user's recorder gives the new file a version"
boxsh "echo 'a second line' >> $BL/work/plan.md"
within 30 eval '[ "$(as_host "$LORE" history --json work/plan.md 2>/dev/null | jq ".versions | length")" = 2 ]' && ok "an edit by the second user gets a version too" || bad "an edit by the second user gets a version too" "$(as_host "$LORE" history --json work/plan.md 2>&1 | head -3)"
box /opt/lorefs/lorefs history --json work/plan.md 2>/dev/null | jq -e '.versions | length == 2' >/dev/null && ok "the second user can read the history" || bad "the second user can read the history"
sbp=$(box /opt/lorefs/lorefs sandbox create work box1 --json 2>"$T/sb.err" | jq -r .path)
if [ -n "$sbp" ] && boxsh "echo 'sandbox edit' >> '$sbp/plan.md'" && box /opt/lorefs/lorefs sandbox apply box1 --yes >"$T/sb.out" 2>&1 && within 10 grep -q "sandbox edit" "$HL/work/plan.md"; then
  ok "the second user's sandbox can be made, edited and applied"; else bad "the second user's sandbox can be made, edited and applied" "$(cat "$T/sb.err" "$T/sb.out" 2>/dev/null)"; fi
boxsh "echo temp > $BL/work/gone.md"; within 10 test -f "$HL/work/gone.md"; sleep 2; boxsh "rm $BL/work/gone.md"; sleep 2
box /opt/lorefs/lorefs trash >"$T/trash.out" 2>&1; grep -q "gone.md" "$T/trash.out" && box /opt/lorefs/lorefs trash restore gone.md --to work >/dev/null 2>&1 && boxsh "[ -f $BL/work/gone.md ]" \
  && ok "the second user can delete a file and bring it back from the trash" || bad "the second user's trash restore" "$(cat "$T/trash.out")"
boxsh "echo content > /home/other/src.txt; touch -d '2021-03-04 05:06:07' /home/other/src.txt; cp -p /home/other/src.txt $BL/work/cp-p.txt" 2>"$T/cpp.err" \
  && boxsh "[ \$(stat -c %Y /home/other/src.txt) = \$(stat -c %Y $BL/work/cp-p.txt) ]" && ok "cp -p keeps the date and does not fail" || bad "cp -p keeps the date and does not fail" "$(cat "$T/cpp.err")"
echo "  (busybox cp -p, which also tries to set the owner, prints: $(boxsh "busybox cp -p /home/other/src.txt $BL/work/cp-busybox.txt 2>&1 | head -1"))"
if boxsh "command -v rsync" >/dev/null 2>&1 || docker exec "$CT" apk add --no-cache rsync >/dev/null 2>&1; then
  boxsh "mkdir -p /home/other/rs/sub && echo r > /home/other/rs/sub/a.txt && rsync -a /home/other/rs/ $BL/work/rs/" 2>"$T/rs.err" \
    && boxsh "[ -f $BL/work/rs/sub/a.txt ]" && ok "rsync -a works (it cannot set another owner, and says nothing)" || bad "rsync -a works" "$(cat "$T/rs.err")"
else echo "  (rsync could not be installed in the container; skipped)"; fi
boxsh "cd $BL/work && echo 'new text' > .plan.md.tmp && mv .plan.md.tmp plan.md" 2>"$T/mv.err" && grep -q "new text" "$HL/work/plan.md" && ok "an editor's save by rename works for the second user" || bad "an editor's save by rename works for the second user" "$(cat "$T/mv.err")"
boxsh "chmod 600 $BL/work/plan.md && chmod 644 $BL/work/plan.md" 2>"$T/chmod.err" && ok "the second user can change the mode of the host user's file" || bad "chmod" "$(cat "$T/chmod.err")"
boxsh "chown $OTHER_UID $BL/work/plan.md" 2>"$T/chown.err"; echo "  (chown by the second user: $(boxsh "chown 1234 $BL/work/plan.md 2>&1; echo exit \$?" | tr '\n' ' '))"

section "Without the option the second user cannot change the other's files (the test notices)"
box fusermount3 -uz "$BL" >/dev/null 2>&1; sleep 1; docker exec "$CT" sh -c 'pkill -f "juicefs mount"; sleep 1; fusermount3 -uz /home/other/Lore' >/dev/null 2>&1
within 30 boxsh "[ ! -e $BL/.config ]" || bad "the first mount stopped"
sleep 1
# Mount again by hand, without --all-squash, as the second user.
docker exec -d -u "$OTHER_UID:$OTHER_UID" -e HOME=/home/other -e META_PASSWORD="$IDX_PW" "$CT" sh -c \
  "/opt/juicefs mount redis://127.0.0.1:$IDX_PORT/1 $BL --cache-dir /home/other/cache2 --no-usage-report --no-syslog >/home/other/mount2.log 2>&1"
within 60 box test -e "$BL/.config" && ! docker exec "$CT" sh -c 'ps aux | grep "[j]uicefs mount" | grep -q all-squash' \
  && ok "the second user mounts again, this time without the option" || bad "mounts without the option" "$(box tail -3 /home/other/mount2.log)"
echo "protected from others" >"$HL/work/host-only.txt"; chmod 644 "$HL/work/host-only.txt"; sleep 3
boxsh "echo 'try' >> $BL/work/host-only.txt" 2>"$T/noopt.err" && bad "without the option the second user is refused" "the write succeeded" || ok "without the option the second user cannot change the host user's file ($(head -c 60 "$T/noopt.err" | tr '\n' ' '))"

echo
printf '\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -gt 0 ] && { printf '  failed: %s\n' "${FAILED[@]}"; }
cleanup
[ "$FAIL" = 0 ]
