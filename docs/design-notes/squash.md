# Acting as the drive's owner on every computer

Every mount is started with `--all-squash UID:GID`, where the two numbers
are those of the user who created the drive (they travel in the join code
and the settings file). Whatever the local user numbers are, the drive's
files then belong to that user and every computer may change them, so two
people whose accounts are numbered differently work on the same files.

## Known behaviour

- **Read-only files and the open cache.** With the option, JuiceFS checks
  a file's mode itself rather than the kernel. Its open cache (2 seconds
  by default, `open_cache_sec` in the settings) can then let a program
  write to a file in the 2 seconds after the file was made read-only, if
  that program opened it just before. Version copies are made read-only
  (mode 444); a program that opens one for writing more than 2 seconds
  after it was made is refused. Measured in the integration rig.
- The option changes what `ls -l` shows: every file belongs to the drive's
  owner. Local group and user names that do not match are shown by number.
