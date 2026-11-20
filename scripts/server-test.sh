#!/usr/bin/env bash
# Checks the index bundle made by `lorefs server init`: it is started with
# Docker and tested the way a user's index would be attacked or fail --
# plain connections, a wrong password, a certificate for another address,
# a restart, a full index -- and a drive is created on it with MinIO as
# storage and joined from a second pretend computer over TLS.
#
# Usage: scripts/server-test.sh [--cleanup-only]
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LORE="$ROOT/bin/lorefs"
T="${LOREFS_STEST_DIR:-/tmp/lorefs-stest}"
MINIO=lorefs-stest-minio; MINIO_PORT=19100
PORT=16420; PORT_SMALL=16421
S_KEY=stestaccesskey; S_SECRET=stest-storage-secret-123
BUCKET_URL="http://127.0.0.1:$MINIO_PORT/stestbucket"

"$ROOT/scripts/get-juicefs.sh" "$ROOT/.tools" >/dev/null || { echo "could not get JuiceFS"; exit 1; }
JFS="$ROOT/.tools/juicefs"
export LOREFS_JUICEFS="$JFS" LOREFS_NO_SYSTEMD=1 LOREFS_NO_NOTIFY=1
export LOREFS_QUIET=300ms LOREFS_MAX_WAIT=20s LOREFS_JOURNAL_FLUSH=300ms LOREFS_FOLLOW=2s LOREFS_HEALTH=2s

PASS=0; FAIL=0; FAILED=()
ok()  { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
within() { local s="$1"; shift; local end=$((SECONDS+s)); until "$@" >/dev/null 2>&1; do [ $SECONDS -ge $end ] && return 1; sleep 0.3; done; }

as() { local m="$1"; shift; LOREFS_CONFIG_DIR="$T/$m/config" LOREFS_STATE_DIR="$T/$m/state" LOREFS_DATA_DIR="$T/$m/data" "$@"; }
lore() { local m="$1"; shift; as "$m" "$LORE" "$@"; }
start_mount() { local m="$1"; as "$m" "$LORE" mount pre; as "$m" "$LORE" mount run >"$T/$m/mount.log" 2>&1 & within 60 test -e "$T/$m/Lore/.config"; }
stop_mount() { local m="$1"; "$JFS" umount "$T/$m/Lore" >/dev/null 2>&1 || fusermount3 -uz "$T/$m/Lore" 2>/dev/null; sleep 1; }
compose() { local d="$1"; shift; docker compose -p "lorefs-stest-$(basename "$d")" -f "$d/compose.yaml" "$@"; }
check_index() { # check_index DIR PORT [extra index-check args]  -> output
  local d="$1" p="$2"; shift 2
  META_PASSWORD="${PW:-$(cat "$d/secrets/index-password")}" "$LORE" index-check --host 127.0.0.1 --port "$p" --ca-file "$d/tls/ca.crt" "$@" 2>&1
}

cleanup() {
  for m in alpha beta; do stop_mount "$m" 2>/dev/null; done
  pkill -f "$JFS mount" 2>/dev/null
  for d in main small; do [ -f "$T/$d/compose.yaml" ] && compose "$T/$d" down -v >/dev/null 2>&1; done
  docker rm -f "$MINIO" >/dev/null 2>&1
  chmod -R u+rwx "$T" 2>/dev/null; rm -rf "$T"
}
trap 'echo; echo "interrupted"; cleanup; exit 130' INT TERM
cleanup 2>/dev/null
[ "${1:-}" = --cleanup-only ] && { echo "cleaned up $T"; exit 0; }
mkdir -p "$T/minio" "$T/alpha" "$T/beta"

section "The bundle"
"$LORE" server init --address 127.0.0.1 --port "$PORT" --name stest-drive "$T/main" >"$T/init.out" 2>&1 && ok "lorefs server init writes a bundle" || { bad "lorefs server init writes a bundle" "$(cat "$T/init.out")"; cleanup; exit 1; }
"$LORE" server init --address 127.0.0.1 "$T/main" >"$T/init2.out" 2>&1; [ $? -ne 0 ] && grep -q "already exists" "$T/init2.out" && ok "an existing folder is refused" || bad "an existing folder is refused" "$(cat "$T/init2.out")"
"$LORE" server init --address 127.0.0.1 --port "$PORT_SMALL" --memory-mb 5 --name stest-small "$T/small" >/dev/null 2>&1 || bad "a second bundle with a small memory limit"
for d in main small; do compose "$T/$d" up -d >"$T/up-$d.log" 2>&1 || { bad "docker compose up ($d)" "$(cat "$T/up-$d.log")"; cleanup; exit 1; }; done
within 60 check_index "$T/main" "$PORT" && ok "the index starts and accepts the right password over TLS" || bad "the index starts and accepts the right password over TLS" "$(check_index "$T/main" "$PORT")"
within 60 check_index "$T/small" "$PORT_SMALL" || bad "the small index starts"

section "Attacks and mistakes"
out=$(check_index "$T/main" "$PORT" --no-tls); echo "$out" | grep -q "refused" && ok "an unencrypted connection is refused" || bad "an unencrypted connection is refused" "$out"
out=$(PW=not-the-password check_index "$T/main" "$PORT"); echo "$out" | grep -q "rejected the password" && ok "a wrong password is rejected" || bad "a wrong password is rejected" "$out"
out=$(META_PASSWORD="$(cat "$T/main/secrets/index-password")" "$LORE" index-check --host localhost --port "$PORT" --ca-file "$T/main/tls/ca.crt" 2>&1)
echo "$out" | grep -q "did not prove its identity" && ok "a certificate made for another address is refused by the client" || bad "a certificate made for another address is refused by the client" "$out"
"$LORE" server init --address 127.0.0.1 --name other-authority "$T/other" >/dev/null 2>&1
out=$(META_PASSWORD="$(cat "$T/main/secrets/index-password")" "$LORE" index-check --host 127.0.0.1 --port "$PORT" --ca-file "$T/other/tls/ca.crt" 2>&1)
echo "$out" | grep -q "did not prove its identity" && ok "a certificate from an unknown authority is refused" || bad "a certificate from an unknown authority is refused" "$out"
rm -rf "$T/other"
for _ in 1 2 3; do printf 'x' | timeout 3 bash -c "cat > /dev/tcp/127.0.0.1/$PORT" 2>/dev/null; done
check_index "$T/main" "$PORT" >/dev/null && ok "the index survives garbage on its port" || bad "the index survives garbage on its port"

section "A drive on the bundle (MinIO as storage) and a second computer over TLS"
docker run -d --name "$MINIO" --user "$(id -u):$(id -g)" -p "127.0.0.1:$MINIO_PORT:9000" -v "$T/minio:/data" \
  -e MINIO_ROOT_USER="$S_KEY" -e MINIO_ROOT_PASSWORD="$S_SECRET" cgr.dev/chainguard/minio:latest server /data >/dev/null
within 30 curl -sf "http://127.0.0.1:$MINIO_PORT/minio/health/live" || { echo "MinIO did not start"; cleanup; exit 1; }
LOREFS_STORAGE_KEY="$S_KEY" LOREFS_STORAGE_SECRET="$S_SECRET" lore alpha create --server-dir "$T/main" --bucket "$BUCKET_URL" \
  --name stest-drive --machine alpha --folder "$T/alpha/Lore" --connect </dev/null >"$T/create.out" 2>"$T/create.err" \
  && ok "lorefs create --server-dir makes a drive from the bundle's address, password and certificate" || bad "create --server-dir" "$(cat "$T/create.err")"
grep -q "allow-unencrypted\|not encrypted" "$T/create.err" && bad "no unencrypted warning for a TLS index" || ok "no unencrypted-connection warning for the bundle (it is encrypted)"
start_mount alpha && ok "the drive mounts over TLS" || bad "the drive mounts over TLS" "$(tail -3 "$T/alpha/mount.log")"
grep '^lorefs2_' "$T/create.out" | lore beta join --token-stdin --name beta --folder "$T/beta/Lore" --cache-dir "$T/beta/cache" >"$T/join.log" 2>&1 \
  && ok "a second pretend computer joins with the printed code" || bad "a second pretend computer joins" "$(cat "$T/join.log")"
start_mount beta && ok "and mounts the drive over TLS" || bad "and mounts the drive over TLS" "$(tail -3 "$T/beta/mount.log")"
echo "from alpha" >"$T/alpha/Lore/one.txt"
within 10 grep -q "from alpha" "$T/beta/Lore/one.txt" && ok "files cross between the two computers" || bad "files cross between the two computers"
out=$(check_index "$T/main" "$PORT"); echo "$out" | grep -q 'holds the drive "stest-drive"' && ok "the index reports the drive it holds" || bad "the index reports the drive it holds" "$out"

section "Restart"
stop_mount beta; stop_mount alpha
compose "$T/main" restart >/dev/null 2>&1
within 60 check_index "$T/main" "$PORT" || bad "the index comes back after a restart"
out=$(check_index "$T/main" "$PORT"); echo "$out" | grep -q 'holds the drive "stest-drive"' && ok "data survives a container restart" || bad "data survives a container restart" "$out"
start_mount alpha && grep -q "from alpha" "$T/alpha/Lore/one.txt" && ok "the drive remounts and the file is still there" || bad "the drive remounts and the file is still there" "$(tail -3 "$T/alpha/mount.log")"
stop_mount alpha

section "A full index"
LOREFS_STORAGE_KEY="$S_KEY" LOREFS_STORAGE_SECRET="$S_SECRET" lore alpha leave --yes --keep-cache >/dev/null 2>&1
LOREFS_STORAGE_KEY="$S_KEY" LOREFS_STORAGE_SECRET="$S_SECRET" lore alpha create --server-dir "$T/small" --bucket "http://127.0.0.1:$MINIO_PORT/stestsmall" \
  --name stest-small --machine alpha --folder "$T/alpha/Lore" --connect </dev/null >"$T/create-small.out" 2>"$T/create-small.err" \
  && ok "a drive is created on the index with a small memory limit (5 MB)" || bad "a drive on the small index" "$(cat "$T/create-small.err")"
start_mount alpha || bad "the small drive mounts" "$(tail -3 "$T/alpha/mount.log")"
echo "kept" >"$T/alpha/Lore/existing.txt"; sync
failed=0; end=$((SECONDS+600)); n=0
while [ $SECONDS -lt $end ]; do
  # A create that the index cannot take fails after about 20 s, so each batch is cut off.
  seq "$((n+1))" "$((n+300))" | timeout 60 xargs -P 16 -I{} sh -c 'mkdir -p "$1/fill/$(( $2 % 50 ))" && : > "$1/fill/$(( $2 % 50 ))/f$2"' _ "$T/alpha/Lore" {} 2>/dev/null
  n=$((n+300))
  if ! timeout 60 sh -c "echo probe >'$T/alpha/Lore/probe-$n'" 2>/dev/null; then failed=1; break; fi
done
[ $failed = 1 ] && ok "a full index refuses new files (after about $n files) instead of crashing" || bad "a full index refuses new files" "still accepting after $n files"
[ "$(cat "$T/alpha/Lore/existing.txt" 2>/dev/null)" = kept ] && ok "files that existed are still readable when the index is full" || bad "files that existed are still readable when the index is full"
used=$(PW="$(cat "$T/small/secrets/index-password")" check_index "$T/small" "$PORT_SMALL" | grep -o 'memory:.*')
check_index "$T/small" "$PORT_SMALL" >/dev/null && ok "the index is still answering ($used)" || bad "the index is still answering"
stop_mount alpha

echo
printf '\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -gt 0 ] && { printf '  failed: %s\n' "${FAILED[@]}"; }
cleanup
[ "$FAIL" = 0 ]
