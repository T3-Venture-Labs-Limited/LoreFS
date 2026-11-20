#!/usr/bin/env bash
# Checks that JuiceFS's metadata changelog tells, for each change made
# through one computer, the right file and kind of change, and nothing
# about changes made through the other. Two mounts of one drive stand for
# two computers; each is followed by scripts/changelogprobe.
#
# Usage: scripts/changelog-experiment.sh [--keep]     (about 1 minute)
#   DELAY=255ms   add this delay each way to the second computer's index
#                 connection (510 ms per request), as in slowlink-test.sh
#   FILES=0       also write this many small files at once, eight at a
#                 time, and check that none is missed
#   BULK_SIDE=a   which computer writes them (b is the slow one with DELAY)
#   BULK_THREADS=8
set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
JFS="$ROOT/.tools/juicefs"
T="${LOREFS_CLEXP_DIR:-/tmp/lorefs-clexp}"
NAME=lorefs-clexp-redis; PORT=16396; PROXY=16397; PW=clexp-password-not-a-secret
DELAY="${DELAY:-0}"; FILES="${FILES:-0}"
KEEP=0; [ "${1:-}" = "--keep" ] && KEEP=1
"$ROOT/scripts/get-juicefs.sh" "$ROOT/.tools" >/dev/null || exit 1
(cd "$ROOT" && go build -o bin/changelogprobe ./scripts/changelogprobe && go build -o bin/slowlink ./scripts/slowlink) || exit 1

pass=0; failn=0
ok()  { pass=$((pass+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { failn=$((failn+1)); printf '  \033[31m✗\033[0m %s\n' "$1"; [ -n "${2:-}" ] && printf '      %s\n' "$2"; }
mounted() { grep -q " $T/$1 fuse.juicefs " /proc/self/mounts; }
cleanup() {
  pkill -f "$ROOT/bin/changelogprobe" 2>/dev/null; pkill -f "$ROOT/bin/slowlink -listen 127.0.0.1:$PROXY" 2>/dev/null
  for m in a b; do "$JFS" umount "$T/$m" >/dev/null 2>&1 || fusermount3 -uz "$T/$m" 2>/dev/null; done
  docker rm -f "$NAME" >/dev/null 2>&1; chmod -R u+rwx "$T" 2>/dev/null; rm -rf "$T"
}
trap 'cleanup; exit 130' INT TERM
cleanup 2>/dev/null; mkdir -p "$T"/{store,a,b,ca,cb}

docker run -d --name "$NAME" -p "127.0.0.1:$PORT:6379" redis:8-alpine redis-server --requirepass "$PW" --maxmemory-policy noeviction --appendonly no >/dev/null || exit 1
for _ in $(seq 1 40); do docker exec "$NAME" redis-cli -a "$PW" --no-auth-warning ping 2>/dev/null | grep -q PONG && break; sleep 0.25; done
export META_PASSWORD="$PW" LOREFS_NO_SYSTEMD=1
UA="redis://127.0.0.1:$PORT/1"; UB="$UA"
if [ "$DELAY" != 0 ]; then
  (setsid "$ROOT/bin/slowlink" -listen "127.0.0.1:$PROXY" -to "127.0.0.1:$PORT" -delay "$DELAY" >"$T/proxy.log" 2>&1 &); sleep 0.5
  UB="redis://127.0.0.1:$PROXY/1"
fi
"$JFS" format --storage file --bucket "$T/store/" --trash-days 7 "$UA" clexp >"$T/format.log" 2>&1 || { cat "$T/format.log"; cleanup; exit 1; }
"$JFS" config "$UA" --changelog --changelog-max-age 24h --changelog-max-lines 200000 --yes >/dev/null 2>&1
for m in a b; do
  u=$UA; [ $m = b ] && u=$UB
  (setsid "$JFS" mount "$u" "$T/$m" --cache-dir "$T/c$m" --no-usage-report --hide-internal --attr-cache 2 --entry-cache 2 --dir-entry-cache 10 --open-cache 2 >"$T/mount-$m.log" 2>&1 &)
done
for _ in $(seq 1 120); do mounted a && mounted b && break; sleep 0.25; done
mounted a && mounted b && ok "two computers mount one drive" || { bad "two computers mount one drive"; cleanup; exit 1; }
A="$T/a"; B="$T/b"

for m in a b; do
  u=$UA; [ $m = b ] && u=$UB
  (setsid "$ROOT/bin/changelogprobe" -juicefs "$JFS" -meta "$u" -mount "$T/$m" >"$T/probe-$m.out" 2>"$T/probe-$m.err" &)
done
sleep 3

marks=(); step() { sleep "${SETTLE:-1.5}"; marks+=("$(wc -l < "$T/probe-a.out") $(wc -l < "$T/probe-b.out")"); }
norm() { sed -E 's/^ *[0-9.]+ //' | grep -v -E '\.tmp|^#' | sort -u; }
# events <a|b> <step number from 1>: normalised events the probe printed during that step
events() {
  local who="$1" n="$2" prev="0 0" cur
  [ "$n" -gt 1 ] && prev="${marks[$((n-2))]}"
  cur="${marks[$((n-1))]}"
  local pa pb ca cb; read -r pa pb <<<"$prev"; read -r ca cb <<<"$cur"
  if [ "$who" = a ]; then [ "$ca" -gt "$pa" ] && sed -n "$((pa+1)),${ca}p" "$T/probe-a.out" | norm
  else [ "$cb" -gt "$pb" ] && sed -n "$((pb+1)),${cb}p" "$T/probe-b.out" | norm; fi
}
expect() { # expect <who> <step> <description> <expected events, one per line>
  local who="$1" n="$2" desc="$3" want="$4" got
  got="$(events "$who" "$n" || true)"
  if [ "$got" = "$(printf '%s\n' "$want" | sort -u | sed '/^$/d')" ]; then ok "$desc"; else bad "$desc" "wanted: $(echo "$want" | tr '\n' ';')  got: $(echo "$got" | tr '\n' ';')"; fi
}

head -c 40000 /dev/urandom > "$T/src.bin"
mkdir "$A/d";                                                     step   # 1
echo one > "$A/d/new.md";                                         step   # 2
echo two >> "$A/d/new.md";                                        step   # 3
echo three > "$A/d/.new.md.tmp"; mv "$A/d/.new.md.tmp" "$A/d/new.md"; step   # 4
cp "$T/src.bin" "$A/d/copied.bin"; touch -d '2026-01-05 10:00' "$A/d/copied.bin"; step   # 5
truncate -s 1 "$A/d/new.md";                                      step   # 6
mv "$A/d" "$A/d2";                                                step   # 7
rm "$A/d2/copied.bin";                                            step   # 8
(cd "$A/d2" && "$JFS" clone new.md new.copy.md >/dev/null 2>&1);  step   # 9
ln "$A/d2/new.md" "$A/d2/hard.md";                                step   # 10
echo x > "$A/d2/odd, name (1)%.md"; echo y > "$A/d2/café.md"; echo z > "$A/d2/a|b.md"; step   # 11
echo from-b > "$B/d2/b.md";                                       step   # 12
echo second-edit >> "$B/d2/b.md"; echo from-a-again >> "$A/d2/new.md"; step   # 13
mv "$A/d2/new.md" "$A/d2/renamed.md";                             step   # 14
mkdir "$A/d2/sub" && echo deep > "$A/d2/sub/deep.md" && mv "$A/d2/sub" "$A/d2/sub2"; echo more >> "$A/d2/sub2/deep.md"; step   # 15

section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
section "Changes made through computer A (what its probe reports)"
expect a 1  "a new folder is not a save"                    ""
expect a 2  "a new file: one path, a save"                  "save d/new.md"
expect a 3  "an append: a save"                              "save d/new.md"
expect a 4  "save by rename: the original is saved"          "save d/new.md"
expect a 5  "a copy whose date is set afterwards: a save"    "save d/copied.bin"
expect a 6  "a truncate: a save"                             "save d/new.md"
expect a 7  "a folder rename is a rename, not a save"        "rename d d2"
expect a 8  "a delete"                                       "delete d2/copied.bin"
expect a 9  "a clone: a save of the copy"                    "save d2/new.copy.md"
expect a 10 "a hard link: both names of the inode"           "save d2/hard.md
save d2/new.md"
expect a 11 "names with commas, brackets, accents, bars"     "save d2/odd, name (1)%.md
save d2/café.md
save d2/a|b.md"
expect a 12 "nothing for what computer B did"                ""
expect a 13 "a save in a renamed folder: new path, own save only"   "save d2/hard.md
save d2/new.md"
expect a 14 "a file rename inside a folder"                  "rename d2/new.md d2/renamed.md
save d2/renamed.md"
expect a 15 "a folder made, filled and renamed, then edited" "save d2/sub2/deep.md
save d2/sub/deep.md
rename d2/sub d2/sub2"
section "Changes made through computer B"
expect b 12 "a file in a folder A made and renamed: right path"  "save d2/b.md"
expect b 13 "B's own edit, not A's"                              "save d2/b.md"
expect b 2  "nothing for what computer A did"                    ""

# How many raw lines does one save cost? (documented, not asserted)
raw2=$(sed -n "$(( $(cut -d' ' -f1 <<<"${marks[0]}") + 1 )),$(cut -d' ' -f1 <<<"${marks[1]}")p" "$T/probe-a.out" | grep -c save)
echo "  note: creating a file with content printed $raw2 save line(s)"

if [ "$FILES" -gt 0 ]; then
  SIDE="${BULK_SIDE:-a}"; section "Bulk: $FILES small files written through computer $SIDE, ${BULK_THREADS:-8} at a time"
  D="$T/$SIDE"; P="$T/probe-$SIDE.out"; export D
  # One folder per worker: creating files in one folder is done one at a
  # time by the kernel, which over a slow connection takes seconds each.
  for k in $(seq 0 19); do mkdir -p "$D/bulk/p$k"; done; sleep 2
  t0=$SECONDS
  seq 1 "$FILES" | xargs -P "${BULK_THREADS:-8}" -I{} sh -c 'echo {} > "$D/bulk/p$(( {} % 20 ))/file-{}.md"'
  wrote=$((SECONDS - t0)); lastwrite=$(date +%s)
  end=$((SECONDS+240)); distinct=0
  while [ $SECONDS -lt $end ]; do
    distinct=$(grep ' save bulk/' "$P" | sed -E 's/^ *[0-9.]+ save //' | sort -u | wc -l)
    [ "$distinct" -ge "$FILES" ] && break; sleep 1
  done
  behind=$(( $(date +%s) - lastwrite ))
  echo "  writing took $wrote s; the reader had every path $behind s after the last write returned"
  [ "$distinct" -ge "$FILES" ] && ok "every one of $FILES files was reported (${distinct} distinct paths)" || bad "every one of $FILES files was reported" "only $distinct distinct paths"
  # Each file must be reported under its own path, with no wrong ones.
  wrong=$(grep ' save bulk/' "$P" | sed -E 's/^ *[0-9.]+ save //' | grep -v -E '^bulk/p[0-9]+/file-[0-9]+\.md$' | head -3)
  [ -z "$wrong" ] && ok "every reported path is a file that was written" || bad "every reported path is a file that was written" "$wrong"
fi

grep -q ' gap ' "$T/probe-a.out" "$T/probe-b.out" && bad "no gap in the versions read" || ok "no gap in the versions read"
printf '\n\033[1m%d passed, %d failed\033[0m\n' "$pass" "$failn"
if [ "$KEEP" = 1 ]; then echo "kept: $T"; else cleanup; fi
[ "$failn" = 0 ]
