#!/usr/bin/env bash
# Rehearsal of a colleague joining: builds a release, installs it into two
# empty pretend home folders through the one-line installer (served from a
# local folder), creates a throwaway drive (MinIO for the files, Redis for
# the index) with `lorefs create` as the first computer, joins it with the
# printed code as the second, and checks that the two work together with
# the real timings of the program (no shortened waits, no systemd).
#
# Run it before publishing a release. It does not touch any real drive.
#
# Usage: scripts/rehearse-join.sh [--cleanup-only]
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2
export PATH="$HOME/.local/share/mise/installs/go/1.27.4/bin:$PATH"; unset GOROOT

C="${REHEARSE_DIR:-/tmp/lorefs-rehearsal}"
NAME="second-$(date +%H%M%S)"
H1="$C/home1"; H2="$C/home2"
K="$H1/Lore"; B="$H2/Lore"
T="rehearsal-$(date +%Y%m%d-%H%M%S)" # the test folder on the drive
MINIO=lorefs-rehearse-minio; IDX=lorefs-rehearse-index; HTTP_PORT=18091
S_KEY=rehearsekey; S_SECRET=rehearse-storage-secret-123; I_PW=rehearse-index-password-xyz
export LOREFS_NO_SYSTEMD=1 LOREFS_NO_NOTIFY=1
PASS=0; FAIL=0; FAILED=()
ok() { PASS=$((PASS + 1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); FAILED+=("$1"); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
within() { local s="$1"; shift; local end=$((SECONDS + s)); until eval "$*" >/dev/null 2>&1; do [ $SECONDS -ge $end ] && return 1; sleep 0.5; done; }
sim() { HOME="$H2" "$@"; }
mounted() { grep -q " $B fuse.juicefs " /proc/self/mounts; }
versions() { "$LORE" history --json "$1" 2>/dev/null | jq '.versions | length' 2>/dev/null || echo 0; }
newest_by() { "$LORE" history --json "$1" 2>/dev/null | jq -r '.versions[0].machine'; }
mounted1() { grep -q " $K fuse.juicefs " /proc/self/mounts; }

cleanup() {
  section "Clearing up"
  [ -f "$C/helper1.pid" ] && kill "$(cat "$C/helper1.pid")" 2>/dev/null
  [ -f "$C/helper2.pid" ] && kill "$(cat "$C/helper2.pid")" 2>/dev/null
  [ -f "$C/http.pid" ] && kill "$(cat "$C/http.pid")" 2>/dev/null
  sleep 1
  for m in "$B" "$K"; do fusermount3 -uz "$m" 2>/dev/null; done
  sleep 1; pkill -f "juicefs mount redis://127.0.0.1:16490" 2>/dev/null
  docker rm -f "$MINIO" "$IDX" >/dev/null 2>&1
  chmod -R u+rwx "$C" 2>/dev/null; rm -rf "$C"
}
trap cleanup EXIT
[ "${1:-}" = --cleanup-only ] && { trap - EXIT; cleanup; exit 0; }
cleanup >/dev/null 2>&1
mkdir -p "$C/rel" "$H1" "$H2" "$C/minio"

section "Building a release and installing it as colleagues would"
scripts/make-release.sh 0.0.9 >"$C/make.log" 2>&1 && cp dist/release/* "$C/rel/" || { bad "make release" "$(tail -3 "$C/make.log")"; exit 1; }
(cd "$C/rel" && exec python3 -m http.server "$HTTP_PORT" --bind 127.0.0.1 >"$C/http.log" 2>&1) & echo $! >"$C/http.pid"
within 10 curl -sf "http://127.0.0.1:$HTTP_PORT/SHA256SUMS" || { bad "the release server starts"; exit 1; }
export LOREFS_RELEASE_URL="http://127.0.0.1:$HTTP_PORT"
for n in 1 2; do
  h="$C/home$n"
  out=$(HOME="$h" sh "$C/rel/install.sh" 2>&1) && [ -x "$h/.local/bin/lorefs" ] && [ -x "$h/.local/bin/juicefs" ] \
    && ok "the one-line installer installs lorefs and the storage engine for computer $n" || bad "the one-line installer for computer $n" "$(echo "$out" | tail -3)"
  printf '#!/bin/sh\nexport HOME="%s"\nexec "%s/.local/bin/lorefs" "$@"\n' "$h" "$h" >"$C/lore$n"; chmod +x "$C/lore$n"
done
LORE="$C/lore1"; CL="$C/lore2"
echo "$out" | grep -q "lorefs join" && ok "the installer says what to do next" || bad "the installer says what to do next"
[ "$("$LORE" version | head -1)" = "$("$CL" version | head -1)" ] && ok "both computers run the same version ($("$LORE" version | head -1))" || bad "both computers run the same version"

section "Creating the drive on computer 1 and joining from computer 2"
docker run -d --name "$MINIO" --user "$(id -u):$(id -g)" -p 127.0.0.1:19400:9000 -v "$C/minio:/data" \
  -e MINIO_ROOT_USER="$S_KEY" -e MINIO_ROOT_PASSWORD="$S_SECRET" cgr.dev/chainguard/minio:latest server /data >/dev/null || exit 2
docker run -d --name "$IDX" -p 127.0.0.1:16490:6379 redis:8-alpine redis-server --requirepass "$I_PW" --appendonly no >/dev/null || exit 2
within 30 curl -sf http://127.0.0.1:19400/minio/health/live || { bad "MinIO starts"; exit 2; }
within 20 docker exec "$IDX" redis-cli -a "$I_PW" --no-auth-warning ping
LOREFS_STORAGE_KEY="$S_KEY" LOREFS_STORAGE_SECRET="$S_SECRET" LOREFS_INDEX_PASSWORD="$I_PW" "$LORE" create \
  --bucket http://127.0.0.1:19400/rehearsebucket --index redis://127.0.0.1:16490/1 --allow-unencrypted-index --name rehearse-drive --machine first \
  --folder "$K" --connect </dev/null >"$C/create.out" 2>"$C/create.err" && ok "lorefs create makes the drive and connects computer 1" || { bad "lorefs create" "$(tail -3 "$C/create.err")"; exit 1; }
"$LORE" mount pre >/dev/null 2>&1
(HOME="$H1" setsid "$H1/.local/bin/lorefs" mount run >"$C/mount1.log" 2>&1 &)
within 40 mounted1 && ok "computer 1's drive connects" || { bad "computer 1's drive connects" "$(tail -3 "$C/mount1.log")"; exit 1; }
(HOME="$H1" setsid "$H1/.local/bin/lorefs" helper run >"$C/helper1.log" 2>&1 & echo $! >"$C/helper1.pid")

section "Joining with a join code"
out=$("$LORE" invite --quiet | sim "$CL" join --token-stdin --name "$NAME" 2>&1) && ok "lorefs join accepts a fresh code from lorefs invite" || bad "lorefs join accepts a fresh code from lorefs invite" "$(echo "$out" | tail -3)"
[ "$(stat -c %a "$H2/.config/lorefs/secret.env")" = 600 ] && ok "the saved secret is readable by the owner only" || bad "the saved secret is readable by the owner only"
( echo x >"$B/should-fail" ) 2>/dev/null && bad "before connecting, the folder refuses files" || ok "before connecting, the folder refuses files"

# No systemd in the pretend home, so start what the services would start.
sim "$CL" mount pre >/dev/null 2>&1
(HOME="$H2" setsid "$H2/.local/bin/lorefs" mount run >"$C/mount.log" 2>&1 &)
within 30 mounted && ok "the drive connects" || { bad "the drive connects" "$(tail -3 "$C/mount.log")"; exit 1; }
(HOME="$H2" setsid "$H2/.local/bin/lorefs" helper run >"$C/helper.log" 2>&1 & echo $! >"$C/helper2.pid")
t0=$SECONDS
within 60 'sim "$CL" status >/dev/null' && ok "lorefs status reports that everything is working ($((SECONDS - t0)) s after connecting)" || bad "lorefs status reports that everything is working" "$(sim "$CL" status | grep -v '✓' | head -5)"
[ "$(ls "$B" | sort | tr '\n' ' ')" = "$(ls "$K" | sort | tr '\n' ' ')" ] && ok "the second computer sees the same files" || bad "the second computer sees the same files"
grep -q "LoreFS" "$B/AGENTS.md" && ok "the rules file for agents is readable there" || bad "the rules file for agents is readable there"
within 20 'sim "$CL" status --json | jq -e ".checks[] | select(.id == \"program\") | .detail | test(\"the same on all [0-9]+ computers\")"' \
  && ok "lorefs status there says every computer runs the same version of lorefs" \
  || bad "lorefs status there says every computer runs the same version of lorefs" "$(sim "$CL" status | grep Program) (is the package the version installed here?)"

section "Speed check (lorefs selftest on the second computer)"
if out=$(sim timeout 300 "$CL" selftest --settle 5s 2>&1); then ok "selftest passes"; echo "$out" | sed 's/^/      /' | tail -8; else bad "selftest passes" "$(echo "$out" | tail -4)"; fi

section "Two computers on one drive"
mkdir "$K/$T"
echo "from this computer" >"$K/$T/a.md"; t0=$(date +%s.%N)
within 5 'grep -q "from this computer" "$B/$T/a.md"' && ok "a file saved here can be read there ($(awk -v s="$t0" -v e="$(date +%s.%N)" 'BEGIN{printf "%.1f s", e-s}'))" || bad "a file saved here can be read there"
echo "from the second computer" >"$B/$T/b.md"; t0=$(date +%s.%N)
within 5 'grep -q "second computer" "$K/$T/b.md"' && ok "a file saved there can be read here ($(awk -v s="$t0" -v e="$(date +%s.%N)" 'BEGIN{printf "%.1f s", e-s}'))" || bad "a file saved there can be read here"
cat "$B/$T/a.md" >/dev/null; echo "edited here" >"$K/$T/a.md"
within 5 'grep -q "edited here" "$B/$T/a.md"' && ok "an edit made here shows there within 5 seconds" || bad "an edit made here shows there within 5 seconds"
(set -o noclobber; echo k >"$K/$T/claim") 2>/dev/null & p1=$!
(set -o noclobber; echo b >"$B/$T/claim") 2>/dev/null & p2=$!
wait $p1; r1=$?; wait $p2; r2=$?
[ $((r1 + r2)) = 1 ] && ok "when both create the same new file at once, exactly one succeeds" || bad "when both create the same new file at once, exactly one succeeds" "$r1 $r2"

section "Version history across the two computers"
within 15 '[ "$(versions "$T/a.md")" -ge 1 ]' && ok "this computer records its own saves" || bad "this computer records its own saves"
# The folder was made here seconds ago. Edit there at once, on purpose.
echo "edited on the second computer, straight away" >"$B/$T/a.md"
within 90 '[ "$(newest_by "$T/a.md")" = "$NAME" ]' && ok "an edit there, in a folder made here seconds earlier, gets its version" || bad "an edit there, in a folder made here seconds earlier, gets its version" "newest by: $(newest_by "$T/a.md")"
n=$(versions "$T/a.md"); echo "edited again on the second computer" >"$B/$T/a.md"
within 15 '[ "$(versions "$T/a.md")" -gt '"$n"' ]' && ok "once it knows the folder, each edit there is recorded within seconds" || bad "once it knows the folder, each edit there is recorded within seconds"
machines=$("$LORE" history --json "$T/a.md" | jq -r '[.versions[].machine] | unique | join(" ")')
[ "$machines" = "$(printf '%s\n' "$NAME" "$("$LORE" status --json | jq -r .machine)" | sort | tr '\n' ' ' | sed 's/ $//')" ] && ok "the history names both computers ($machines)" || bad "the history names both computers" "$machines"
"$LORE" restore "$T/a.md" 2 >/dev/null
within 8 'grep -q "straight away" "$B/$T/a.md"' && ok "restoring an older version here changes the file there" || bad "restoring an older version here changes the file there" "$(cat "$B/$T/a.md")"
# A computer may show another's newest log lines a few seconds late.
within 10 'sim "$CL" log -n 50 | grep -q "$NAME" && sim "$CL" log -n 50 | grep -q "restored"' && ok "the activity log there shows both computers and the restore" || bad "the activity log there shows both computers and the restore"
# Both people save the same file seconds apart.
echo "both start here" >"$K/$T/shared.md"; within 15 '[ "$(versions "$T/shared.md")" -ge 1 ]'; cat "$B/$T/shared.md" >/dev/null; sleep 3
echo "saved here first" >"$K/$T/shared.md"; sleep "${REHEARSE_GAP:-2}"; echo "saved there a moment later" >"$B/$T/shared.md"
within 30 'grep -q "there a moment later" "$K/$T/shared.md" && [ "$(versions "$T/shared.md")" -ge 3 ]' \
  && "$LORE" history --json "$T/shared.md" | jq -e '[.versions[].machine] | unique | length == 2' >/dev/null \
  && ok "two saves of one file seconds apart: the later one wins and both are in the history" \
  || bad "two saves of one file seconds apart: the later one wins and both are in the history" "$(cat "$K/$T/shared.md"); $("$LORE" history --json "$T/shared.md" | jq -c '[.versions[] | {machine, size}]')"
n=$(versions "$T/shared.md"); first=$("$LORE" history --json "$T/shared.md" | jq -r '[.versions[] | select(.size == 17)][0].number')
sim "$CL" restore "$T/shared.md" "$first" >/dev/null
within 8 'grep -q "saved here first" "$K/$T/shared.md"' && within 15 '[ "$(versions "$T/shared.md")" -gt '"$n"' ]' \
  && ok "the other person can bring back the earlier save, and that restore is itself in the history" \
  || bad "the other person can bring back the earlier save" "$(cat "$K/$T/shared.md")"
mv "$K/$T/a.md" "$K/$T/renamed.md"; sleep 3
within 15 '[ "$(versions "$T/renamed.md")" -ge 3 ]' && ok "a renamed file keeps its history" || bad "a renamed file keeps its history" "$(versions "$T/renamed.md")"

section "Videos and the trash"
head -c 600000 /dev/urandom >"$B/$T/reel.mp4"; cp "$B/$T/reel.mp4" "$C/reel.expected"
within 8 'cmp -s "$C/reel.expected" "$K/$T/reel.mp4"' && ok "a video saved there arrives here intact" || bad "a video saved there arrives here intact"
sleep 5; [ "$(versions "$T/reel.mp4")" = 0 ] && ok "the video gets no versions" || bad "the video gets no versions"
rm "$B/$T/reel.mp4"; sleep 2
"$LORE" trash reel | grep -q "reel.mp4" && "$LORE" trash restore reel.mp4 --to "$K/$T" >/dev/null && cmp -s "$C/reel.expected" "$K/$T/reel.mp4" \
  && ok "a video deleted there is brought back from the trash here" || bad "a video deleted there is brought back from the trash here" "$("$LORE" trash reel | head -3)"

section "Fast folders"
for i in 1 2 3 4 5 6 7 8; do echo "small note $i" >"$K/$T/n$i.md"; done
pct() { "$H2/.local/bin/juicefs" warmup --check "$1" 2>&1 | grep -o '([0-9.]*%) cached' | tr -d '()% cached'; }
within 60 '[ "$(pct "$B/$T")" = "100.0" ]' && ok "notes saved here are ready there without being opened" || bad "notes saved here are ready there without being opened" "ready: $(pct "$B/$T")%"

if [ -n "${REHEARSE_HOOK:-}" ]; then
  section "Extra checks ($REHEARSE_HOOK)"
  if K="$K" B="$B" LORE="$LORE" CL="$CL" SIM_HOME="$H2" "$REHEARSE_HOOK" 2>&1 | sed 's/^/      /'; [ "${PIPESTATUS[0]}" = 0 ]; then ok "the extra checks passed"; else bad "the extra checks passed"; fi
fi

section "Leaving"
kill "$(cat "$C/helper2.pid")" 2>/dev/null; sleep 1
out=$(sim "$CL" leave --yes --delete-cache 2>&1); sleep 2
mounted && { fusermount3 -u "$B" 2>/dev/null; sleep 1; }
! mounted && [ ! -e "$H2/.config/lorefs/secret.env" ] && ok "lorefs leave disconnects and removes the saved secret" || bad "lorefs leave disconnects and removes the saved secret" "$(echo "$out" | tail -3)"
[ ! -e "$K/.lorefs/machines/$NAME.json" ] && ok "the computer that left no longer counts in the version line" || bad "the computer that left no longer counts in the version line"
grep -q "small note 3" "$K/$T/n3.md" && ok "the files are still on the drive after the other computer left" || bad "the files are still on the drive after the other computer left"

echo
echo "Not rehearsed here, because they need the colleague's own login session:"
echo "  starting by itself at login, the desktop notifications, and the file"
echo "  manager's right-click entry. All three are in use on this computer."
printf '\n\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
for f in "${FAILED[@]:-}"; do [ -n "$f" ] && echo "  failed: $f"; done
[ "$FAIL" = 0 ]
