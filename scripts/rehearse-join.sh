#!/usr/bin/env bash
# Rehearsal of a colleague joining: installs the release package into an
# empty pretend home folder on this computer, joins the REAL drive with a
# fresh join code as a second computer, and checks that the two computers
# work together. Run it before sending the package to someone.
#
# It differs from itest.sh, which uses a throwaway drive on this computer
# and shortened waiting times. This uses the real index and storage, the
# real package and the real timings.
#
# Usage: scripts/rehearse-join.sh [package.tar.gz]
#   (default: the newest dist/lorefs-*.tar.gz)
#
# It creates a folder "rehearsal-<time>" on the drive and removes it at
# the end. The files it deletes stay in the drive's trash for 7 days.
#
# Environment:
#   REHEARSE_DIR   where the pretend home folder is made (default
#                  ~/.cache; it should be on a disk, because the second
#                  computer's cache of downloaded files lives in it)
#   REHEARSE_HOOK  a program to run while both computers are connected,
#                  for extra checks on real data. It gets K (the drive
#                  here), B (the drive on the pretend computer), LORE and
#                  CL (the lorefs program of each) in its environment, and
#                  counts as one more check: it passes if it exits with 0.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2

PKG="${1:-$(ls -t dist/lorefs-*-linux-amd64.tar.gz 2>/dev/null | head -1)}"
[ -f "$PKG" ] || { echo "No package found. Run: make dist" >&2; exit 2; }
LORE="${LORE:-$HOME/.local/bin/lorefs}"
K="$("$LORE" status --json 2>/dev/null | jq -r '.folder // empty')"
[ -n "$K" ] && grep -q " $K fuse.juicefs " /proc/self/mounts || { echo "The drive is not connected on this computer. Run: lorefs status" >&2; exit 2; }

NAME="rehearsal-$(date +%H%M%S)"
C="$(mktemp -d "${REHEARSE_DIR:-$HOME/.cache}/lorefs-rehearsal.XXXXXX")"
B="$C/home/Lore"
CL="$C/home/.local/bin/lorefs"
T="rehearsal-$(date +%Y%m%d-%H%M%S)" # the test folder on the drive
export LOREFS_NO_SYSTEMD=1 LOREFS_NO_NOTIFY=1
sim() { HOME="$C/home" "$@"; }

PASS=0; FAIL=0; FAILED=()
ok() { PASS=$((PASS + 1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); FAILED+=("$1"); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
within() { local s="$1"; shift; local end=$((SECONDS + s)); until eval "$*" >/dev/null 2>&1; do [ $SECONDS -ge $end ] && return 1; sleep 0.5; done; }
versions() { "$LORE" history --json "$1" 2>/dev/null | jq '.versions | length' 2>/dev/null || echo 0; }
newest_by() { "$LORE" history --json "$1" 2>/dev/null | jq -r '.versions[0].machine'; }
mounted() { grep -q " $B fuse.juicefs " /proc/self/mounts; }

cleanup() {
  section "Clearing up"
  if [ "$FAIL" != 0 ] || [ -n "${KEEP_LOGS:-}" ]; then
    L="/tmp/lorefs-rehearsal-logs-$(date +%H%M%S)"; mkdir -p "$L"
    cp "$C"/*.log "$L"/ 2>/dev/null; cp "$C/home/.local/state/lorefs/"*.log "$L"/ 2>/dev/null
    echo "  logs of the pretend second computer kept in $L"
  fi
  pkill -f "$CL helper run" 2>/dev/null
  if mounted; then sim "$CL" leave --yes --delete-cache >/dev/null 2>&1; fi
  mounted && { fusermount3 -u "$B" 2>/dev/null || fusermount -u "$B" 2>/dev/null; sleep 1; }
  pkill -f "$CL mount run" 2>/dev/null
  if mounted; then
    echo "  The rehearsal drive is still connected at $B; NOT deleting $C. Disconnect it with: fusermount3 -u $B"
  else
    chmod -R u+w "$C" 2>/dev/null
    rm -rf --one-file-system "$C" && echo "  removed the pretend home folder"
  fi
  # The rehearsal's traces on the real drive.
  if [ -d "$K/$T" ]; then rm -rf "${K:?}/$T" && echo "  removed $T from the drive"; fi
  rm -rf "${K:?}/.lorefs/history/$T" "${K:?}/.lorefs/journal/$NAME" 2>/dev/null
}
trap cleanup EXIT

echo "Package:  $PKG"
echo "Drive:    $K (this computer), $B (pretend second computer \"$NAME\")"

section "Installing from the package, as the colleague would"
(cd "$(dirname "$PKG")" && sha256sum -c --quiet "$(basename "$PKG").sha256") && ok "the package matches its checksum" || bad "the package matches its checksum"
mkdir -p "$C/home" "$C/unpack"
tar -C "$C/unpack" -xzf "$PKG"
D="$(echo "$C"/unpack/*/)"
[ -x "$D/install.sh" ] && [ -x "$D/lorefs" ] && [ -f "$D/README.txt" ] && ok "the package holds the installer, the program and the instructions" || bad "the package holds the installer, the program and the instructions" "$(ls "$D")"
out=$(sim "$D/install.sh" 2>&1) && [ -x "$CL" ] && [ -x "$C/home/.local/bin/juicefs" ] && ok "install.sh installs lorefs and the storage engine" || bad "install.sh installs lorefs and the storage engine" "$(echo "$out" | tail -3)"
echo "$out" | grep -q "lorefs join" && ok "the installer says what to do next" || bad "the installer says what to do next"
[ "$(sim "$CL" version | head -1)" = "$("$LORE" version | head -1)" ] && ok "both computers run the same version ($("$LORE" version | head -1))" || bad "both computers run the same version" "$(sim "$CL" version | head -1) vs $("$LORE" version | head -1)"

section "Joining with a join code"
out=$("$LORE" invite --quiet | sim "$CL" join --token-stdin --name "$NAME" 2>&1) && ok "lorefs join accepts a fresh code from lorefs invite" || bad "lorefs join accepts a fresh code from lorefs invite" "$(echo "$out" | tail -3)"
[ "$(stat -c %a "$C/home/.config/lorefs/secret.env")" = 600 ] && ok "the saved secret is readable by the owner only" || bad "the saved secret is readable by the owner only"
( echo x >"$B/should-fail" ) 2>/dev/null && bad "before connecting, the folder refuses files" || ok "before connecting, the folder refuses files"

# No systemd in the pretend home, so start what the services would start.
sim "$CL" mount pre >/dev/null 2>&1
(HOME="$C/home" setsid "$CL" mount run >"$C/mount.log" 2>&1 &)
within 30 mounted && ok "the drive connects" || { bad "the drive connects" "$(tail -3 "$C/mount.log")"; exit 1; }
(HOME="$C/home" setsid "$CL" helper run >"$C/helper.log" 2>&1 &)
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
# The folder was made here, so the second computer must learn of it
# before its helper can see edits inside. Edit at once, on purpose.
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
pct() { "$C/home/.local/bin/juicefs" warmup --check "$1" 2>&1 | grep -o '([0-9.]*%) cached' | tr -d '()% cached'; }
within 60 '[ "$(pct "$B/$T")" = "100.0" ]' && ok "notes saved here are ready there without being opened" || bad "notes saved here are ready there without being opened" "ready: $(pct "$B/$T")%"

if [ -n "${REHEARSE_HOOK:-}" ]; then
  section "Extra checks ($REHEARSE_HOOK)"
  if K="$K" B="$B" LORE="$LORE" CL="$CL" SIM_HOME="$C/home" "$REHEARSE_HOOK" 2>&1 | sed 's/^/      /'; [ "${PIPESTATUS[0]}" = 0 ]; then ok "the extra checks passed"; else bad "the extra checks passed"; fi
fi

section "Leaving"
pkill -f "$CL helper run"; sleep 1
out=$(sim "$CL" leave --yes --delete-cache 2>&1); sleep 2
mounted && { fusermount3 -u "$B" 2>/dev/null; sleep 1; }
pkill -f "$CL mount run" 2>/dev/null
! mounted && [ ! -e "$C/home/.config/lorefs/secret.env" ] && ok "lorefs leave disconnects and removes the saved secret" || bad "lorefs leave disconnects and removes the saved secret" "$(echo "$out" | tail -3)"
[ ! -e "$K/.lorefs/machines/$NAME.json" ] && ok "the computer that left no longer counts in the version line" || bad "the computer that left no longer counts in the version line"
grep -q "small note 3" "$K/$T/n3.md" && ok "the files are still on the drive after the other computer left" || bad "the files are still on the drive after the other computer left"

echo
echo "Not rehearsed here, because they need the colleague's own login session:"
echo "  starting by itself at login, the desktop notifications, and the file"
echo "  manager's right-click entry. All three are in use on this computer."
printf '\n\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
for f in "${FAILED[@]:-}"; do [ -n "$f" ] && echo "  failed: $f"; done
[ "$FAIL" = 0 ]
