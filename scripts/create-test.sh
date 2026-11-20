#!/usr/bin/env bash
# Checks `lorefs create`: every check that must stop a creation, the
# creation itself against MinIO (storage) and Redis (index) in Docker, a
# second computer joining with the printed code, and trial drives.
# Nothing here touches ~/.config/lorefs or any real drive.
#
# Usage: scripts/create-test.sh [--cleanup-only]
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LORE="$ROOT/bin/lorefs"
T="${LOREFS_CTEST_DIR:-/tmp/lorefs-ctest}"
MINIO=lorefs-ctest-minio; MINIO_PORT=19000
IDX=lorefs-ctest-index; IDX_PORT=16410
S_KEY=ctestaccesskey; S_SECRET=ctest-storage-secret-123
I_PASS=ctest-index-password-xyz
BUCKET_URL="http://127.0.0.1:$MINIO_PORT/ctestbucket"
INDEX_IMAGE="${INDEX_IMAGE:-redis:8-alpine}"

"$ROOT/scripts/get-juicefs.sh" "$ROOT/.tools" >/dev/null || { echo "could not get JuiceFS"; exit 1; }
JFS="$ROOT/.tools/juicefs"
export LOREFS_JUICEFS="$JFS" LOREFS_NO_SYSTEMD=1 LOREFS_NO_NOTIFY=1
export LOREFS_QUIET=300ms LOREFS_MAX_WAIT=20s LOREFS_JOURNAL_FLUSH=300ms LOREFS_FOLLOW=2s LOREFS_HEALTH=2s

PASS=0; FAIL=0; FAILED=()
ok()  { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
within() { local s="$1"; shift; local end=$((SECONDS+s)); until "$@" >/dev/null 2>&1; do [ $SECONDS -ge $end ] && return 1; sleep 0.2; done; }

# Run lorefs as one computer ($1) with its own settings.
as() { local m="$1"; shift; LOREFS_CONFIG_DIR="$T/$m/config" LOREFS_STATE_DIR="$T/$m/state" LOREFS_DATA_DIR="$T/$m/data" "$@"; }
lore() { local m="$1"; shift; as "$m" "$LORE" "$@"; }
start_mount() { local m="$1"; as "$m" "$LORE" mount pre; as "$m" "$LORE" mount run >"$T/$m/mount.log" 2>&1 & within 40 test -e "$T/$m/Lore/.config"; }
start_helper() { local m="$1"; as "$m" "$LORE" helper run >>"$T/$m/helper.log" 2>&1 & echo $! >"$T/$m/helper.pid"; }
stop_helper() { local m="$1"; [ -f "$T/$m/helper.pid" ] && kill "$(cat "$T/$m/helper.pid")" 2>/dev/null; rm -f "$T/$m/helper.pid"; sleep 0.5; }
stop_mount() { local m="$1"; "$JFS" umount "$T/$m/Lore" >/dev/null 2>&1 || fusermount3 -uz "$T/$m/Lore" 2>/dev/null; sleep 1; }
idx() { docker exec "$IDX" redis-cli -a "$I_PASS" --no-auth-warning -n "$1" "${@:2}" 2>/dev/null; }

cleanup() {
  for m in alpha beta triala; do stop_helper "$m" 2>/dev/null; stop_mount "$m" 2>/dev/null; done
  pkill -f "$JFS mount" 2>/dev/null
  docker rm -f "$MINIO" "$IDX" >/dev/null 2>&1
  chmod -R u+rwx "$T" 2>/dev/null; rm -rf "$T"
}
trap 'echo; echo "interrupted"; cleanup; exit 130' INT TERM
cleanup 2>/dev/null
[ "${1:-}" = --cleanup-only ] && { echo "cleaned up $T"; exit 0; }
mkdir -p "$T/minio" "$T/alpha" "$T/beta" "$T/triala"

section "Setting up storage (MinIO) and an index ($INDEX_IMAGE)"
docker run -d --name "$MINIO" --user "$(id -u):$(id -g)" -p "127.0.0.1:$MINIO_PORT:9000" -v "$T/minio:/data" \
  -e MINIO_ROOT_USER="$S_KEY" -e MINIO_ROOT_PASSWORD="$S_SECRET" cgr.dev/chainguard/minio:latest server /data >/dev/null || { echo "docker failed"; exit 1; }
docker run -d --name "$IDX" -p "127.0.0.1:$IDX_PORT:6379" "$INDEX_IMAGE" redis-server --requirepass "$I_PASS" --appendonly no >/dev/null || { echo "docker failed"; exit 1; }
within 30 curl -sf "http://127.0.0.1:$MINIO_PORT/minio/health/live" || { echo "MinIO did not start"; exit 1; }
within 20 docker exec "$IDX" redis-cli -a "$I_PASS" --no-auth-warning ping
export LOREFS_STORAGE_KEY="$S_KEY" LOREFS_STORAGE_SECRET="$S_SECRET" LOREFS_INDEX_PASSWORD="$I_PASS"
create() { # create INDEX_DB [extra args...]  (run as alpha; no connecting)
  local db="$1"; shift
  lore alpha create --bucket "$BUCKET_URL" --index "redis://127.0.0.1:$IDX_PORT/$db" --allow-unencrypted-index --no-connect "$@" </dev/null
}

section "Nothing is created unless every check passes"
out=$(LOREFS_STORAGE_SECRET=wrong-secret create 1 --name drive-one 2>&1); code=$?
if [ $code -ne 0 ] && echo "$out" | grep -q "refused the key"; then ok "a wrong storage secret is refused with a plain message"; else bad "a wrong storage secret is refused with a plain message" "exit $code: $out"; fi
[ "$(idx 1 dbsize)" = "(integer) 0" ] || [ "$(idx 1 dbsize)" = "0" ] && ok "the index still holds nothing after the failed attempt" || bad "the index still holds nothing" "$(idx 1 dbsize)"
out=$(LOREFS_STORAGE_KEY=nosuchkey create 1 --name drive-one 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "refused the key" && ok "a wrong storage access key is refused too" || bad "a wrong storage access key is refused too" "$out"
out=$(lore alpha create --bucket "$BUCKET_URL" --index "redis://127.0.0.1:19998/1" --allow-unencrypted-index --no-connect --name drive-one </dev/null 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "cannot be reached" && ok "an unreachable index is reported as such" || bad "an unreachable index is reported as such" "exit $code: $out"
[ ! -e "$T/minio/ctestbucket" ] && ok "the bucket was not touched when the index was unreachable" || bad "the bucket was not touched when the index was unreachable" "$(ls "$T/minio")"
out=$(LOREFS_INDEX_PASSWORD=wrong create 1 --name drive-one 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "refused the password" && [ ! -e "$T/minio/ctestbucket" ] && ok "a wrong index password is refused and the bucket is untouched" || bad "a wrong index password is refused and the bucket is untouched" "$out"
out=$(lore alpha create --bucket "$BUCKET_URL" --index "redis://127.0.0.1:$IDX_PORT/1" --no-connect --name drive-one </dev/null 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q -- "--allow-unencrypted-index" && [ ! -e "$T/minio/ctestbucket" ] \
  && ok "an unencrypted index is refused and the option is named" || bad "an unencrypted index is refused and the option is named" "exit $code: $out"
out=$(create 1 --name x 2>&1); [ $? -ne 0 ] && echo "$out" | grep -q "not usable" && ok "a drive name JuiceFS would reject is refused first" || bad "a drive name JuiceFS would reject is refused first" "$out"
out=$(lore alpha create --index "redis://127.0.0.1:$IDX_PORT/1" --allow-unencrypted-index </dev/null 2>&1); [ $? -eq 64 ] && echo "$out" | grep -q "missing --bucket" \
  && ok "missing inputs are listed when there is no terminal" || bad "missing inputs are listed when there is no terminal" "$out"
printf 'LOREFS_STORAGE_SECRET=%s\n' "$S_SECRET" >"$T/secrets"; chmod 644 "$T/secrets"
out=$( unset LOREFS_STORAGE_SECRET; lore alpha create --secrets-file "$T/secrets" --bucket "$BUCKET_URL" --index "redis://127.0.0.1:$IDX_PORT/1" --allow-unencrypted-index </dev/null 2>&1 )
echo "$out" | grep -q "chmod 600" && ok "a secrets file readable by others is refused" || bad "a secrets file readable by others is refused" "$out"

section "Secrets typed at the prompt are not shown"
# A real terminal (a pty); the index address is unreachable so that nothing is created.
LOREFS_CONFIG_DIR="$T/alpha/config" LOREFS_STATE_DIR="$T/alpha/state" LOREFS_DATA_DIR="$T/alpha/data" LORE="$LORE" \
  env -u LOREFS_STORAGE_KEY -u LOREFS_STORAGE_SECRET -u LOREFS_INDEX_PASSWORD python3 - <<'PY' >"$T/pty.out" 2>&1
import os, pty, sys, time, select
secret1, secret2 = "typed-storage-secret-QQ1", "typed-index-password-QQ2"
pid, fd = pty.fork()
if pid == 0:
    os.execv(os.environ["LORE"], [os.environ["LORE"], "create", "--allow-unencrypted-index", "--no-connect", "--name", "prompt-test"])
out = b""
def read_until(text, timeout=20):
    global out
    end = time.time() + timeout
    while text.encode() not in out[len(out) - 4000:] and time.time() < end:
        r, _, _ = select.select([fd], [], [], 0.5)
        if r:
            try: out += os.read(fd, 4096)
            except OSError: break
steps = [("Bucket address", "http://127.0.0.1:19000/ctestbucket"), ("access key", "somekey"),
         ("secret key", secret1), ("Index address", "redis://127.0.0.1:19998/1"), ("Index password", secret2)]
marks = []
for prompt, answer in steps:
    read_until(prompt + ": ") if False else read_until(prompt)
    time.sleep(0.4)
    marks.append(len(out))
    os.write(fd, (answer + "\n").encode())
    time.sleep(0.6)
read_until("cannot be reached", 25)
text = out.decode(errors="replace")
echoed = [s for s in (secret1, secret2) if s in text]
print("ECHOED:", echoed)
print("REACHED:", "cannot be reached" in text)
print("VISIBLE_ANSWER_ECHOED:", "somekey" in text)
PY
grep -q "ECHOED: \[\]" "$T/pty.out" && ok "nothing appears on screen while a secret is typed, and the output never contains it" || bad "secrets typed at a prompt are not echoed" "$(cat "$T/pty.out")"
grep -q "REACHED: True" "$T/pty.out" && ok "the typed answers were used (the unreachable index was reported)" || bad "the typed answers were used" "$(cat "$T/pty.out")"

section "No secret on a command line or in the output"
# A wrapper makes `juicefs format` wait four seconds, so that there is time to look.
printf '#!/bin/sh\n[ "$1" = format ] && sleep 4\nexec "%s" "$@"\n' "$JFS" >"$T/slow-juicefs"; chmod +x "$T/slow-juicefs"
( LOREFS_JUICEFS="$T/slow-juicefs" LOREFS_STORAGE_SECRET=ps-visible-secret-777 create 1 --name drive-one >"$T/ps-run.log" 2>&1 & )
leaked=1; end=$((SECONDS+40)); seen=0; formats=0
sleep 0.3
while [ $SECONDS -lt $end ] && pgrep -f "bin/lorefs create" >/dev/null; do
  seen=$((seen+1)); pgrep -f "slow-juicefs format|juicefs format" >/dev/null && formats=$((formats+1))
  ps -eo args | grep -E "ps-visible-secret-777|$I_PASS" | grep -v grep >/dev/null && { leaked=0; break; }
  sleep 0.2
done
[ $seen -gt 3 ] && [ $formats -gt 0 ] && [ $leaked = 1 ] && ok "while create and juicefs ran, ps showed neither the storage secret nor the index password" || bad "ps showed no secret" "looks=$seen with juicefs format running in $formats; leaked=$((1-leaked))"
while pgrep -f "bin/lorefs create" >/dev/null; do sleep 0.5; done

section "Creating a drive"
create 1 --name drive-one >"$T/create.out" 2>"$T/create.err"; code=$?
[ $code -eq 0 ] && ok "a drive is created on MinIO and Redis" || bad "a drive is created on MinIO and Redis" "$(cat "$T/create.err")"
code1=$(grep -c '^lorefs2_' "$T/create.out"); [ "$code1" = 1 ] && ok "a join code (lorefs2_) is printed on its own line" || bad "a join code is printed" "$(cat "$T/create.out")"
grep -q "allow-unencrypted\|not encrypted" "$T/create.err" && ok "the unencrypted choice is warned about" || bad "the unencrypted choice is warned about" "$(cat "$T/create.err")"
cat "$T/create.out" "$T/create.err" | grep -q -e "$S_SECRET" -e "$I_PASS" && bad "the output never contains a secret" || ok "the output never contains a secret"
st=$(META_PASSWORD="$I_PASS" "$JFS" status "redis://127.0.0.1:$IDX_PORT/1" 2>/dev/null)
echo "$st" | jq -e '.Setting.TrashDays == 7 and .Setting.ChangeLog == true and .Setting.ChangeLogMaxLines == 100000 and .Setting.ChangeLogMaxAge == 86400' >/dev/null \
  && ok "the drive keeps deleted files 7 days and has its change log on (24 h, 100000)" || bad "safe defaults" "$(echo "$st" | jq -c .Setting)"
out=$(create 1 --name drive-one 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "lorefs join" && ok "an index that already holds a drive is refused, pointing to join" || bad "an index that already holds a drive is refused" "exit $code: $out"
idx 1 set stray-key 1 >/dev/null; idx 3 set stray-key 1 >/dev/null
out=$(create 3 --name drive-two 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "not empty" && ok "an index database holding other data is refused" || bad "an index database holding other data is refused" "exit $code: $out"
idx 1 del stray-key >/dev/null
out=$(create 2 --name drive-one 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "already holds a drive" && [ "$(idx 2 dbsize | tr -dc 0-9)" = 0 ] \
  && ok "a bucket that already holds a drive of that name is refused and the new index stays empty" || bad "a bucket holding a drive of that name is refused" "exit $code: $out / $(idx 2 dbsize)"
create 2 --name drive-two >/dev/null 2>"$T/create2.err" && ok "a second drive of another name can share the bucket" || bad "a second drive of another name can share the bucket" "$(cat "$T/create2.err")"

section "Another computer joins with the printed code"
code=$(grep '^lorefs2_' "$T/create.out")
# Computer alpha connects through create's own offer; beta uses the code.
lore alpha create --bucket "$BUCKET_URL" --index "redis://127.0.0.1:$IDX_PORT/4" --allow-unencrypted-index --connect \
  --name drive-three --machine alpha --folder "$T/alpha/Lore" </dev/null >"$T/c3.out" 2>"$T/c3.err" && ok "create --connect writes this computer's settings" || bad "create --connect writes this computer's settings" "$(cat "$T/c3.err")"
start_mount alpha && ok "the new drive mounts on this computer" || bad "the new drive mounts on this computer" "$(tail -3 "$T/alpha/mount.log")"
start_helper alpha
code3=$(grep '^lorefs2_' "$T/c3.out")
echo "$code3" | lore beta join --token-stdin --name beta --folder "$T/beta/Lore" --cache-dir "$T/beta/cache" >"$T/beta-join.log" 2>&1 && ok "a second computer joins with the printed code" || bad "a second computer joins" "$(cat "$T/beta-join.log")"
start_mount beta && ok "and mounts the drive" || bad "and mounts the drive" "$(tail -3 "$T/beta/mount.log")"
echo "hello from alpha" >"$T/alpha/Lore/note.txt"
within 10 grep -q "hello from alpha" "$T/beta/Lore/note.txt" && ok "a file saved on one computer appears on the other" || bad "a file saved on one computer appears on the other"
echo "hello from beta" >"$T/beta/Lore/reply.txt"
within 10 grep -q "hello from beta" "$T/alpha/Lore/reply.txt" && ok "and the other way round" || bad "and the other way round"
within 30 test -e "$T/alpha/Lore/AGENTS.md" && ok "the rules file for agents is at the top of the new drive" || bad "the rules file for agents is at the top of the new drive"
sleep 3; js=$(lore alpha status --json)
echo "$js" | jq -e '.checks[] | select(.id == "change-log") | .level == "ok"' >/dev/null && ok "status shows the change log as fine" || bad "status shows the change log as fine" "$(echo "$js" | jq -c '.checks[] | select(.id=="change-log")')"
stop_helper alpha; stop_mount beta; stop_mount alpha

section "A trial drive needs no account anywhere"
# The command that creates it runs in a process with no network at all.
if unshare -rn true 2>/dev/null; then
  LOREFS_CONFIG_DIR="$T/triala/config" LOREFS_STATE_DIR="$T/triala/state" LOREFS_DATA_DIR="$T/triala/data" \
    unshare -rn "$LORE" create --local --name trial-a --no-connect >"$T/local.out" 2>&1 \
    && ok "create --local works in a process with no network" || bad "create --local works in a process with no network" "$(cat "$T/local.out")"
  rm -rf "$T/triala/data"
else
  echo "  (unshare -rn is not available here; the no-network check is skipped)"
fi
mkdir -p "$T/triala"
lore triala create --local --name trial-a --folder "$T/triala/Lore" --machine triala >"$T/local.out" 2>&1 && ok "create --local makes and connects a trial drive" || bad "create --local makes and connects a trial drive" "$(cat "$T/local.out")"
grep -q "cannot be joined from another computer" "$T/local.out" && ok "it says a trial drive cannot be joined from another computer" || bad "it says a trial drive cannot be joined" "$(cat "$T/local.out")"
start_mount triala && ok "the trial drive's folder appears" || bad "the trial drive's folder appears" "$(tail -3 "$T/triala/mount.log")"
start_helper triala
mkdir -p "$T/triala/Lore/notes"; echo "first" >"$T/triala/Lore/notes/a.md"; sleep 1; echo "second" >"$T/triala/Lore/notes/a.md"
within 20 eval '[ "$(lore triala history --json notes/a.md 2>/dev/null | jq ".versions | length")" = 2 ]' && ok "a saved file gets versions on a trial drive" || bad "a saved file gets versions on a trial drive" "$(lore triala history --json notes/a.md 2>&1 | head -5)"
lore triala sandbox create notes box >/dev/null 2>&1 && echo "in sandbox" >"$T/triala/Lore/.lorefs/sandboxes/box/b.md" 2>/dev/null
d=$(lore triala sandbox list --json 2>/dev/null | jq -r '.[0].name // empty'); [ "$d" = box ] && ok "a sandbox can be made on a trial drive" || bad "a sandbox can be made on a trial drive" "$(lore triala sandbox list 2>&1)"
rm "$T/triala/Lore/notes/a.md"; sleep 2
lore triala trash 2>&1 | grep -q "a.md" && ok "a deleted file is in the trash of a trial drive" || bad "a deleted file is in the trash of a trial drive" "$(lore triala trash 2>&1)"
out=$(lore triala status 2>&1); echo "$out" | grep -q "trial drive" && ok "status says it is a trial drive" || bad "status says it is a trial drive" "$out"
pid=$(pgrep -f "juicefs mount sqlite3.*$T/triala" | head -1)
if [ -n "$pid" ]; then
  conns=$(ss -tnp 2>/dev/null | grep -c "pid=$pid,"); [ "$conns" = 0 ] && ok "the trial drive's mount holds no network connection" || bad "the trial drive's mount holds no network connection" "$conns found"
fi
out=$(lore triala invite 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "trial drive" && echo "$out" | grep -q "lorefs create" && ok "invite on a trial drive explains instead of printing a code" || bad "invite on a trial drive explains" "exit $code: $out"
stop_helper triala; stop_mount triala
lore triala leave --delete-drive --yes >"$T/del.out" 2>&1; code=$?
if [ $code -eq 0 ] && [ ! -e "$T/triala/data/local/trial-a" ] && [ ! -e "$T/triala/config/config.toml" ] && [ ! -e "$T/triala/Lore" ]; then
  ok "leave --delete-drive removes the trial drive and leaves nothing behind"; else bad "leave --delete-drive removes the trial drive and leaves nothing behind" "exit $code: $(cat "$T/del.out"); $(find "$T/triala" -maxdepth 4 | head -20)"; fi

echo
printf '\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -gt 0 ] && { printf '  failed: %s\n' "${FAILED[@]}"; }
cleanup
[ "$FAIL" = 0 ]
