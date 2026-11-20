# JuiceFS's metadata changelog as the source of version history

Written 20 November 2026 (task group 2 of the first release). JuiceFS
1.4.2, Redis 8 as the index, a file folder as storage, two mounts of one
drive standing for two computers. Everything here was measured with the
scripts named in each section; they stay in the repository so that the
numbers can be repeated.

## Decision

**Go.** Version recording is built on the changelog (design D3), with
the changes below to the plan's retention and grouping.

| Criterion (task 2.4) | Result |
|---|---|
| No entry missed in the scripted sequence, the 2,000-file copy and the slow connection | Met: 20 of 20 on four runs in a row; 2,000 of 2,000 files next to the index; 1,000 of 1,000 through half a second per request (300 s to write them, the reader never behind) |
| Entries readable within 2 seconds of a save near the index | Met: median 50 ms, 95th percentile 95 ms, maximum 96 ms over 40 saves |
| A detectable signal when the requested position is gone | Met: the version numbers jump; the reader reports `gap 11 150` for a request from 10 when the log starts at 151 |
| Index memory under 300 bytes per entry | Met: 82 bytes per entry (1.1 MB for 13,351 entries after 10,000 operations) |

## What the changelog is

Turned on with `juicefs config META --changelog` (default retention 2
hours; `--changelog-max-age`, `--changelog-max-lines`). In a
Redis-compatible index it is a list, `txnLog`, with a counter
`txnLastLog`; the version of an entry is `txnLastLog - length + 1 +
position`. `juicefs changelog META --from N` prints the entries *after*
N (0 means "from now"), polls about every 100 ms, and keeps running.
The mounts trim the list in the background every five minutes by age
and by length. Entries are not a backup and carry no file data.

One line per entry:

    VERSION: SECONDS.NANOS|OPERATION(arguments)[:result]|(SESSION,TRANSACTION)

Names in arguments use `%XX` escapes for control characters, commas,
percent signs, parentheses, quotes, backslashes and bytes outside ASCII.
A bar (`|`) is not escaped. `NEWSESSION` has no session part.

The operations that matter, as printed by 1.4.2 (arguments by position):

| Operation | Arguments | Result | What it means for a file |
|---|---|---|---|
| `CREATE` | parent, name, uid, gid, type (1 file, 2 folder, 3 link), mode, umask, path, flavour, flag | new inode | teaches the path |
| `WRITE` | inode, chunk index, offset, slice id, slice length, mtime, mtime nanoseconds | number of slices | content committed: a save |
| `TRUNCATE` | inode, old length, new length, flags | | a save |
| `FALLOCATE` | inode, offset, size, mode | | a save |
| `COPYFILERANGE` | source inode, source offset, destination inode, destination offset, size | new length | the destination is saved |
| `SETATTR` | inode, flags, … times, mode, owner | | date, mode or owner changed (a copy tool setting the original date) |
| `MOVE` | source parent, source name, destination parent, destination name, flags, overwritten inode, trash folder | moved inode | rename; the destination is saved; an overwritten inode is gone |
| `UNLINK` | parent, name, trash folder, … | inode | delete |
| `RMDIR` | parent, name, trash folder | inode | folder removed |
| `CLONE` | source inode, destination parent, name, … | new inode | a new file with the source's content |
| `LINK` | inode, parent, name, … | inode | a second name for the same inode |
| `ATTACH` | inode, parent, name | | a detached inode gets a name |
| `DIRSTAT`, `INCR_COUNTER`, `NEWSESSION`, `CLEANSESSION`, `DELETESLICE`, … | | | bookkeeping, ignored |

The entry carries the client session. A mount's own session number is
in its `.config` file (`Meta.Sid`), so a computer can act only on its
own saves, and every version carries the name of the computer that made
it. A path is built from inodes: parents and names from `CREATE`,
`MOVE`, `LINK` and `ATTACH` as they pass, and `juicefs info -i INODE`
(run inside the mount) for an inode that was not seen. A file with
hard links has several names; all of them are saved.

## Experiments

### Sequence (`scripts/changelog-experiment.sh`)

Fifteen steps on two mounts, each followed by what the probe reported:
new file, append, save by rename, copy with the date set afterwards,
truncate, folder rename, delete, `juicefs clone`, hard link, names with
commas, brackets, accents and a bar, a save by the other computer, an
edit in a renamed folder, a file rename, a folder made, filled and
renamed. Each step's reported set equals the expected set, nothing is
reported for the other computer's changes, and no gap in the versions.
20 of 20, four runs in a row. A new file with content gave one `save`.

### Lag (`changelog-measure.sh lag`)

From a save returning to its entry being read: 40 saves, 0.4 s apart:
median 50 ms, 95th 95 ms, maximum 96 ms. A write is committed to the
index before the program's `close` returns, so the entry is there at
once; the delay is the reader's poll interval.

### Streams (`changelog-measure.sh stream`)

A 400 MB file written in 3.2 s gave **seven entries**, one per 64 MiB
slice, the longest gap between them 0.61 s. Five appends a second apart
gave five entries 1.1 s apart. There is **no entry for closing a file**.
Consequence for grouping: a quiet time of half a second can fall inside
a long write (a full slice committed, the next one still being filled
and uploaded), and over a real connection the gaps are as long as it
takes to upload 64 MiB. See "Consequences".

### Memory (`changelog-measure.sh memory`)

10,000 operations (create and write, rename, delete) produced 13,351
entries using 1,103,327 bytes: 82 bytes per entry. A log of 100,000
entries is about 8 MB of a 400 MB index.

### Restart (`changelog-measure.sh restart`)

The index was restarted while a reader followed the log. The reader
kept running, lost nothing (no gap) and reported a save made after the
restart **21 seconds** after it was made. It recovers by itself; the
delay is its reconnection back-off.

### Gap (`changelog-measure.sh gap`)

After the list was cut to start at version 151, asking from 10 printed
the entries from 151 on, with nothing in between: the program does not
say that entries were skipped. A reader detects it itself, because the
first version it receives is not the one after its position
(`gap 11 150` in the probe). Asking from a position inside the log
reports no gap.

### Resuming (`changelog-measure.sh resume`)

With 56,038 entries in the log, reading from the start to the end took
**0.2 s** next to the index and **32.5 s** through half a second per
request. For a Redis-compatible index the program always reads the list
from its beginning in blocks of 1,000, whatever position was asked for,
so the cost of starting is (entries in the log ÷ 1,000) round trips:
about 57 at that size, 110 at 100,000 entries, even if the reader
resumes from the last entry. Steady state is one round trip per poll.

## Consequences for the design

1. **Retention: 24 hours and 100,000 entries** (the plan said 200,000).
   At half a second per request a full log takes about a minute to read
   on start; at 40 ms it takes 5 seconds. A quiet drive holds far fewer.
   If starting through a very slow connection proves too slow in group
   7, the reader can read the list at a given position itself (one
   round trip), as the existing index client already reads other JuiceFS
   keys; that is an optimisation, not a change of behaviour.
2. **Settling a file.** The reader acts after a file has been quiet for
   half a second, as planned. The plan to wait 30 seconds after a full
   slice was **not** adopted: the integration rig showed that a 64 MiB file
   (exactly one slice) then took 10 s to get its version instead of under
   5, and the wait is not needed, because a file that is still being written
   is not recorded anyway (its size on the drive is ahead of what has been
   stored, so the copy is dropped, and the file's own close produces the entry
   that records it). The option stays (`LOREFS_LONG_QUIET`, off by default) for
   storage so slow that a writer pauses longer than that between slices.
3. **Unfinished state.** The entries describe committed state, so the
   "recorded while still being written" fault of the folder watcher
   cannot occur through the log. A recorder in the writer's own mount
   still sees the writer's unflushed length when it looks at the file,
   so the check that the clone has the file's size stays.
4. **A gap is detected from the version numbers**, and answered by the
   periodic walk of changed files (design D3), as is a position older
   than the log.
5. **Temporary names** pass through the log like any other entry and
   are dropped by the skip list where the path is known.
6. **Hard links** save every name of the inode.

## Not measured

- SQL and TiKV indexes (the changelog reader there uses an ID window
  with its own rewind rules; trial drives use SQLite, whose reader has
  none).
- Behaviour with several computers each running a reader at once beyond
  two (each reads the whole log; cost grows with computers times
  operations).
- A real storage provider: the experiments used a folder; the changelog
  is metadata only, so storage does not enter into it.
