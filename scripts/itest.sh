#!/usr/bin/env bash
# Local integration test. It builds a real JuiceFS drive on a local folder
# with Redis in Docker and mounts it twice, as two "computers" (alpha and
# beta) with separate settings, then checks the behaviour the specs
# promise. Nothing here touches the real backend or ~/.config/lorefs.
#
# Usage: scripts/itest.sh [--keep] [--fast]
#   --keep  leave the drive mounted afterwards for inspection
#   --fast  use a 64 MB file instead of 1 GB for the large-file check
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LORE="$ROOT/bin/lorefs"
T="${LOREFS_ITEST_DIR:-/tmp/lorefs-itest}"
REDIS_NAME=lorefs-itest-redis
REDIS_PORT=16380
REDIS_PW=itest-password-not-a-real-secret
KEEP=0; FAST=0
CLEAN_ONLY=0
for a in "$@"; do case "$a" in --keep) KEEP=1 ;; --fast) FAST=1 ;; --cleanup-only) CLEAN_ONLY=1 ;; esac; done

"$ROOT/scripts/get-juicefs.sh" "$ROOT/.tools" >/dev/null || { echo "could not get JuiceFS"; exit 1; }
JFS="$ROOT/.tools/juicefs"
export LOREFS_JUICEFS="$JFS"
export LOREFS_NO_SYSTEMD=1 LOREFS_NO_NOTIFY=1
export LOREFS_QUIET=300ms LOREFS_MAX_WAIT=20s LOREFS_JOURNAL_FLUSH=300ms
export LOREFS_FOLLOW=2s LOREFS_LATE_IDLE=6s LOREFS_HEALTH=2s LOREFS_DOWN_AFTER=5s

PASS=0; FAIL=0; FAILED=()
ok()   { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
check() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$d"; else bad "$d" "command failed: $*"; fi; }
refute() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then bad "$d" "command unexpectedly succeeded: $*"; else ok "$d"; fi; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# within SECONDS CMD...: retry until CMD succeeds or the time is up.
within() { local s="$1"; shift; local end=$((SECONDS+s)); until "$@" >/dev/null 2>&1; do [ $SECONDS -ge $end ] && return 1; sleep 0.2; done; }

# Run lorefs as one of the two computers.
as() { local m="$1"; shift; LOREFS_CONFIG_DIR="$T/$m/config" LOREFS_STATE_DIR="$T/$m/state" LOREFS_NOTIFY_LOG="$T/$m/notify.log" "$@"; }
lore_a() { as alpha "$LORE" "$@"; }
lore_b() { as beta "$LORE" "$@"; }
A="$T/alpha/Lore"; B="$T/beta/Lore"

start_mount() { local m="$1"; as "$m" "$LORE" mount pre; as "$m" "$LORE" mount run >"$T/$m/mount.log" 2>&1 & within 30 test -e "$T/$m/Lore/.config"; }
start_helper() { local m="$1"; LOREFS_CONFIG_DIR="$T/$m/config" LOREFS_STATE_DIR="$T/$m/state" LOREFS_NOTIFY_LOG="$T/$m/notify.log" "$LORE" helper run >>"$T/$m/helper.log" 2>&1 &
  echo $! >"$T/$m/helper.pid"; }
stop_helper() { local m="$1"; [ -f "$T/$m/helper.pid" ] && kill "$(cat "$T/$m/helper.pid")" 2>/dev/null; rm -f "$T/$m/helper.pid"; sleep 0.5; }
stop_mount() { local m="$1"; "$JFS" umount "$T/$m/Lore" >/dev/null 2>&1 || fusermount3 -uz "$T/$m/Lore" 2>/dev/null; as "$m" "$LORE" mount post 2>/dev/null; }

cleanup() {
  for m in alpha beta gamma; do stop_helper "$m" 2>/dev/null; done
  for m in alpha beta; do stop_mount "$m" 2>/dev/null; done
  pkill -f "$JFS mount" 2>/dev/null
  docker rm -f "$REDIS_NAME" >/dev/null 2>&1
  chmod -R u+rwx "$T" 2>/dev/null; rm -rf "$T"
}
versions() { as "$1" "$LORE" history --json "$2" 2>/dev/null | jq '.versions | length' 2>/dev/null || echo 0; }
store_bytes() { du -sb "$T/store" | cut -f1; }

trap 'echo; echo "interrupted"; cleanup; exit 130' INT TERM
cleanup 2>/dev/null
[ "$CLEAN_ONLY" = 1 ] && { echo "cleaned up $T"; exit 0; }
mkdir -p "$T/store" "$T/alpha" "$T/beta" "$T/gamma"

section "Setting up a local drive with two computers"
docker run -d --name "$REDIS_NAME" -p "127.0.0.1:$REDIS_PORT:6379" redis:8-alpine \
  redis-server --requirepass "$REDIS_PW" --maxmemory-policy noeviction --appendonly no >/dev/null || { echo "docker failed"; exit 1; }
within 20 docker exec "$REDIS_NAME" redis-cli -a "$REDIS_PW" --no-auth-warning ping
META_PASSWORD="$REDIS_PW" "$JFS" format --storage file --bucket "$T/store/" --trash-days 7 \
  "redis://127.0.0.1:$REDIS_PORT/1" itest >"$T/format.log" 2>&1 || { cat "$T/format.log"; exit 1; }
META_PASSWORD="$REDIS_PW" "$LORE" make-token --host 127.0.0.1 --port "$REDIS_PORT" --db 1 --drive itest --no-tls >"$T/token"

# A bad join code must change nothing.
echo "lorefs1_notavalidcode" | as gamma "$LORE" join --token-stdin --name gamma --folder "$T/gamma/Lore" >"$T/gamma/join.log" 2>&1
if [ $? -ne 0 ] && grep -q "not valid" "$T/gamma/join.log" && [ ! -e "$T/gamma/config" ] && [ ! -e "$T/gamma/Lore" ]; then
  ok "a bad join code is refused and nothing is changed"; else bad "a bad join code is refused and nothing is changed" "$(cat "$T/gamma/join.log")"; fi
# A code with a wrong password is refused too.
META_PASSWORD=wrong "$LORE" make-token --host 127.0.0.1 --port "$REDIS_PORT" --drive itest --no-tls >"$T/badtoken"
as gamma "$LORE" join --token-file "$T/badtoken" --name gamma --folder "$T/gamma/Lore" >"$T/gamma/join.log" 2>&1
if [ $? -ne 0 ] && grep -q "not valid" "$T/gamma/join.log" && [ ! -e "$T/gamma/config" ]; then
  ok "a join code with a wrong password is refused"; else bad "a join code with a wrong password is refused" "$(cat "$T/gamma/join.log")"; fi

check "alpha joins" as alpha "$LORE" join --token-file "$T/token" --name alpha --folder "$A" --cache-dir "$T/alpha/cache"
check "beta joins" as beta "$LORE" join --token-file "$T/token" --name beta --folder "$B" --cache-dir "$T/beta/cache" --cache-limit-mb 64
[ "$(stat -c %a "$T/alpha/config/config.toml")" = 600 ] && [ "$(stat -c %a "$T/alpha/config/secret.env")" = 600 ] \
  && ok "connection details are readable only by the user" || bad "connection details are readable only by the user"
out=$(as alpha "$LORE" join --token-file "$T/token" --name alpha2 2>&1)
echo "$out" | grep -q "already joined" && ok "joining twice changes nothing and says so" || bad "joining twice changes nothing and says so" "$out"

section "Folder refuses writes while the drive is not mounted"
refute "creating a file in the unmounted folder fails" touch "$B/should-not-exist"
[ -z "$(ls -A "$B")" ] && ok "nothing was written to the local disk" || bad "nothing was written to the local disk"

start_mount alpha && ok "alpha mounts the drive" || { bad "alpha mounts the drive" "$(tail -3 "$T/alpha/mount.log")"; cleanup; exit 1; }
start_mount beta && ok "beta mounts the drive" || { bad "beta mounts the drive" "$(tail -3 "$T/beta/mount.log")"; cleanup; exit 1; }
grep -q -- "--writeback" "$T/alpha/mount.log" && bad "writeback must be off" || ok "saves wait for remote storage (writeback is off)"
[ "$(jq -r .Chunk.Writeback "$A/.config")" = false ] && ok "the mounted drive reports writeback off" || bad "the mounted drive reports writeback off"
start_helper alpha; start_helper beta
within 15 test -f "$A/AGENTS.md" && ok "the rules file for agents is created on a new drive" || bad "the rules file for agents is created on a new drive"
within 15 test -f "$A/.lorefs/meta.json" && ok "the drive's bookkeeping folder is created" || bad "the drive's bookkeeping folder is created"

section "Changes appear on the other computer within seconds"
echo "hello from alpha" >"$A/hello.txt"
within 5 grep -q "hello from alpha" "$B/hello.txt" && ok "new file content is readable on beta within 5 s" || bad "new file content is readable on beta within 5 s"
cat "$B/hello.txt" >/dev/null; echo "changed by alpha" >"$A/hello.txt"
within 5 grep -q "changed by alpha" "$B/hello.txt" && ok "changed content is readable on beta within 5 s" || bad "changed content is readable on beta within 5 s"
ls "$B" >/dev/null; mv "$A/hello.txt" "$A/renamed.txt"
within 5 test -e "$B/renamed.txt" && within 5 test ! -e "$B/hello.txt" && ok "a rename shows in beta's listing within 5 s" || bad "a rename shows in beta's listing within 5 s"
rm "$A/renamed.txt"
within 5 test ! -e "$B/renamed.txt" && ok "a deletion shows in beta's listing within 5 s" || bad "a deletion shows in beta's listing within 5 s"

section "Ordinary applications work"
printf 'AAAAAAAAAABBBBBBBBBBCCCCCCCCCC' >"$A/inplace.bin"
printf 'xxxx' | dd of="$A/inplace.bin" bs=1 seek=12 conv=notrunc status=none
within 5 eval '[ "$(cat "$B/inplace.bin" 2>/dev/null)" = "AAAAAAAAAABBxxxxBBBBCCCCCCCCCC" ]' && ok "editing bytes in the middle of a file" || bad "editing bytes in the middle of a file" "$(cat "$B/inplace.bin")"
ln -s inplace.bin "$A/link" && within 5 eval '[ "$(readlink "$B/link" 2>/dev/null)" = inplace.bin ]' && ok "symbolic links" || bad "symbolic links"

# Save by replace: readers must see a complete old or new file, never a mix.
head -c 1048576 /dev/zero | tr '\0' 'a' >"$A/replace.dat"
( for i in $(seq 1 15); do c=$([ $((i%2)) = 0 ] && echo a || echo b)
    head -c 1048576 /dev/zero | tr '\0' "$c" >"$A/.replace.dat.tmp" && mv "$A/.replace.dat.tmp" "$A/replace.dat"; done ) &
writer=$!; mixed=0; reads=0
while kill -0 $writer 2>/dev/null; do
  s=$(tr -d 'a' <"$B/replace.dat" 2>/dev/null | tr -d 'b' | wc -c); u=$(tr -s 'ab' <"$B/replace.dat" 2>/dev/null | wc -c)
  reads=$((reads+1)); { [ "$s" != 0 ] || [ "$u" -gt 1 ]; } && mixed=$((mixed+1))
done
[ "$mixed" = 0 ] && [ "$reads" -gt 3 ] && ok "save by replace never shows a mixture ($reads reads)" || bad "save by replace never shows a mixture" "$mixed mixed of $reads reads"

# Appending again and again must never lose a line. (This is the check that
# fails with JuiceFS 1.4.2 when its client-side metadata cache is on.)
append_ok=1
for pause in 0 0.005 0.02; do
  python3 - "$A/append-check.txt" "$pause" <<'PY' || append_ok=0
import os, sys, time
path, pause = sys.argv[1], float(sys.argv[2])
if os.path.exists(path): os.remove(path)
for i in range(300):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o644)
    os.write(fd, ("line %04d %s\n" % (i, "x" * 60)).encode()); os.close(fd)
    if pause: time.sleep(pause)
lines = open(path, "rb").read().split(b"\n")
good = sum(1 for l in lines if l.startswith(b"line ") and len(l) == 70)
sys.exit(0 if good == 300 and os.path.getsize(path) == 300 * 71 else 1)
PY
done
[ "$append_ok" = 1 ] && ok "900 appends in three rhythms: no line lost or overwritten" || bad "appending repeatedly never loses a line"
grep -q "client-cache" "$T/alpha/mount.log" && bad "the client-side metadata cache is off" || ok "the client-side metadata cache is off"

echo lock >"$A/lock.file"; within 5 test -e "$B/lock.file"
flock "$A/lock.file" -c 'sleep 4' & sleep 1
refute "a lock held on alpha blocks beta" flock -n "$B/lock.file" true
wait $!; check "the lock is free once alpha releases it" flock -n "$B/lock.file" true

wins_ok=1
for i in $(seq 1 15); do
  f="claims/job-$i"; mkdir -p "$A/claims"; within 5 test -d "$B/claims"
  ( set -o noclobber; echo alpha >"$A/$f" ) 2>/dev/null & pa=$!
  ( set -o noclobber; echo beta >"$B/$f" ) 2>/dev/null & pb=$!
  wait $pa; ra=$?; wait $pb; rb=$?
  [ $((ra + rb)) = 1 ] || wins_ok=0
done
[ "$wins_ok" = 1 ] && ok "exclusive creation: exactly one of two computers wins, 15 of 15 rounds" || bad "exclusive creation: exactly one of two computers wins"

section "Version history"
mkdir -p "$A/notes"; echo "first draft" >"$A/notes/plan.md"
within 10 eval '[ "$(versions alpha notes/plan.md)" = 1 ]' && ok "a new file gets a version within 10 s" || bad "a new file gets a version within 10 s" "have $(versions alpha notes/plan.md)"
sleep 1; echo "second draft" >"$A/notes/plan.md"
within 10 eval '[ "$(versions alpha notes/plan.md)" = 2 ]' && ok "a changed file gets a second version" || bad "a changed file gets a second version"
old=$(lore_a open --path notes/plan.md 2)
[ "$(cat "$old")" = "first draft" ] && ok "the earlier version holds the earlier content" || bad "the earlier version holds the earlier content"
refute "a version cannot be modified" sh -c "echo x >> '$old'"
within 5 eval '[ "$(versions beta notes/plan.md)" = 2 ]' && ok "beta sees the same history" || bad "beta sees the same history"
lore_a history --list notes/plan.md | grep -q "alpha" && ok "the list shows time, computer and size" || bad "the list shows time, computer and size"
lore_a history "$A/AGENTS.md" >/dev/null 2>&1; check "history works with an absolute path" lore_a history --list "$A/notes/plan.md"

# Save by replace through a temporary file: one more version, none for the temporary file.
sleep 1; echo "third draft" >"$A/notes/.plan.md.tmp" && mv "$A/notes/.plan.md.tmp" "$A/notes/plan.md"
within 10 eval '[ "$(versions alpha notes/plan.md)" = 3 ]' && ok "save by replace records a version" || bad "save by replace records a version"
[ ! -e "$A/.lorefs/history/notes/.plan.md.tmp" ] && ok "no version is recorded for the temporary file" || bad "no version is recorded for the temporary file"
echo swap >"$A/notes/.plan.md.swp"; rm "$A/notes/.plan.md.swp"; sleep 1.5
[ ! -e "$A/.lorefs/history/notes/.plan.md.swp" ] && ok "editor swap files are not recorded" || bad "editor swap files are not recorded"
lore_a history --list notes/.x.swp | grep -q "not recorded" && ok "history explains that such files are not recorded" || bad "history explains that such files are not recorded"

# Bursts are grouped.
for i in 1 2 3 4 5; do echo "burst $i" >"$A/notes/burst.md"; done
sleep 3; n=$(versions alpha notes/burst.md)
[ "$n" -ge 1 ] && [ "$n" -le 2 ] && [ "$(cat "$(lore_a open --path notes/burst.md 1)")" = "burst 5" ] \
  && ok "five rapid saves are grouped ($n version) holding the last content" || bad "five rapid saves are grouped" "$n versions"

# What changed.
lore_a diff notes/plan.md | grep -q "^+third draft" && lore_a diff notes/plan.md | grep -q "^-second draft" \
  && ok "diff shows added and removed lines for text" || bad "diff shows added and removed lines for text" "$(lore_a diff notes/plan.md | head -5)"
head -c 2000 /dev/urandom >"$A/notes/pic.png"; within 10 eval '[ "$(versions alpha notes/pic.png)" = 1 ]'
sleep 1; head -c 3000 /dev/urandom >"$A/notes/pic.png"; within 10 eval '[ "$(versions alpha notes/pic.png)" = 2 ]'
head -c 4000 /dev/urandom >"$A/notes/deck.pdf"; within 10 eval '[ "$(versions alpha notes/deck.pdf)" = 1 ]'
sleep 1; head -c 5000 /dev/urandom >"$A/notes/deck.pdf"; within 10 eval '[ "$(versions alpha notes/deck.pdf)" = 2 ]'
lore_a diff notes/pic.png | grep -q "not a text file" && lore_a diff notes/deck.pdf | grep -q "not a text file" && ok "diff shows sizes and times for non-text files"

# Videos are not versioned; the trash covers them.
head -c 400000 /dev/urandom >"$A/notes/reel.mp4"; cp "$A/notes/reel.mp4" "$T/reel.expected"
head -c 300000 /dev/urandom >"$A/notes/SHOOT.MOV"; sleep 4
[ "$(versions alpha notes/reel.mp4)" = 0 ] && [ "$(versions alpha notes/SHOOT.MOV)" = 0 ] && [ ! -e "$A/.lorefs/history/notes/reel.mp4" ] \
  && ok "video files get no versions (in any case: reel.mp4, SHOOT.MOV)" || bad "video files get no versions" "$(versions alpha notes/reel.mp4) $(versions alpha notes/SHOOT.MOV)"
sleep 1; lore_a log -n 300 | grep -q "saved .*notes/reel.mp4" && ok "a saved video still appears in the activity log" || bad "a saved video still appears in the activity log" "$(lore_a log -n 5)"
lore_a history --list notes/reel.mp4 | grep -q "lorefs trash" && ok "history on a video explains and points to the trash" || bad "history on a video explains and points to the trash"
rm "$A/notes/reel.mp4"; sleep 1
lore_a trash | grep -q "reel.mp4" && lore_b trash reel | grep -q "reel.mp4" && ok "a deleted video is listed in the trash on both computers" || bad "a deleted video is listed in the trash" "$(lore_a trash | head -5)"
lore_a trash | grep -q -E "~alpha~|\.jsonl|\.lorefs-" && bad "the trash listing hides LoreFS's own leftovers" || ok "the trash listing hides LoreFS's own leftovers"
lore_a trash restore reel.mp4 --to notes >/dev/null && cmp -s "$T/reel.expected" "$A/notes/reel.mp4" && ok "a deleted video is brought back with its original content" || bad "a deleted video is brought back with its original content"
lore_a trash restore reel.mp4 --to notes >/dev/null; [ -e "$A/notes/reel (restored).mp4" ] && cmp -s "$T/reel.expected" "$A/notes/reel.mp4" \
  && ok "restoring again never overwrites: the copy gets a new name" || bad "restoring again never overwrites" "$(ls "$A/notes")"
out=$(lore_a trash restore never-existed.mp4 2>&1); echo "$out" | grep -q "no deleted file" && ok "restoring an unknown name explains itself" || bad "restoring an unknown name explains itself" "$out" || bad "diff shows sizes and times for non-text files"

# Restore.
lore_a restore notes/plan.md 3 >/dev/null
[ "$(cat "$A/notes/plan.md")" = "first draft" ] && ok "restore makes the chosen version current" || bad "restore makes the chosen version current" "$(cat "$A/notes/plan.md")"
within 5 grep -q "first draft" "$B/notes/plan.md" && ok "beta sees the restored content within 5 s" || bad "beta sees the restored content within 5 s"
found=0; for i in 1 2 3 4 5; do p=$(lore_a open --path notes/plan.md $i 2>/dev/null) && [ "$(cat "$p")" = "third draft" ] && found=1; done
[ "$found" = 1 ] && ok "the content replaced by the restore is kept as a version" || bad "the content replaced by the restore is kept as a version"

# Rename and move.
before=$(versions alpha notes/plan.md); mv "$A/notes/plan.md" "$A/notes/roadmap.md"
within 10 eval '[ "$(versions alpha notes/roadmap.md)" -ge "$before" ]' && ok "history follows a renamed file" || bad "history follows a renamed file" "$(versions alpha notes/roadmap.md) of $before"
mkdir -p "$A/archive"; sleep 0.5; mv "$A/notes" "$A/archive/notes-2026"
within 10 eval '[ "$(versions alpha archive/notes-2026/roadmap.md)" -ge "$before" ]' && ok "history follows a moved folder" || bad "history follows a moved folder"

# Delete on beta, bring back on alpha.
echo "do not lose me" >"$B/precious.md"; within 10 eval '[ "$(versions beta precious.md)" = 1 ]'
rm "$B/precious.md"
within 10 sh -c "LOREFS_CONFIG_DIR='$T/alpha/config' '$LORE' history --json precious.md | jq -e .deleted" \
  && ok "a deleted file keeps its versions and is marked deleted" || bad "a deleted file keeps its versions and is marked deleted"
lore_a restore precious.md 1 >/dev/null
[ "$(cat "$A/precious.md" 2>/dev/null)" = "do not lose me" ] && ok "a deleted file can be brought back" || bad "a deleted file can be brought back"

# Large file: recording a version is fast and stores nothing extra.
SIZE_MB=$([ "$FAST" = 1 ] && echo 64 || echo 1024)
dd if=/dev/urandom of="$A/large.bin" bs=1M count="$SIZE_MB" status=none
saved=$SECONDS; s0=$(store_bytes)
within 10 eval '[ "$(versions alpha large.bin)" = 1 ]'; took=$((SECONDS-saved)); s1=$(store_bytes)
[ "$(versions alpha large.bin)" = 1 ] && [ "$took" -le 5 ] && ok "a version of a ${SIZE_MB} MB file is recorded within 5 s (took about ${took} s)" || bad "a version of a ${SIZE_MB} MB file is recorded within 5 s" "took $took s"
[ $((s1 - s0)) -lt 1048576 ] && ok "recording it stored no duplicate data (+$((s1-s0)) bytes)" || bad "recording it stored no duplicate data" "+$((s1-s0)) bytes"

# Catch-up after the recorder was stopped.
stop_helper alpha; echo "changed while the recorder was off" >"$A/missed.md"; sleep 1
start_helper alpha
within 30 eval '[ "$(versions alpha missed.md)" = 1 ]' && ok "a change made while the recorder was stopped is caught up" || bad "a change made while the recorder was stopped is caught up"

# Copy tools give a file its original date, sometimes seconds after
# writing it. That must neither hide the file from the catch-up nor make
# a second version of the same content.
stop_helper alpha; echo "arrived with an old date" >"$A/old-date.md"; touch -d "2026-01-05 10:00" "$A/old-date.md"; sleep 1
start_helper alpha
within 30 eval '[ "$(versions alpha old-date.md)" = 1 ]' && ok "a file copied in with an old date while the recorder was stopped is caught up" || bad "a file copied in with an old date while the recorder was stopped is caught up" "$(versions alpha old-date.md)"
echo "written, then dated afterwards" >"$A/dated-later.md"; within 10 eval '[ "$(versions alpha dated-later.md)" = 1 ]'
touch -d "2026-02-03 09:00" "$A/dated-later.md"; echo "trigger" >"$A/other-file.md"; within 10 eval '[ "$(versions alpha other-file.md)" = 1 ]'
stop_helper alpha; start_helper alpha; sleep 6
[ "$(versions alpha dated-later.md)" = 1 ] && ok "a file whose date was set after it was recorded does not get a second version of the same content" || bad "same content under a new date is not recorded twice" "$(versions alpha dated-later.md)"

# A folder that arrives with a file still open in the program writing it
# (the same happens with a folder created a moment ago). The drive shows
# the file at the size written so far, but a copy of it holds only what
# has reached the storage. That must not become a version.
mkdir -p "$A/node_modules/arriving"
python3 -c 'import sys, time
f = open(sys.argv[1], "wb", buffering=0)
for _ in range(60):
    f.write(b"x" * 50000); time.sleep(0.05)
f.close()' "$A/node_modules/arriving/slow.bin" & slow_writer=$!
sleep 0.5; mv "$A/node_modules/arriving" "$A/arriving"; sleep 1.5
mid="$(versions alpha arriving/slow.bin)"; wait "$slow_writer"
within 10 eval '[ "$(versions alpha arriving/slow.bin)" = 1 ]'; sleep 1
[ "$mid" = 0 ] && [ "$(lore_a history --json arriving/slow.bin | jq -c '[.versions[].size]')" = "[3000000]" ] \
  && ok "a file still being written gets no version until the program closes it" \
  || bad "a file still being written gets no version until the program closes it" "while open: $mid version(s); after: sizes $(lore_a history --json arriving/slow.bin | jq -c '[.versions[].size]')"

# Two writers.
echo base >"$A/both.md"; within 10 eval '[ "$(versions alpha both.md)" = 1 ]'; cat "$B/both.md" >/dev/null
echo "from alpha" >"$A/both.md"; sleep 1.5; echo "from beta" >"$B/both.md"
within 10 eval '[ "$(versions alpha both.md)" -ge 3 ]'
within 5 grep -q "from beta" "$A/both.md" && ok "with two writers the later save is current" || bad "with two writers the later save is current" "$(cat "$A/both.md")"
all=$(for i in 1 2 3 4; do p=$(lore_a open --path both.md $i 2>/dev/null) && cat "$p"; done)
echo "$all" | grep -q "from alpha" && echo "$all" | grep -q "from beta" && ok "both writers' content is in history" || bad "both writers' content is in history" "$all"
lore_a history --json both.md | jq -r '.versions[].machine' | sort -u | tr '\n' ' ' | grep -q "alpha beta" \
  && ok "each version names the computer that saved it" || bad "each version names the computer that saved it"

# A folder made on one computer and used on the other. A computer only
# sees changes made through its own mount, so it has to be told about
# folders the other one creates or renames.
mkdir -p "$A/made-on-alpha/sub"; echo "v1 from alpha" >"$A/made-on-alpha/sub/doc.md"
within 10 eval '[ "$(versions alpha made-on-alpha/sub/doc.md)" = 1 ]'; sleep 5
cat "$B/made-on-alpha/sub/doc.md" >/dev/null; echo "v2 from beta" >"$B/made-on-alpha/sub/doc.md"
within 10 eval '[ "$(versions alpha made-on-alpha/sub/doc.md)" = 2 ]' \
  && [ "$(lore_a history --json made-on-alpha/sub/doc.md | jq -r '.versions[0].machine')" = beta ] \
  && ok "an edit on beta inside a folder made on alpha gets its version" || bad "an edit on beta inside a folder made on alpha gets its version" "$(versions alpha made-on-alpha/sub/doc.md)"
mkdir "$A/rush"; within 5 test -d "$B/rush"; echo "written by beta at once" >"$B/rush/first.md"
within 30 eval '[ "$(versions alpha rush/first.md)" = 1 ]' \
  && [ "$(lore_a history --json rush/first.md | jq -r '.versions[0].machine')" = beta ] \
  && ok "a file saved on beta before it was told about the new folder is still recorded" || bad "a file saved on beta before it was told about the new folder is still recorded"
mv "$A/made-on-alpha" "$A/renamed-on-alpha"; sleep 5
echo "v3 from beta" >"$B/renamed-on-alpha/sub/doc.md"
within 10 eval '[ "$(versions alpha renamed-on-alpha/sub/doc.md)" = 3 ]' \
  && ok "after alpha renames the folder, an edit on beta is recorded under the new name" || bad "after alpha renames the folder, an edit on beta is recorded under the new name" "$(versions alpha renamed-on-alpha/sub/doc.md)"
rm -r "$A/rush"; sleep 3; mkdir "$A/rush"; echo "second life" >"$A/rush/again.md"; within 10 eval '[ "$(versions alpha rush/again.md)" = 1 ]'; sleep 5
cat "$B/rush/again.md" >/dev/null; echo "edited by beta" >"$B/rush/again.md"
within 10 eval '[ "$(versions alpha rush/again.md)" = 2 ]' \
  && ok "a folder deleted and made again on alpha is still followed on beta" || bad "a folder deleted and made again on alpha is still followed on beta" "$(versions alpha rush/again.md)"
[ "$(lore_a log --json -n 2000 | jq '[.[] | select(.op == "mkdir" or .op == "mvdir")] | length')" = 0 ] \
  && ok "the notes computers leave each other about folders stay out of the activity log" || bad "folder notes stay out of the activity log"

# Activity log.
log=$(lore_a log -n 200)
echo "$log" | grep -q "alpha" && echo "$log" | grep -q "beta" && echo "$log" | grep -q "deleted" && echo "$log" | grep -q "restored" \
  && ok "the activity log lists saves, deletions and restores from both computers" || bad "the activity log lists events from both computers" "$(echo "$log" | head -5)"

# Out of the way.
ls "$A" | grep -q lorefs && bad "history storage is hidden from a normal listing" || ok "history storage is hidden from a normal listing"
timeout 30 grep -rIl "third draft" "$A" >"$T/grep.out" 2>/dev/null; rc=$?
[ "$rc" != 124 ] && ok "a plain recursive search over the whole drive finishes (no file that never ends)" || bad "a plain recursive search over the whole drive finishes"
ls -a "$A" | grep -q -E '^\.(accesslog|stats|config|control)$' && bad "the drive's internal files are hidden from listings" || ok "the drive's internal files are hidden from listings"
if command -v rg >/dev/null; then rg -l "third draft" "$A" 2>/dev/null | grep -q '\.lorefs' && bad "a default search (ripgrep) does not look into history" || ok "a default search (ripgrep) does not look into history"; fi

# Retention lock across computers.
flock "$A/.lorefs/prune.lock" -c 'sleep 3' & sleep 0.7
lore_b prune 2>&1 | grep -q "Another computer" && ok "only one computer removes old versions at a time" || bad "only one computer removes old versions at a time"
wait $!; lore_b prune | grep -q "Removed" && ok "removing old versions runs when the lock is free" || bad "removing old versions runs when the lock is free"

section "Fast folders"
mkdir -p "$A/kb"; for i in $(seq 1 200); do echo "note $i about something" >"$A/kb/note-$i.md"; done
pct() { "$JFS" warmup --check "$1" 2>&1 | grep -o '([0-9.]*%) cached' | tr -d '()% cached'; }
within 60 eval '[ "$(pct "$B/kb")" = "100.0" ]' && ok "200 small files added on alpha become cached on beta without being opened" || bad "200 small files become cached on beta" "cached: $(pct "$B/kb")%"
sleep 9; [ "$(cat "$A"/.lorefs/journal/beta/*.jsonl 2>/dev/null | grep -c '"path":"kb/')" = 0 ] \
  && ok "beta leaves alpha's 200 new files to alpha: every version carries alpha's name" || bad "beta leaves alpha's 200 new files to alpha" "$(lore_a history --json kb/note-7.md | jq -c '[.versions[].machine]')"
mkdir -p "$A/media"; dd if=/dev/urandom of="$A/media/clip.mp4" bs=1M count=8 status=none
within 5 test -e "$B/media/clip.mp4"; sleep 3
[ "$(pct "$B/media")" != "100.0" ] && ok "a large file is not cached on beta by default" || bad "a large file is not cached on beta by default"
lore_b pin media >/dev/null
within 30 eval '[ "$(pct "$B/media")" = "100.0" ]' && ok "pinning a folder caches its content" || bad "pinning a folder caches its content" "$(pct "$B/media")%"
lore_b pins | grep -q "media.*100%" && ok "pins lists the folder and how much is ready" || bad "pins lists the folder and how much is ready" "$(lore_b pins)"
dd if=/dev/urandom of="$A/media/clip2.mp4" bs=1M count=6 status=none
within 60 eval '[ "$(pct "$B/media/clip2.mp4")" = "100.0" ]' && ok "a file added later to a pinned folder becomes cached" || bad "a file added later to a pinned folder becomes cached"
lore_a pins | grep -q "No folders are pinned" && ok "pins belong to one computer" || bad "pins belong to one computer"
out=$(lore_b pin large.bin 2>&1); echo "$out" | grep -q "not a folder" && ok "only folders can be pinned" || bad "only folders can be pinned" "$out"
mkdir -p "$A/bigfolder"; mv "$A/large.bin" "$A/bigfolder/"; within 5 test -d "$B/bigfolder"
out=$(lore_b pin bigfolder 2>&1)
echo "$out" | grep -q "Cannot pin" && echo "$out" | grep -q "cache_limit_mb" && ok "pinning more than the cache holds is refused, with both sizes" || bad "pinning more than the cache holds is refused" "$out"
# Files added while a computer's helper was stopped are cached by its next walk.
stop_helper beta; mkdir -p "$A/kb2"; for i in $(seq 1 20); do echo "late note $i" >"$A/kb2/late-$i.md"; done
dd if=/dev/urandom of="$A/media/clip3.mp4" bs=1M count=3 status=none; sleep 3
start_helper beta
within 60 eval '[ "$(pct "$B/kb2")" = "100.0" ]' && within 60 eval '[ "$(pct "$B/media/clip3.mp4")" = "100.0" ]' \
  && ok "small files and pinned-folder files added while the helper was stopped are cached after it restarts" \
  || bad "files added while the helper was stopped are cached after it restarts" "kb2 $(pct "$B/kb2")% clip3 $(pct "$B/media/clip3.mp4")%"
lore_b unpin media >/dev/null && lore_b pins | grep -q "No folders are pinned" && ok "unpin stops keeping the folder ready" || bad "unpin stops keeping the folder ready"

section "Agent access"
grep -q "sandbox" "$A/AGENTS.md" && grep -q "lorefs history" "$A/AGENTS.md" && ok "the rules file explains history and sandboxes" || bad "the rules file explains history and sandboxes"
echo "# our own rules" >"$A/AGENTS.md"; stop_helper alpha; start_helper alpha; sleep 2
[ "$(cat "$A/AGENTS.md")" = "# our own rules" ] && ok "an edited rules file is never overwritten" || bad "an edited rules file is never overwritten"
lore_a agents-snippet | grep -q "$A" && ok "agents-snippet names this computer's drive folder" || bad "agents-snippet names this computer's drive folder"

s0=$(store_bytes); sb=$(lore_a sandbox create bigfolder try1 --json | jq -r .path); s1=$(store_bytes)
[ -d "$sb" ] && [ $((s1 - s0)) -lt 1048576 ] && ok "a sandbox of a ${SIZE_MB} MB folder stores no duplicate data" || bad "a sandbox stores no duplicate data" "+$((s1-s0)) bytes"
sb2=$(lore_a sandbox create kb agentwork --json | jq -r .path)
echo "rewritten in the sandbox" >"$sb2/note-1.md"; echo "brand new" >"$sb2/extra.md"; rm "$sb2/note-2.md"
grep -q "note 1 about" "$A/kb/note-1.md" && [ ! -e "$A/kb/extra.md" ] && ok "work in a sandbox does not affect the original" || bad "work in a sandbox does not affect the original"
d=$(lore_a sandbox diff agentwork)
echo "$d" | grep -q "changed  note-1.md" && echo "$d" | grep -q "added    extra.md" && ! echo "$d" | grep -q "note-2.md" \
  && ok "sandbox diff lists added and changed files" || bad "sandbox diff lists added and changed files" "$d"
lore_a sandbox list | grep -q agentwork && ok "sandbox list shows it" || bad "sandbox list shows it"
lore_a sandbox apply agentwork --yes >/dev/null
grep -q "rewritten in the sandbox" "$A/kb/note-1.md" && [ "$(cat "$A/kb/extra.md")" = "brand new" ] && [ -e "$A/kb/note-2.md" ] \
  && ok "apply copies added and changed files and deletes nothing" || bad "apply copies added and changed files and deletes nothing"
prev=$(for i in 1 2 3; do p=$(lore_a open --path kb/note-1.md $i 2>/dev/null) && cat "$p"; done)
echo "$prev" | grep -q "note 1 about" && ok "a file replaced by apply keeps its previous content in history" || bad "a file replaced by apply keeps its previous content in history"
lore_a sandbox remove agentwork >/dev/null && lore_a sandbox remove try1 >/dev/null && [ ! -e "$sb2" ] && grep -q "rewritten" "$A/kb/note-1.md" \
  && ok "removing a sandbox leaves the original alone" || bad "removing a sandbox leaves the original alone"
out=$(lore_a containers on 2>&1)
if grep -q '^user_allow_other' /etc/fuse.conf 2>/dev/null; then ok "container access: administrator step already done on this machine (skipped)";
else echo "$out" | grep -q "sudo tee -a /etc/fuse.conf" && ok "containers on explains the one administrator step" || bad "containers on explains the one administrator step" "$out"; fi

section "Status"
sleep 3
out=$(lore_a status); code=$?
[ "$code" = 0 ] && echo "$out" | grep -q "Everything is working" && ok "a healthy drive reports healthy and exits 0" || bad "a healthy drive reports healthy and exits 0" "exit $code: $out"
for word in "Drive" "Index" "Storage" "Local cache" "Version history" "Index backup" "Index memory" "JuiceFS"; do
  echo "$out" | grep -q "$word" || bad "status shows: $word"; done; ok "status shows every item on one page"
js=$(lore_a status --json); echo "$js" | jq -e '.overall == "ok" and (.checks | length >= 7)' >/dev/null && ok "status --json is valid and complete" || bad "status --json is valid and complete"
{ echo "$out"; echo "$js"; lore_a invite 2>&1 >/dev/null; } | grep -q "$REDIS_PW" && bad "status never shows the password" || ok "status never shows the password"
# Which version of lorefs each computer runs.
echo "$js" | jq -e '.checks[] | select(.id == "program") | .level == "ok" and (.detail | test("the same on all 2 computers"))' >/dev/null \
  && ok "status says both computers run the same version of lorefs" || bad "status says both computers run the same version of lorefs" "$(echo "$js" | jq -c '.checks[] | select(.id == "program")')"
printf '{"machine":"gamma","version":"fffffff","commit_time":4102444800,"seen":"%s"}\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$A/.lorefs/machines/gamma.json"
out=$(lore_a status); code=$?
[ "$code" = 1 ] && echo "$out" | grep -q "newer version is in use on: gamma (fffffff" && echo "$out" | grep -q "lorefs update" \
  && ok "status warns when another computer runs a newer version, and says how to update" || bad "status warns when another computer runs a newer version" "exit $code: $out"
rm -f "$A/.lorefs/machines/gamma.json"
out=$(as gamma "$LORE" status 2>&1); code=$?; [ "$code" = 3 ] && echo "$out" | grep -q "has not joined" && echo "$out" | grep -q "lorefs join" && ok "a computer that has not joined is told how to join" || bad "a computer that has not joined is told how to join"

stop_helper alpha; sleep 1
out=$(lore_a status); code=$?
[ "$code" = 1 ] && echo "$out" | grep -q "not being recorded" && ok "a stopped recorder is a warning (exit 1)" || bad "a stopped recorder is a warning (exit 1)" "exit $code"
start_helper alpha

# Invite a third computer with a code from alpha.
lore_a invite --quiet >"$T/invite"
check "a code from invite lets another computer join" as gamma "$LORE" join --token-file "$T/invite" --name gamma --folder "$T/gamma/Lore" --cache-dir "$T/gamma/cache"
as gamma "$LORE" leave --yes >/dev/null 2>&1
[ ! -e "$T/gamma/config/config.toml" ] && [ ! -e "$T/gamma/config/secret.env" ] && [ ! -e "$T/gamma/Lore" ] && [ -e "$A/AGENTS.md" ] \
  && ok "leave removes the connection details and leaves the drive alone" || bad "leave removes the connection details and leaves the drive alone"

section "When the index is unreachable"
head -c 300000 /dev/urandom >"$A/uncached.bin"; within 5 test -e "$B/uncached.bin"
docker pause "$REDIS_NAME" >/dev/null
t0=$SECONDS; cat "$B/uncached.bin" >/dev/null 2>"$T/err.txt"; rc=$?; took=$((SECONDS-t0))
[ "$rc" != 0 ] && [ "$took" -le 120 ] && ok "opening an uncached file fails with an error instead of hanging (after ${took} s)" || bad "opening an uncached file fails within 2 minutes" "rc=$rc after $took s"
lore_a status --json >"$T/status.json" 2>/dev/null; code=$?
[ "$code" = 2 ] && jq -e '.checks[] | select(.id=="index") | .level == "failed"' "$T/status.json" >/dev/null && ok "status marks the index as failed and exits 2" || bad "status marks the index as failed and exits 2" "exit $code"
out=$(lore_a status 2>/dev/null); echo "$out" | grep -q "Check your internet connection first" && echo "$out" | grep -q "cannot be opened or saved" && ok "status explains the problem and the next step in plain words" || bad "status explains the problem in plain words"
within 40 grep -q "unavailable" "$T/alpha/notify.log" && ok "one notification says the drive is unavailable" || bad "one notification says the drive is unavailable"
docker unpause "$REDIS_NAME" >/dev/null
within 60 cat "$B/uncached.bin" && ok "file operations work again without remounting" || bad "file operations work again without remounting"
within 40 grep -q "available again" "$T/alpha/notify.log" && ok "one notification says the drive is available again" || bad "one notification says the drive is available again"
[ "$(grep -c unavailable "$T/alpha/notify.log")" = 1 ] && ok "only one notification per change of state" || bad "only one notification per change of state" "$(cat "$T/alpha/notify.log")"

section "Power loss right after a save"
head -c 3000000 /dev/urandom >"$T/expected.bin"
cp "$T/expected.bin" "$A/critical.bin"
pkill -9 -f "[j]uicefs mount.*alpha/Lore"   # the supervisor and the worker, at once
within 10 cmp -s "$T/expected.bin" "$B/critical.bin" && ok "a file whose save returned is complete on beta after alpha's mount is killed" || bad "a save that returned survives a killed mount"
stop_helper alpha; as alpha "$LORE" mount post
refute "after the crash the folder refuses writes again" touch "$A/should-not-exist"
start_mount alpha && ok "alpha remounts after the crash" || bad "alpha remounts after the crash" "$(tail -2 "$T/alpha/mount.log" | cut -c1-200)"
within 30 cmp -s "$T/expected.bin" "$A/critical.bin" && ok "alpha reads the complete file after remounting" || bad "alpha reads the complete file after remounting"

echo
printf '\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -gt 0 ] && printf '  failed: %s\n' "${FAILED[@]}"
if [ "$KEEP" = 1 ]; then echo "left running in $T (clean up with: scripts/itest.sh --cleanup-only)"; else cleanup; fi
[ "$FAIL" = 0 ]
