# LoreFS shared drive: rules for agents

This folder is LoreFS, a drive shared by the people who use it and by
their AI agents. The same files appear on every person's machine. A file
you save here is visible to the others within a few seconds.

## What it is for

- Notes, documents, plans, research and decisions that others should see.
- Images, video and other assets the team shares.
- Handoffs between agents: what was done, what was checked, what is next.

## Where things go

This drive is the shared knowledge base of the people who use it and
their agents. Keep findings, research, decisions, drafts others should
see and handoffs in the folder of the project they belong to (for
example `website/` or `research/`), so that the other people and their
agents can find them and build on them.

- Before making a folder, look for an existing one that fits and use it.
- Put new work at most two folders below the project folder, for
  example `website/research/pricing-competitors.md` or
  `website/research/competitors/acme.md`. Never deeper.
- Do not make a folder for one or two files. A descriptive file name
  does that job: `2026-11-20 creator pricing benchmarks.md`.
- No folders per date, per agent or per session.
- Before writing something new, search for an existing file on the same
  topic (`rg -l "topic" website/`) and update it instead.

## How to write here

- Write complete files. A save replaces the whole file; two writers to
  one file do not merge, and the later save wins.
- Prefer one topic per file, and new files over long shared ones. For a
  running log, write one file per day or per agent instead of appending
  to a single file that others also append to.
- Use plain, descriptive file names, with a date at the front when the
  content is about a moment (`2026-11-20 call with Acme.md`).
- Moving or renaming is fine; a file's history follows it.
- To claim a piece of work so that only one agent takes it, create a
  claim file with "fail if it exists" (exclusive create). Exactly one
  creator succeeds, on any machine.

## Earlier versions and undo

Every save keeps the previous version for 30 days.

- `lorefs history <file>` lists the versions of a file (add `--json` for
  structured output).
- `lorefs diff <file>` shows what changed since the previous version.
- `lorefs restore <file> <number>` makes a version current again. The
  content it replaces is kept as a version, so a restore can be undone.
- `lorefs log` lists recent saves, deletions and restores on all machines.
- A deleted file can be brought back for 30 days: run `lorefs history` on
  its old path, then `lorefs restore`.
- Video files (`.mp4`, `.mov`, `.mkv`, `.webm`, `.avi`, `.m4v`) are not
  versioned. A deleted or replaced video is kept for 7 days:
  `lorefs trash` lists such files and `lorefs trash restore <name>` brings
  one back. Do not overwrite a video in place if the old one may still
  be wanted; save the new one under a new name.

## Finding and searching files

Looking into a folder takes about a fifth of a second here, however
small the folder is. Tools that go through folders one at a time are
therefore slow on a large tree, and tools that look into many folders
at once are fast. Measured on 2,100 files in 345 folders:

- Search file contents with `rg` (ripgrep): about 5 seconds. `grep -r`
  takes about a minute.
- Find or list files by name with `fd` or `rg --files`: 3 to 6 seconds.
  `find`, `ls -R`, `tree` and `du` take about a minute.
- Search the folder you need, not the whole drive.
- Leave `.lorefs` out of searches. `rg` and `fd` skip it by default;
  with `--hidden`, add `--glob '!.lorefs'` (`fd`: `--exclude .lorefs`).
- Listing one folder, and opening a file whose path you know, are fast.

## Risky or large changes: use a sandbox

Before reorganising, bulk-editing or deleting many files, work on a
private copy:

- `lorefs sandbox create <folder> <name>` prints the path of the copy. It
  takes no extra storage.
- Work inside that path. The original folder is not affected.
- `lorefs sandbox diff <name>` lists what you added or changed.
- `lorefs sandbox apply <name>` copies added and changed files back.
  `lorefs sandbox remove <name>` discards the copy.

## What must not be stored here

- Code repositories, `node_modules`, virtual environments and build
  output. Many small files are slow on this drive, and git belongs on
  the local disk. Link to the repository instead.
- Passwords, API keys, tokens and other secrets.
- Databases that are being written to (SQLite files in use, and so on).

## Things that behave differently from a local folder

- Programs that wait for change notifications do not notice changes
  made on another machine. Re-read the file or folder instead.
- If the internet connection or the drive is down, reads and writes
  fail with an error. Run `lorefs status` to see what is wrong; do not
  retry in a tight loop.
- The folder `.lorefs` holds the drive's history and bookkeeping. Do
  not edit, move or delete anything inside it.

## Checking the drive

`lorefs status` shows whether the drive is healthy; `lorefs status --json`
gives the same as data, and exits with 0 (healthy), 1 (warnings) or
2 (not working).

<!-- lorefs-rules-version: 1 -->
