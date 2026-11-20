# Version history

Every time a file on the drive is saved, LoreFS keeps the version that
was there before. You can list a file's versions, see what changed, and
go back to any of them.

## Using it

| To do this | Run |
|---|---|
| See a file's versions and restore one | `lorefs history <file>` |
| Just list them | `lorefs history --list <file>` |
| See what the latest change did | `lorefs diff <file>` |
| Compare an older version with now | `lorefs diff <file> <number>` |
| Make a version current again | `lorefs restore <file> <number>` |
| Open a version without changing anything | `lorefs open <file> <number>` |
| See recent activity on all computers | `lorefs log` |
| Find a file deleted in the last 7 days | `lorefs trash [part of its name]` |
| Bring it back | `lorefs trash restore <name>` |

Version 1 is the newest. In the file manager, right-click a file and
choose **Version history**.

A restore never destroys anything: the content it replaces is kept as a
version, so you can restore back.

Deleted files keep their versions. Run `lorefs history` on the path the
file used to have, then `lorefs restore`.

## How long versions are kept

- A version is removed 30 days after a newer version replaced it.
- The newest version of a file that still exists is never removed.
- A deleted file's versions are removed 30 days after the deletion.

So the change before the latest one can always be undone for 30 days,
however old the earlier content is. To change the period, set
`retention_days` in `~/.config/lorefs/config.toml`.

## How it works

Each computer records the saves made through its own mount. A version is
a copy made inside the drive's index: it takes no extra storage until the
file's content is later replaced, and then only the replaced part.

The drive keeps a **change log**, a list of everything that was done to
it (files created, written, renamed, deleted), written by JuiceFS. Each
computer follows that list, keeps only the entries made through its own
mount, works out which file each one is about, and records a version once
the file has been quiet for half a second:

- Saves to one file less than half a second apart become one version
  holding the last content.
- A file that is written without pause still gets a version every
  5 minutes.
- A file that is still being written is not recorded until its writer is
  done; a version is only made when the file's size on the drive matches
  what has been stored.
- A folder made or renamed on any computer needs no introduction: the
  others learn about it from the same list.
- The change log keeps 24 hours of changes (up to 100,000 entries). A
  computer remembers how far it has read, so after being switched off it
  carries on from there. If it was off for longer than the log reaches
  back, it notices, and records the files that changed since it last
  looked by walking the drive (this takes longer, but misses nothing).
- If the drive was made without a change log, or it was turned off
  (`lorefs admin changelog off`), versions come from that walk alone,
  every ten minutes. `lorefs status` says so and how to turn the log on:
  `lorefs admin changelog on`. After the log is turned on, a computer's
  mount takes a few seconds to a minute to start writing to it, so a
  computer walks the drive once more a minute after it notices. After the log is turned on, a computer's
  mount takes a few seconds to a minute to start writing to it, so a
  computer walks the drive once more a minute after it notices.
- Versions live in the hidden folder `.lorefs/history` at the top of the
  drive. Do not edit it by hand.

## Limits

- A version can be written to by a program that opened the version file
  a moment before it was made read-only (the mount remembers recently
  opened files for 2 seconds). Versions are protected against accidents,
  not against a determined program.
- Another computer's saves are recorded by that computer. If one computer
  is never switched on again, its last few saves before it stopped may
  not have versions.
- The change log is JuiceFS's. LoreFS reads it with `juicefs changelog`
  and does not modify it.

## What is not recorded

Temporary files, lock files and partial downloads would only add noise,
so files and folders matching these patterns never get versions.

Folders:

```
.lorefs
.trash
.Trash-*
.git
node_modules
__pycache__
.venv
```

Files:

```
*.tmp
*.temp
*.swp
*.swx
*.swo
*~
.#*
#*#
.~lock.*
*.lck
*.part
*.partial
*.crdownload
.goutputstream-*
4913
.DS_Store
.lorefs-*
.control
.config
.stats
.accesslog
```

Video files:

```
*.mp4
*.mov
*.mkv
*.webm
*.avi
*.m4v
```

Video files are large and are replaced rather than edited, so keeping
every replaced copy for 30 days would use storage for little benefit.
Upper and lower case make no difference (`CLIP.MP4` is skipped too). A
video that was deleted or replaced can still be brought back for 7 days
with `lorefs trash`.

### Changing the list

Create `~/.config/lorefs/skip` on each computer, one pattern per line:

```
# also skip backup files
*.bak
# skip everything in folders called "build" (the slash marks a folder)
build/
# record lock files after all (an exclamation mark removes a built-in rule)
!*.lck
# keep versions of mp4 files after all
!*.mp4
```

Then restart the recorder: `systemctl --user restart lorefs-helper`.
The drive's own bookkeeping (`.lorefs`, `.trash`) can never be recorded.
