---
name: lorefs
description: Work safely in a LoreFS shared drive (usually ~/Lore): read and write shared files, see and undo changes with version history, recover deleted files, and make bulk edits in a private sandbox. Use when the task involves the shared team folder, "undo", "restore a file", or editing many files shared with other people or agents.
---

# Working in a LoreFS drive

The drive is an ordinary folder (default `~/Lore`) shared by people and agents on several computers. Changes appear on other computers within seconds. Read `AGENTS.md` at the top of the drive first: it holds this team's conventions.

## Before you start

```bash
lorefs status --json        # exit 0 healthy, 1 warnings, 2 not working
```

If the drive is not working, stop and tell the user what `lorefs status` says; do not write elsewhere.

## Rules

- Keep new work at most two folders below the project folder. No folder for one or two files. No folders per date, agent or session.
- Search for an existing file on the topic before writing a new one, and update it.
- Write complete files. Two writers to one file do not merge; the later save wins.
- To claim a job, create a claim file with exclusive create (it fails if it exists).

## See and undo changes

```bash
lorefs history <file> --json          # versions, newest first
lorefs diff <file>                    # latest change; add a version number to compare an older one
lorefs restore <file> <number>        # make a version current; what it replaces is kept
lorefs log --json                     # recent saves, deletions and restores on all computers
```

A deleted file: run `lorefs history` on its old path, then `lorefs restore`. Videos are not versioned; use `lorefs trash` and `lorefs trash restore <name>` within 7 days.

## Bulk or risky edits: use a sandbox

```bash
lorefs sandbox create <folder> [name] --json    # private copy, no extra storage; returns its path
# edit files inside the sandbox path only
lorefs sandbox diff <name>                       # review exactly what changed
lorefs sandbox apply <name> --yes                # copy changes back; replaced files keep their history
lorefs sandbox remove <name>                     # or discard
```

Apply only after the diff looks right, and tell the user what changed.

## Never

- Do not edit, move or delete anything inside `.lorefs/`.
- Do not print or store a join code (`lorefs invite`); it is a password for the whole drive.
- Do not run `lorefs leave`, `lorefs create` or `lorefs backup` unless the user asks.
