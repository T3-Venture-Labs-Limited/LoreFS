# Performance notes

Measured with JuiceFS 1.4.2, a Redis-compatible index on a small virtual
server about 40 ms away and an S3-compatible bucket about 40 ms away, from
one computer. Times will differ with your distance to the index.

| What | Result |
|---|---|
| One request to the index | 40 ms |
| List a folder of 100 files (`lorefs selftest`) | 0.3 s |
| Open a 5 MB file, first time / again | 0.6 s / 0.001 s |
| List 1,000 files not listed recently | 0.4 s |
| Save one small file | about 0.5 s |
| Save 1,000 small files, many at a time | about 4 a second, however many at once |
| Save a 5 MB file | 1.1 s |
| Upload / download throughput of the bucket | 11 MiB/s / 61 MiB/s |

Why small files are slow to save: each folder in a path costs one request to
the index when the kernel has forgotten it, creating a file costs about
three, and the save itself about 0.3 s (one upload and one index update).
The mount therefore remembers file names for 2 s and folder names for 10 s
(`file_cache_sec`, `folder_cache_sec`, `open_cache_sec` in the settings). One
consequence: a file made on one computer may not be openable on another for
up to 10 seconds if its folder was just made again.

## Distance to the index

Everything that touches names costs one round trip per question, so the
distance to the index matters more than the speed of the connection.
`lorefs selftest` prints the time of one request and says when the distance
is the cause of slow results. A VPN that routes traffic the long way round
can turn 40 ms into 500 ms; let connections to the index bypass it.

At 500 ms per request, listing a folder takes seconds, starting the helper
reads the change log in about a minute for a full log (one round trip per
1,000 entries), and saving a file takes several seconds. Everything still
works.

## Index memory

About 80 bytes per change-log entry and about 1 KB per file with its
versions. 2,100 files with their versions used 5 MiB. A 400 MB limit is
generous for tens of thousands of files.

## Failure behaviour

| Test | Result |
|---|---|
| Index stopped, then an uncached file opened | Input/output error after 8 s |
| Index down for minutes | the status page and a notification say so; both services stay up |
| Index back | answering within 2 s, no remount |
| Index restarted or its server rebooted | all files and versions present |
| Storage unreachable | uncached reads fail within 3 minutes, saves within 6 minutes, listings and cached files keep working, everything recovers within a minute |
| Drive stopped | the folder becomes read-only and empty; creating a file is refused |

JuiceFS waits a fixed 30 seconds for an answer on a connection before trying
another, so a brief network drop can make one uncached read take 30 s.
