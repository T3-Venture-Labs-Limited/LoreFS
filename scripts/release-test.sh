#!/usr/bin/env bash
# Checks the release files and the ways of installing and updating from them:
# the archives, the one-line installer and `lorefs update`, against a local
# folder of release files served over HTTP.
#
# Usage: scripts/release-test.sh [--cleanup-only]
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
T="${LOREFS_REL_DIR:-/tmp/lorefs-reltest}"
PORT=18080
PASS=0; FAIL=0; FAILED=()
ok()  { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); FAILED+=("$1"); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
within() { local s="$1"; shift; local end=$((SECONDS+s)); until "$@" >/dev/null 2>&1; do [ $SECONDS -ge $end ] && return 1; sleep 0.3; done; }

cleanup() {
  [ -f "$T/server.pid" ] && kill "$(cat "$T/server.pid")" 2>/dev/null
  for h in "$T"/home*; do [ -d "$h" ] && fusermount3 -uz "$h/Lore" 2>/dev/null; done
  for p in $(pgrep -f "$T/home.*juicefs mount" 2>/dev/null); do kill "$p" 2>/dev/null; done
  sleep 1
  chmod -R u+rwx "$T" 2>/dev/null; rm -rf "$T"
}
trap 'echo; echo interrupted; cleanup; exit 130' INT TERM
cleanup 2>/dev/null
[ "${1:-}" = --cleanup-only ] && { echo "cleaned up $T"; exit 0; }
mkdir -p "$T"
export PATH="$HOME/.local/share/mise/installs/go/1.27.4/bin:$PATH"; unset GOROOT

section "Building two releases (0.0.1 and 0.0.2)"
for v in 0.0.1 0.0.2; do
  (cd "$ROOT" && scripts/make-release.sh "$v" >"$T/make-$v.log" 2>&1) || { bad "make release $v" "$(tail -3 "$T/make-$v.log")"; cleanup; exit 1; }
  mkdir -p "$T/rel$v"; cp "$ROOT"/dist/release/* "$T/rel$v/"
done
R1="$T/rel0.0.1"; R2="$T/rel0.0.2"
[ -f "$R2/lorefs-0.0.2-linux-amd64.tar.gz" ] && [ -f "$R2/lorefs-0.0.2-linux-arm64.tar.gz" ] && [ -f "$R2/SHA256SUMS" ] && [ -f "$R2/install.sh" ] \
  && ok "a release holds both archives, the one-line installer and SHA256SUMS" || bad "a release holds both archives, the installer and SHA256SUMS" "$(ls "$R2")"
( cd "$R2" && sha256sum -c SHA256SUMS >/dev/null 2>&1 ) && ok "the checksums match the files" || bad "the checksums match the files"
mkdir -p "$T/x"; tar -xzf "$R2/lorefs-0.0.2-linux-amd64.tar.gz" -C "$T/x"; tar -xzf "$R2/lorefs-0.0.2-linux-arm64.tar.gz" -C "$T/x"
for f in lorefs juicefs install.sh LICENSE NOTICE; do [ -e "$T/x/lorefs-0.0.2-linux-amd64/$f" ] || bad "the amd64 archive holds $f"; done
[ -e "$T/x/lorefs-0.0.2-linux-amd64/NOTICE" ] && [ -e "$T/x/lorefs-0.0.2-linux-arm64/LICENSE" ] && ok "each archive holds the licence, the notice, the installer and both programs"
out=$(docker run --rm -v "$T/x/lorefs-0.0.2-linux-amd64:/r:ro" alpine:latest /r/lorefs version 2>&1); echo "$out" | grep -q "v0.0.2" && ok "the amd64 program runs in a minimal container ($(echo "$out" | head -1))" || bad "the amd64 program runs in a minimal container" "$out"
machine=$(od -An -t u2 -j 18 -N 2 "$T/x/lorefs-0.0.2-linux-arm64/lorefs" | tr -d ' ')
[ "$machine" = 183 ] && ok "the arm64 program is an ARM64 executable (ELF machine 183)" || bad "the arm64 program is an ARM64 executable" "ELF machine $machine"
[ "$(od -An -t u2 -j 18 -N 2 "$T/x/lorefs-0.0.2-linux-arm64/juicefs" | tr -d ' ')" = 183 ] && ok "and so is its juicefs" || bad "the arm64 juicefs is an ARM64 executable"

serve() { # serve DIR
  [ -f "$T/server.pid" ] && kill "$(cat "$T/server.pid")" 2>/dev/null; sleep 0.5
  (cd "$1" && python3 -m http.server "$PORT" --bind 127.0.0.1 >"$T/http.log" 2>&1 & echo $! >"$T/server.pid")
  within 10 curl -sf "http://127.0.0.1:$PORT/SHA256SUMS" >/dev/null
}
URL="http://127.0.0.1:$PORT"
newhome() { local h="$T/home-$1"; rm -rf "$h"; mkdir -p "$h"; echo "$h"; }
# E HOME COMMAND...: run a command as a pretend user whose home is HOME.
E() { local h="$1"; shift; env "HOME=$h" "LOREFS_RELEASE_URL=$URL" LOREFS_NO_SYSTEMD=1 "LOREFS_CONFIG_DIR=$h/.config/lorefs" "LOREFS_STATE_DIR=$h/.local/state/lorefs" "$@"; }

section "The one-line installer"
serve "$R1"
h=$(newhome ok)
out=$(E "$h" sh "$R1/install.sh" 2>&1); code=$?
[ $code -eq 0 ] && [ -x "$h/.local/bin/lorefs" ] && [ -x "$h/.local/bin/juicefs" ] && ok "it downloads, verifies and installs lorefs and juicefs under the home folder" || bad "the one-line installer installs" "exit $code: $(echo "$out" | tail -5)"
E "$h" "$h/.local/bin/lorefs" version 2>&1 | grep -q 'v0.0.1' && ok "the installed program is the release's ($("$h/.local/bin/lorefs" version 2>&1 | head -1))" || bad "the installed program is the release's"
# A corrupted archive: nothing is installed.
mkdir -p "$T/rel-bad"; cp "$R1"/* "$T/rel-bad/"; printf 'X' | dd of="$T/rel-bad/lorefs-0.0.1-linux-amd64.tar.gz" bs=1 seek=4000 conv=notrunc 2>/dev/null
serve "$T/rel-bad"; h=$(newhome bad)
out=$(E "$h" sh "$R1/install.sh" 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "does not match" && [ -z "$(ls -A "$h")" ] && ok "a corrupted archive is refused and nothing is installed" || bad "a corrupted archive is refused" "exit $code: $out; $(ls -A "$h")"
# No FUSE 3: a PATH with the tools the installer needs, and no fusermount3.
mkdir -p "$T/bin-nofuse"; for t in sh curl tar sha256sum grep sort tail mktemp rm uname cat dirname printf head cut dd; do p=$(command -v $t) && ln -sf "$p" "$T/bin-nofuse/$t"; done
serve "$R1"; h=$(newhome nofuse)
out=$(E "$h" PATH="$T/bin-nofuse" sh "$R1/install.sh" 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "needs FUSE 3" && echo "$out" | grep -q "install fuse3" && [ -z "$(ls -A "$h")" ] && ok "without FUSE 3 it stops before installing and prints the command to run" || bad "missing FUSE 3 is explained" "exit $code: $out"

section "lorefs update"
L="$h/.local/bin/lorefs"
h=$(newhome upd)
serve "$R1"; E "$h" sh "$R1/install.sh" >/dev/null 2>&1
L="$h/.local/bin/lorefs"
mkdir -p "$h/.config/lorefs"; echo 'drive_name = "kept"' >"$h/.config/lorefs/config.toml"
out=$(E "$h" "$L" update --check 2>&1); code=$?
[ $code -eq 0 ] && echo "$out" | grep -q "newest version" && ok "with the newest release installed, update --check says so" || bad "update --check on the newest release" "exit $code: $out"
out=$(E "$h" "$L" update 2>&1); code=$?
[ $code -eq 0 ] && echo "$out" | grep -q "newest version" && ok "and update changes nothing" || bad "update on the newest release" "exit $code: $out"
# An unreachable server.
before=$(sha256sum "$L" | cut -d' ' -f1)
out=$(E "$h" LOREFS_RELEASE_URL="http://127.0.0.1:1" "$L" update 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "nothing was changed" && [ "$(sha256sum "$L" | cut -d' ' -f1)" = "$before" ] && ok "an unreachable server changes nothing" || bad "an unreachable server changes nothing" "exit $code: $out"
# A newer release with a corrupted archive.
mkdir -p "$T/rel2-bad"; cp "$R2"/* "$T/rel2-bad/"; printf 'X' | dd of="$T/rel2-bad/lorefs-0.0.2-linux-amd64.tar.gz" bs=1 seek=4000 conv=notrunc 2>/dev/null
serve "$T/rel2-bad"
out=$(E "$h" "$L" update 2>&1); code=$?
[ $code -ne 0 ] && echo "$out" | grep -q "does not match" && [ "$(sha256sum "$L" | cut -d' ' -f1)" = "$before" ] && ok "a corrupted archive changes nothing" || bad "a corrupted archive changes nothing" "exit $code: $out"
# A newer release: installed while a program is reading a file on a drive.
serve "$R2"
E "$h" "$L" create --local --name reading --folder "$h/Lore" --no-connect >/dev/null 2>&1
export HOME="$h" LOREFS_CONFIG_DIR="$h/.config/lorefs" LOREFS_STATE_DIR="$h/.local/state/lorefs" LOREFS_JUICEFS="$h/.local/bin/juicefs" LOREFS_NO_SYSTEMD=1
"$L" mount pre; "$L" mount run >"$h/mount.log" 2>&1 &
within 40 test -e "$h/Lore/.config" && ok "a trial drive is mounted from the installed release" || bad "a trial drive is mounted" "$(tail -3 "$h/mount.log")"
echo "a file on the drive" >"$h/Lore/reading.txt"; sleep 1
( end=$((SECONDS+40)); n=0; fails=0; while [ $SECONDS -lt $end ]; do n=$((n+1)); [ "$(cat "$h/Lore/reading.txt" 2>/dev/null)" = "a file on the drive" ] || fails=$((fails+1)); sleep 0.05; done; echo "$n $fails" >"$h/reader.out" ) &
reader=$!
sleep 1
out=$(LOREFS_RELEASE_URL="$URL" "$L" update --check 2>&1); code=$?
[ $code -eq 1 ] && echo "$out" | grep -q "0.0.2" && ok "update --check reports the newer release (exit 1)" || bad "update --check reports the newer release" "exit $code: $out"
out=$(LOREFS_RELEASE_URL="$URL" "$L" update 2>&1); code=$?
[ $code -eq 0 ] && "$L" version 2>&1 | grep -q 'v0.0.2' && ok "update installs the newer release in one step ($("$L" version 2>&1 | head -1))" || bad "update installs the newer release" "exit $code: $out"
grep -q 'drive_name = "kept"' "$h/.config/lorefs/config.toml" && ok "the settings are kept" || bad "the settings are kept"
wait "$reader"; read -r n fails <"$h/reader.out"
[ "$fails" = 0 ] && [ "$n" -gt 50 ] && ok "a program reading a file on the drive kept working throughout ($n reads, none failed)" || bad "a reader kept working throughout" "$fails of $n reads failed"
[ -e "$h/Lore/.config" ] && ok "the drive stayed connected (it is not restarted by an update)" || bad "the drive stayed connected"
fusermount3 -uz "$h/Lore" 2>/dev/null

echo
printf '\033[1m%d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" -gt 0 ] && { printf '  failed: %s\n' "${FAILED[@]}"; }
cleanup
[ "$FAIL" = 0 ]
