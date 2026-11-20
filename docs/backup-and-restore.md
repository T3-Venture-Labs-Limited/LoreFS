# Backup and restore

A drive has two parts, and each needs a copy:

- **The files** live in the storage bucket. `lorefs backup` copies them to a
  second bucket at another provider.
- **The index** (names and folders) is copied by JuiceFS into the bucket
  every hour while a computer is connected. The backup copies those too.

## Setting it up

```
lorefs backup setup --bucket https://my-backup.s3.us-west-004.backblazeb2.com --access-key <key>
```

The secret key is asked for (or `LOREFS_BACKUP_SECRET`). Setup checks that
the bucket can be reached and written with the key, refuses the drive's
own bucket, says whether the bucket keeps what is removed from it, and
stores the keys where only you can read them.

**Choose a destination that keeps removed objects for a while** (bucket
versioning, or a rule keeping them for about 30 days; Backblaze B2 does this
by default). Otherwise a wipe of the drive's bucket would reach the copy at
the next run.

## Running it

```
lorefs backup run          # copy now
lorefs backup schedule     # every night at about 03:30; catches up after the computer was off
lorefs backup schedule --off
lorefs backup status
```

Run it on the computer that is always on, if there is one. The first run
copies everything; later runs transfer only what changed. A run refuses to
copy when the drive's bucket holds fewer than half as many objects as the
copy (once the copy holds over a thousand): that looks like a loss, so it
stops and says to check the drive's bucket. The result of the last run (when,
how many objects, how much data, or why it failed) shows in `lorefs status`
on every computer.

Note: the copy is made by `juicefs sync`, which takes the drive's storage key
on its command line for the length of the run. The drive's key is already
held by every computer of the drive. The backup's own key is passed in the
environment.

## Restoring

`lorefs restore-index` rebuilds the index of a drive from one of the copies.
File contents are never touched by a restore: only the map from names to
stored blocks is rebuilt. Changes made after the copy was written (at most
about an hour) are not in it.

1. Make a new, **empty** index (`lorefs server init`, or any empty
   Redis-compatible database).
2. See the copies: `lorefs restore-index --list --bucket <the drive's bucket> --access-key <key>`
   (secret key in `LOREFS_STORAGE_SECRET`; add `--name <drive name>` on a computer
   that has not joined).
3. Restore: `lorefs restore-index --index rediss://new-index:6379/1 --index-ca-file ca.crt --bucket ... --access-key ...`
   (index password in `LOREFS_INDEX_PASSWORD`; or `--server-dir <folder>`).
   Use `--copy <name>` for an older copy. It refuses an index that already
   holds a drive, and runs a check that every file's blocks are present.
4. Give each computer a new join code (`lorefs invite` on one that is
   connected, `lorefs leave --keep-cache` and `lorefs join` on the others).

If the drive's bucket itself is gone, restore from the backup instead: add
`--from-backup` (the bucket and keys are read from the backup settings). The
drive then reads its files from the backup bucket.

One command puts the storage key on its own command line for a moment during
a restore (`juicefs config`).

## Rehearse it

```
lorefs restore-index --drill
```

restores into a throwaway index on this computer, mounts it read-only,
compares restored files with the live drive and reports how many were
identical. It changes neither the real index nor either bucket and leaves
nothing behind. Do this now and then, so that the first time you restore is
not an emergency.
