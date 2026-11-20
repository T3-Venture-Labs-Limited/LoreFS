#!/usr/bin/env bash
# Measures the JuiceFS metadata changelog on a throwaway drive, for the
# questions in docs/design-notes/changelog.md:
#
#   lag      how long after a save completes can its entry be read
#   stream   how entries arrive while one large file is being written
#   memory   index memory per entry
#   restart  what the reader does when the index restarts under it
#   gap      what the reader sees when the entries it asks for are gone
#   resume   how long reading from an old position takes, near the index
#            and far from it
#
# Usage: scripts/changelog-measure.sh [lag|stream|memory|restart|gap|resume|all]
#   ENTRIES=50000   size of the log for "resume"
#   DELAY=255ms     one-way delay for the far computer in "resume"
set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
JFS="$ROOT/.tools/juicefs"
T="${LOREFS_CLMEASURE_DIR:-/tmp/lorefs-clmeasure}"
NAME=lorefs-clmeasure-redis; PORT=16398; PROXY=16399; PW=clmeasure-password-not-a-secret
WHAT="${1:-all}"; ENTRIES="${ENTRIES:-50000}"; DELAY="${DELAY:-255ms}"
"$ROOT/scripts/get-juicefs.sh" "$ROOT/.tools" >/dev/null || exit 1
(cd "$ROOT" && go build -o bin/changelogprobe ./scripts/changelogprobe && go build -o bin/slowlink ./scripts/slowlink) || exit 1
rcli() { docker exec "$NAME" redis-cli -a "$PW" --no-auth-warning -n 1 "$@" 2>/dev/null; }
cleanup() {
  pkill -f "$ROOT/bin/changelogprobe" 2>/dev/null; pkill -f "$JFS changelog" 2>/dev/null; pkill -f "$ROOT/bin/slowlink -listen 127.0.0.1:$PROXY" 2>/dev/null
  "$JFS" umount "$T/a" >/dev/null 2>&1 || fusermount3 -uz "$T/a" 2>/dev/null
  docker rm -f "$NAME" >/dev/null 2>&1; chmod -R u+rwx "$T" 2>/dev/null; rm -rf "$T"
}
trap 'cleanup; exit 130' INT TERM
cleanup 2>/dev/null; mkdir -p "$T"/{store,a,ca}
# The index keeps its writes on disk (as the real one does) and writes them out on a clean stop.
docker run -d --name "$NAME" -p "127.0.0.1:$PORT:6379" redis:8-alpine redis-server --requirepass "$PW" --maxmemory-policy noeviction --appendonly yes --appendfsync everysec >/dev/null || exit 1
for _ in $(seq 1 40); do rcli ping | grep -q PONG && break; sleep 0.25; done
export META_PASSWORD="$PW"
U="redis://127.0.0.1:$PORT/1"
"$JFS" format --storage file --bucket "$T/store/" --trash-days 7 "$U" clmeasure >"$T/format.log" 2>&1 || { cat "$T/format.log"; cleanup; exit 1; }
"$JFS" config "$U" --changelog --changelog-max-age 24h --changelog-max-lines 1000000 --yes >/dev/null 2>&1
(setsid "$JFS" mount "$U" "$T/a" --cache-dir "$T/ca" --no-usage-report --hide-internal --attr-cache 2 --entry-cache 2 --dir-entry-cache 10 --open-cache 2 >"$T/mount.log" 2>&1 &)
for _ in $(seq 1 120); do grep -q " $T/a fuse.juicefs " /proc/self/mounts && break; sleep 0.25; done
A="$T/a"
probe() { (setsid "$ROOT/bin/changelogprobe" -juicefs "$JFS" -meta "$U" -mount "$A" "$@" >"$T/probe.out" 2>"$T/probe.err" &); sleep 2.5; }
stats() { sort -n | awk '{a[NR]=$1} END{p=int(NR*0.95+0.5); if (p>NR) p=NR; if (p<1) p=1; printf "n=%d min=%.0f median=%.0f p95=%.0f max=%.0f", NR, a[1], a[int((NR+1)/2)], a[p], a[NR]}'; }
sec() { printf '\n\033[1m%s\033[0m\n' "$1"; }

if [ "$WHAT" = all ] || [ "$WHAT" = lag ]; then
  sec "lag: from the save returning to the entry being read (milliseconds)"
  probe -abs; mkdir -p "$A/lag"; : > "$T/saved.txt"
  for i in $(seq 1 40); do echo "$i" > "$A/lag/f$i"; printf 'lag/f%s %s\n' "$i" "$(date +%s.%N)" >> "$T/saved.txt"; sleep 0.4; done
  sleep 2
  join -j1 <(sort "$T/saved.txt") <(awk '/ save lag\/f[0-9]+$/ && !seen[$3]++ {print $3, $1}' "$T/probe.out" | sort) \
    | awk '{printf "%.0f\n", ($3-$2)*1000}' | stats; echo
  pkill -f "$ROOT/bin/changelogprobe"; sleep 0.5
fi

if [ "$WHAT" = all ] || [ "$WHAT" = stream ]; then
  sec "stream: entries while a 400 MB file is written, and one written slowly"
  probe -abs; mkdir -p "$A/st"
  t0=$(date +%s.%N); dd if=/dev/urandom of="$A/st/big.bin" bs=1M count=400 status=none; t1=$(date +%s.%N)
  sleep 2
  awk '/ save st\/big.bin$/ {t[++n]=$1} END{ if(n<1){print "  no entries"; exit} for(i=2;i<=n;i++){g=t[i]-t[i-1]; if(g>m)m=g}; printf "  400 MB written in %.1f s: %d entries for the file, first %.1f s after the start, last %.1f s after it ended, longest gap between entries %.2f s\n", '"$t1 - $t0"', n, t[1]-'"$t0"', t[n]-'"$t1"', m}' "$T/probe.out"
  # A program that writes a block, waits a second, writes another.
  (for i in 1 2 3 4 5; do head -c 1000000 /dev/urandom >> "$A/st/slow.bin"; sleep 1; done) 
  sleep 2
  awk '/ save st\/slow.bin$/ {t[++n]=$1} END{ for(i=2;i<=n;i++){g=t[i]-t[i-1]; if(g>m)m=g}; printf "  five appends a second apart (the file is closed each time): %d entries, longest gap %.2f s\n", n, m}' "$T/probe.out"
  pkill -f "$ROOT/bin/changelogprobe"; sleep 0.5
fi

if [ "$WHAT" = all ] || [ "$WHAT" = memory ]; then
  sec "memory: index memory per changelog entry, after 10,000 operations"
  export A; mkdir -p "$A/mem"
  before=$(rcli llen txnLog)
  seq 1 3334 | xargs -P 8 -I{} sh -c 'echo {} > "$A/mem/m{}"; mv "$A/mem/m{}" "$A/mem/n{}"; rm "$A/mem/n{}"'   # create+write, rename, delete = about 10,000 operations
  sleep 2
  n=$(rcli llen txnLog); used=$(rcli memory usage txnLog)
  echo "  entries $before -> $n; the log uses $used bytes: $(( used / n )) bytes per entry"
  echo "  index memory in all: $(rcli info memory | grep '^used_memory_human')"
fi

if [ "$WHAT" = all ] || [ "$WHAT" = restart ]; then
  sec "restart: the index restarts while a reader follows the log"
  probe -abs; mkdir -p "$A/rs"; echo before > "$A/rs/before"; sleep 2
  t0=$(date +%s.%N); docker restart "$NAME" >/dev/null
  for _ in $(seq 1 80); do rcli ping | grep -q PONG && break; sleep 0.25; done
  echo "  index answered again $(awk -v s="$t0" -v e="$(date +%s.%N)" 'BEGIN{printf "%.1f", e-s}') s after the restart began"
  t1=$(date +%s.%N); echo after > "$A/rs/after"
  for _ in $(seq 1 240); do grep -q ' save rs/after$' "$T/probe.out" && break; sleep 0.5; done
  grep ' save rs/after$' "$T/probe.out" >/dev/null && echo "  the reader reported the save made after the restart $(awk -v s="$t1" -v e="$(date +%s.%N)" 'BEGIN{printf "%.1f", e-s}') s after it was made" || echo "  the reader did NOT report the save made after the restart"
  grep -q ' save rs/before$' "$T/probe.out" && echo "  and the one before it" ; grep -c ' gap ' "$T/probe.out" | sed 's/^/  gaps reported: /'
  pgrep -f "$ROOT/bin/changelogprobe" >/dev/null && echo "  the reader was still running" || echo "  the reader had stopped"
  pkill -f "$ROOT/bin/changelogprobe"; sleep 0.5
fi

if [ "$WHAT" = all ] || [ "$WHAT" = gap ]; then
  sec "gap: asking for a position that no longer exists"
  mkdir -p "$A/gp"; for i in $(seq 1 200); do echo "$i" > "$A/gp/g$i"; done; sleep 1
  last=$(rcli get txnLastLog); len=$(rcli llen txnLog)
  echo "  log holds versions $((last - len + 1)) to $last"
  rcli ltrim txnLog 150 -1 >/dev/null
  last=$(rcli get txnLastLog); len=$(rcli llen txnLog); first=$((last - len + 1))
  echo "  after the log was cut: versions $first to $last"
  pkill -f "$ROOT/bin/changelogprobe"; (setsid "$ROOT/bin/changelogprobe" -juicefs "$JFS" -meta "$U" -mount "$A" -from 10 >"$T/probe.out" 2>"$T/probe.err" &); sleep 4
  grep ' gap ' "$T/probe.out" | head -2 | sed 's/^/  asking from 10: /'
  (setsid "$ROOT/bin/changelogprobe" -juicefs "$JFS" -meta "$U" -mount "$A" -from "$((first + 20))" >"$T/probe2.out" 2>"$T/probe2.err" &); sleep 4
  echo "  asking from $((first + 20)) (inside the log): $(grep -c ' gap ' "$T/probe2.out") gap(s) reported"
  pkill -f "$ROOT/bin/changelogprobe"; sleep 0.5
fi

if [ "$WHAT" = all ] || [ "$WHAT" = resume ]; then
  sec "resume: reading from an old position, with $ENTRIES entries in the log"
  export A; mkdir -p "$A/rs2"
  while [ "$(rcli llen txnLog)" -lt "$ENTRIES" ]; do
    seq 1 2000 | xargs -P 8 -I{} sh -c 'echo {} > "$A/rs2/m{}"; mv "$A/rs2/m{}" "$A/rs2/n{}"; rm "$A/rs2/n{}"'
  done
  last=$(rcli get txnLastLog); len=$(rcli llen txnLog)
  echo "  log holds $len entries (versions $((last - len + 1)) to $last), using $(rcli memory usage txnLog) bytes"
  reading() { # reading <label> <meta url>
    local url="$2" t0 n; : > "$T/all.out"
    t0=$(date +%s.%N)
    (setsid "$JFS" changelog "$url" --from "$(( last - len + 1 > 1 ? last - len + 1 : 1 ))" >"$T/all.out" 2>/dev/null &)
    for _ in $(seq 1 1200); do n=$(wc -l < "$T/all.out"); [ "$n" -ge "$((len - 1))" ] && break; sleep 0.2; done
    echo "  $1: read $n of $len entries in $(awk -v s="$t0" -v e="$(date +%s.%N)" 'BEGIN{printf "%.1f", e-s}') s"
    pkill -f "$JFS changelog"; sleep 0.3
  }
  reading "next to the index" "$U"
  (setsid "$ROOT/bin/slowlink" -listen "127.0.0.1:$PROXY" -to "127.0.0.1:$PORT" -delay "$DELAY" >"$T/proxy.log" 2>&1 &); sleep 0.5
  reading "through $DELAY each way (twice that per request)" "redis://127.0.0.1:$PROXY/1"
fi
cleanup
