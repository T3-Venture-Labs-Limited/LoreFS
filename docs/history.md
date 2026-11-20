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

Each computer records the saves made on that computer. A version is a
copy made inside the drive's index: it takes no extra storage until the
file's content is later replaced, and then only the replaced part.

- Saves to one file less than 2 seconds apart are recorded as one
  version holding the last content.
- A file that is written without pause still gets a version every
  5 minutes.
- If the recorder was not running while files changed, it records their
  current content when it starts again.
- A folder made or renamed on one computer is noticed by the others
  within about 10 seconds, so edits anyone makes inside it are recorded.
  A file saved in those first seconds is recorded a little later, within
  a minute or so.
- Versions live in the hidden folder `.lorefs/history` at the top of the
  drive. Do not edit it by hand.

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
