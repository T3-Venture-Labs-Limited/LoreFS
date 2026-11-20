#!/usr/bin/env bash
# Checks the backup and the restore: MinIO stands in for two providers (a
# bucket for the drive, a bucket for the copy) and Redis for the index.
#
# Usage: scripts/backup-test.sh [--cleanup-only]
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LORE="$ROOT/bin/lorefs"
T="${LOREFS_BTEST_DIR:-/tmp/lorefs-btest}"
MINIO=lorefs-btest-minio; MINIO_PORT=19200
IDX=lorefs-btest-index; IDX_PORT=16460; IDX_PW=btest-index-password-xyz
NEW=lorefs-btest-newindex; NEW_PORT=16461
S_KEY=btestaccesskey; S_SECRET=btest-storage-secret-123
DRIVE_BUCKET="http://127.0.0.1:$MINIO_PORT/drivebucket"
COPY_BUCKET="http://127.0.0.1:$MINIO_PORT/copybucket"
DRIVE=btest-drive

"$ROOT/scripts/get-juicefs.sh" "$ROOT/.tools" >/dev/null || { echo "could not get JuiceFS"; exit 1; }
JFS="$ROOT/.tools/juicefs"
export LOREFS_JUICEFS="$JFS" LOREFS_NO_SYSTEMD=1 LOREFS_NO_NOTIFY=1
export LOREFS_QUIET=300ms LOREFS_MAX_WAIT=20s LOREFS_JOURNAL_FLUSH=300ms LOREFS_FOLLOW=2s LOREFS_HEALTH=2s

PASS=0; FAIL=0; FAILED=()
ok()  { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
within() { local s="$1"; shift; local end=$((SECONDS+s)); until "$@" >/dev/null 2>&1; do [ $SECONDS -ge $end ] && return 1; sleep 0.3; done; }

A="$T/alpha/Lore"; B="$T/beta/Lore"
as() { local m="$1"; shift; LOREFS_CONFIG_DIR="$T/$m/config" LOREFS_STATE_DIR="$T/$m/state" "$@"; }
lore() { local m="$1"; shift; as "$m" "$LORE" "$@"; }
start_mount() { local m="$1"; as "$m" "$LORE" mount pre; as "$m" "$LORE" mount run >"$T/$m/mount.log" 2>&1 & within 40 test -e "$T/$m/Lore/.config"; }
stop_mount() { "$JFS" umount "$T/$1/Lore" >/dev/null 2>&1 || fusermount3 -uz "$T/$1/Lore" 2>/dev/null; sleep 1; }
meta="redis://127.0.0.1:$IDX_PORT/1"
idx() { docker exec "$IDX" redis-cli -a "$IDX_PW" --no-auth-warning -n "$1" "${@:2}" 2>/dev/null; }
objects() { find "$T/minio/$1" -name xl.meta 2>/dev/null | wc -l; }   # MinIO keeps each object as a folder with xl.meta

cleanup() {
  for m in alpha beta; do stop_mount "$m" 2>/dev/null; done
  pkill -f "$JFS mount redis://127.0.0.1:$IDX_PORT" 2>/dev/null
  LOREFS_CONFIG_DIR="$T/alpha/config" "$LORE" backup schedule --off >/dev/null 2>&1
  docker rm -f "$MINIO" "$IDX" "$NEW" >/dev/null 2>&1
  chmod -R u+rwx "$T" 2>/dev/null; rm -rf "$T"
}
trap 'echo; echo "interrupted"; cleanup; exit 130' INT TERM
cleanup 2>/dev/null
[ "${1:-}" = --cleanup-only ] && { echo "cleaned up $T"; exit 0; }
mkdir -p "$T/minio" "$T/alpha" "$T/beta"

section "A drive on MinIO and a second bucket for the copy"
docker run -d --name "$MINIO" --user "$(id -u):$(id -g)" -p "127.0.0.1:$MINIO_PORT:9000" -v "$T/minio:/data" \
  -e MINIO_ROOT_USER="$S_KEY" -e MINIO_ROOT_PASSWORD="$S_SECRET" cgr.dev/chainguard/minio:latest server /data >/dev/null || { echo "docker failed"; exit 1; }
docker run -d --name "$IDX" -p "127.0.0.1:$IDX_PORT:6379" redis:8-alpine redis-server --requirepass "$IDX_PW" --appendonly no >/dev/null
docker run -d --name "$NEW" -p "127.0.0.1:$NEW_PORT:6379" redis:8-alpine redis-server --requirepass "$IDX_PW" --appendonly no >/dev/null
within 30 curl -sf "http://127.0.0.1:$MINIO_PORT/minio/health/live" || { echo "MinIO did not start"; exit 1; }
within 20 docker exec "$IDX" redis-cli -a "$IDX_PW" --no-auth-warning ping
export LOREFS_STORAGE_KEY="$S_KEY" LOREFS_STORAGE_SECRET="$S_SECRET" LOREFS_INDEX_PASSWORD="$IDX_PW"
lore alpha create --bucket "$DRIVE_BUCKET" --index "redis://127.0.0.1:$IDX_PORT/1" --allow-unencrypted-index --name "$DRIVE" --machine alpha \
  --folder "$A" --connect </dev/null >"$T/create.out" 2>"$T/create.err" && ok "the drive is created and connected" || { bad "the drive is created" "$(cat "$T/create.err")"; cleanup; exit 1; }
start_mount alpha || { bad "the drive mounts" "$(tail -3 "$T/alpha/mount.log")"; cleanup; exit 1; }
grep '^lorefs2_' "$T/create.out" | lore beta join --token-stdin --name beta --folder "$B" --cache-dir "$T/beta/cache" >/dev/null 2>&1 && start_mount beta && ok "a second computer joins" || bad "a second computer joins"
mkdir -p "$A/docs"; for i in $(seq 1 1200); do echo "document $i" >"$A/docs/d$i.md"; done
echo "original" >"$A/docs/special.md"; head -c 3000000 /dev/urandom >"$A/docs/big.bin"
sync; sleep 2
within 40 eval '[ "$(objects drivebucket)" -ge 1200 ]' && ok "the drive holds $(objects drivebucket) objects" || bad "the drive holds its objects" "$(objects drivebucket)"
# JuiceFS writes a copy of the index into the bucket every hour at the soonest (5 minutes is
# its lowest setting), so the test writes one the same way: a gzip of `juicefs dump`.
dump="dump-$(date -u +%Y-%m-%d-%H%M%S).json.gz"
META_PASSWORD="$IDX_PW" "$JFS" dump "$meta" 2>/dev/null | gzip >"$T/$dump"
curl -s -o /dev/null -X PUT --aws-sigv4 "aws:amz:us-east-1:s3" --user "$S_KEY:$S_SECRET" -T "$T/$dump" "http://127.0.0.1:$MINIO_PORT/drivebucket/$DRIVE/meta/$dump"
ls "$T"/minio/drivebucket/"$DRIVE"/meta/dump-* >/dev/null 2>&1 && ok "a copy of the index is in the bucket, as JuiceFS writes them" || bad "an index copy appears in the bucket" "$(ls "$T/minio/drivebucket/$DRIVE" 2>&1)"
# Create the empty destination bucket (setup checks it, it does not make it).
curl -s -o /dev/null -X PUT --aws-sigv4 "aws:amz:us-east-1:s3" --user "$S_KEY:$S_SECRET" "http://127.0.0.1:$MINIO_PORT/copybucket"
[ -d "$T/minio/copybucket" ] && ok "the second bucket exists" || bad "the second bucket exists"

section "Setting up the backup"
out=$(LOREFS_BACKUP_SECRET="$S_SECRET" lore alpha backup setup --bucket "$DRIVE_BUCKET" --access-key "$S_KEY" 2>&1 </dev/null); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "the bucket the drive itself uses" && ok "a destination that is the drive's own bucket is refused" || bad "the drive's own bucket is refused" "exit $code: $out"
out=$(LOREFS_BACKUP_SECRET="wrong" lore alpha backup setup --bucket "$COPY_BUCKET" --access-key "$S_KEY" 2>&1 </dev/null); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "refused the key" && [ ! -e "$T/alpha/config/backup.json" ] && ok "a wrong key is refused and nothing is saved" || bad "a wrong key is refused" "exit $code: $out"
out=$(LOREFS_BACKUP_SECRET="$S_SECRET" lore alpha backup setup --bucket "http://127.0.0.1:$MINIO_PORT/nosuchbucket" --access-key "$S_KEY" 2>&1 </dev/null); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "does not exist" && ok "a bucket that is not there is reported" || bad "a missing bucket is reported" "exit $code: $out"
out=$(LOREFS_BACKUP_SECRET="$S_SECRET" lore alpha backup setup --bucket "$COPY_BUCKET" --access-key "$S_KEY" 2>&1 </dev/null); code=$?
[ $code -eq 0 ] && ok "a destination that can be written is saved" || bad "a destination that can be written is saved" "exit $code: $out"
echo "$out" | grep -q "removes objects at once" && echo "$out" | grep -q "versioning" && ok "setup says the bucket does not keep removed objects, and what to turn on" || bad "setup says whether removed objects are kept" "$out"
[ "$(stat -c %a "$T/alpha/config/backup.env")" = 600 ] && ! grep -q "$S_SECRET" <<<"$out" && ok "the keys are stored readable only by the user and never printed" || bad "the keys are stored privately"

section "Copying"
out=$(lore alpha backup run 2>&1); code=$?
[ $code -eq 0 ] && ok "the first run succeeds" || bad "the first run succeeds" "exit $code: $out"
src_n=$(objects drivebucket); dst_n=$(objects copybucket)
[ "$src_n" -gt 1200 ] && [ "$src_n" = "$dst_n" ] && ok "the copy holds as many objects as the drive's bucket ($dst_n)" || bad "the copy holds as many objects as the drive's bucket" "drive $src_n, copy $dst_n"
rec=$(idx 2 get lorefs:backup); echo "$rec" | jq -e '.ok == true and .objects > 1200 and .bytes > 3000000' >/dev/null && ok "the result is recorded in the index ($(echo "$rec" | jq -c '{objects,bytes}'))" || bad "the result is recorded in the index" "$rec"
echo "changed" >"$A/docs/special.md"; sync; sleep 2
out=$(lore alpha backup run 2>&1); n=$(echo "$out" | grep -o 'Copying [0-9]*' | grep -o '[0-9]*')
[ -n "$n" ] && [ "$n" -le 20 ] && ok "a later run copies only what changed ($n objects of $dst_n)" || bad "a later run copies only what changed" "$out"
[ "$(objects drivebucket)" = "$(objects copybucket)" ] && ok "and the two buckets still match" || bad "the buckets match after the second run" "$(objects drivebucket) vs $(objects copybucket)"

section "Every computer sees the result"
for m in alpha beta; do
  js=$(lore $m status --json 2>/dev/null | jq -c '.checks[] | select(.id == "file-backup")')
  echo "$js" | jq -e '.level == "ok"' >/dev/null && ok "status on $m shows the backup as fine" || bad "status on $m shows the backup" "$js"
done
idx 2 set lorefs:backup "{\"time\":\"$(date -u -d '-40 hours' +%Y-%m-%dT%H:%M:%SZ)\",\"ok\":true,\"objects\":1300,\"bytes\":3200000,\"destination\":\"x\"}" >/dev/null
for m in alpha beta; do lore $m status --json 2>/dev/null | jq -e '.checks[] | select(.id == "file-backup") | .level == "warning" and (.detail | test("old"))' >/dev/null && ok "status on $m warns when the copy is older than 36 hours" || bad "status on $m warns about an old copy"; done

section "Restoring the index from the drive's bucket"
out=$(lore alpha restore-index --list 2>&1); code=$?
n_copies=$(echo "$out" | grep -c 'dump-')
[ $code -eq 0 ] && [ "$n_copies" -ge 1 ] && ok "--list shows the copies of the index in the bucket ($n_copies)" || bad "--list shows the copies" "exit $code: $out"
mount_new() { # mount_new DB DIR : read-only mount of a restored index
  mkdir -p "$2"; META_PASSWORD="$IDX_PW" "$JFS" mount -d --read-only --no-bgjob --backup-meta 0 --no-usage-report "redis://127.0.0.1:$NEW_PORT/$1" "$2" --cache-dir "$T/cache-$1" >"$T/mount-new-$1.log" 2>&1
  within 30 test -e "$2/.config"
}
out=$(lore alpha restore-index --index "redis://127.0.0.1:$NEW_PORT/1" 2>&1 </dev/null); code=$?
# (the new index is plain, so it is given as redis://: restore accepts it)
[ $code -eq 0 ] && ok "restore-index rebuilds an empty index from the newest copy" || bad "restore-index into an empty index" "exit $code: $out"
if mount_new 1 "$T/restored1"; then
  [ "$(find "$T/restored1/docs" -name 'd*.md' | wc -l)" -ge 1200 ] && grep -q -E '^(original|changed)$' "$T/restored1/docs/special.md" \
    && ok "every file that existed at the time of the copy is listed and readable ($(find "$T/restored1/docs" -type f | wc -l) in docs)" || bad "restored files are listed and readable"
  fusermount3 -uz "$T/restored1"
else bad "the restored index mounts" "$(tail -3 "$T/mount-new-1.log")"; fi
out=$(lore alpha restore-index --index "redis://127.0.0.1:$NEW_PORT/1" 2>&1 </dev/null); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "already holds a drive" && ok "restoring over an index that holds a drive is refused" || bad "restoring over a drive is refused" "exit $code: $out"
out=$(lore alpha restore-index --drill 2>&1); code=$?
echo "$out" | grep -q "identical" && [ $code -eq 0 ] && ok "the drill compares with the live drive: $(echo "$out" | grep -o '[0-9]* identical, [0-9]* different, [0-9]* no longer')" || bad "the drill reports identical files" "exit $code: $out"
left=$(ls -d /tmp/lorefs-drill-* 2>/dev/null | wc -l); [ "$left" = 0 ] && ! grep -q lorefs-drill /proc/self/mounts && ok "the drill left no temporary folder or mount behind" || bad "the drill left nothing behind" "$left folders"
[ "$(idx 1 dbsize | tr -dc 0-9)" -gt 0 ] && ok "the real index was not changed by the drill" || bad "the real index is intact"

section "A run that must not mirror a loss"
src_before=$(objects copybucket)
# Wipe the drive's stored blocks, as a mistake or an attack would. MinIO keeps a listing cache, so it
# is restarted afterwards to make it look at its folder again.
rm -rf "${T:?}/minio/drivebucket/$DRIVE/chunks"
docker restart "$MINIO" >/dev/null; within 30 curl -sf "http://127.0.0.1:$MINIO_PORT/minio/health/live"
wiped=$(objects drivebucket)
out=$(lore alpha backup run 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "check the drive's bucket" && ok "the run stops and says to check the drive's bucket (drive $wiped objects, copy $src_before)" || bad "the run stops when the drive's bucket was emptied" "exit $code: $out"
[ "$(objects copybucket)" = "$src_before" ] && ok "the copy was left untouched" || bad "the copy was left untouched" "$src_before -> $(objects copybucket)"
for m in alpha beta; do lore $m status --json 2>/dev/null | jq -e '.checks[] | select(.id == "file-backup") | .level == "warning" and (.detail | test("check the drive"))' >/dev/null && ok "status on $m shows the reason" || bad "status on $m shows why the backup failed"; done

section "A refused upload is reported"
cp "$T/alpha/config/backup.env" "$T/backup.env.good"
sed -i 's/^BACKUP_SECRET_KEY=.*/BACKUP_SECRET_KEY=wrong/' "$T/alpha/config/backup.env"
out=$(lore alpha backup run 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -qi "refused the key\|failed" && ok "a refused key is reported with its reason" || bad "a refused key is reported" "exit $code: $out"
cp "$T/backup.env.good" "$T/alpha/config/backup.env"

section "Restoring from the backup alone (the drive's bucket is lost)"
rm -rf "$T/minio/drivebucket"
out=$(lore alpha restore-index --list --from-backup 2>&1); code=$?
[ $code -eq 0 ] && echo "$out" | grep -q 'dump-' && ok "--from-backup lists the copies held in the backup bucket" || bad "--from-backup lists the copies" "exit $code: $out"
out=$(lore alpha restore-index --from-backup --index "redis://127.0.0.1:$NEW_PORT/2" 2>&1 </dev/null); code=$?
[ $code -eq 0 ] && ok "the index is rebuilt from the backup" || bad "the index is rebuilt from the backup" "exit $code: $out"
if mount_new 2 "$T/restored2"; then
  [ "$(find "$T/restored2/docs" -name 'd*.md' | wc -l)" -ge 1200 ] && [ "$(wc -c <"$T/restored2/docs/big.bin")" = 3000000 ] \
    && ok "with the drive's bucket gone, every file from the last backup is listed and readable" || bad "files are readable from the backup alone" "$(find "$T/restored2/docs" -type f | wc -l) files"
  fusermount3 -uz "$T/restored2"
else bad "the index restored from the backup mounts" "$(tail -3 "$T/mount-new-2.log")"; fi

section "The nightly timer"
if command -v systemctl >/dev/null && systemctl --user is-system-running >/dev/null 2>&1 || systemctl --user status >/dev/null 2>&1; then
  unset LOREFS_NO_SYSTEMD
  soon=$(date -d '+75 seconds' '+*-*-* %H:%M:%S')
  LOREFS_BACKUP_CALENDAR="$soon" LOREFS_CONFIG_DIR="$T/alpha/config" "$LORE" backup schedule >"$T/sched.out" 2>&1 && ok "backup schedule installs the timer" || bad "backup schedule installs the timer" "$(cat "$T/sched.out")"
  grep -q '^Persistent=true' "$HOME/.config/systemd/user/lorefs-backup.timer" && ok "the timer catches up after a missed run (Persistent=true)" || bad "the timer is persistent"
  systemctl --user list-timers lorefs-backup.timer --no-pager 2>/dev/null | grep -q lorefs-backup && ok "systemd lists the timer" || bad "systemd lists the timer"
  before=$(idx 2 get lorefs:backup | jq -r .time)
  within 150 eval '[ "$(idx 2 get lorefs:backup | jq -r .time)" != "'"$before"'" ]' && ok "the timer ran the backup by itself" || bad "the timer ran the backup" "$(journalctl --user -u lorefs-backup -n 5 --no-pager 2>&1 | tail -3)"
  LOREFS_CONFIG_DIR="$T/alpha/config" "$LORE" backup schedule --off >/dev/null 2>&1; [ ! -e "$HOME/.config/systemd/user/lorefs-backup.timer" ] && ok "backup schedule --off removes it" || bad "backup schedule --off removes it"
  export LOREFS_NO_SYSTEMD=1
else
  echo "  (no systemd user session here; the timer checks are skipped)"
fi

echo
printf '\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -gt 0 ] && { printf '  failed: %s\n' "${FAILED[@]}"; }
cleanup
[ "$FAIL" = 0 ]
