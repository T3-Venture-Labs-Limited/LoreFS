# Acting as the drive's owner on every computer

Every mount is started with `--all-squash UID:GID`, where the two numbers
are those of the user who created the drive (they travel in the join code
and the settings file). Whatever the local user numbers are, the drive's
files then belong to that user and every computer may change them, so two
people whose accounts are numbered differently work on the same files.

## Known behaviour

- **Read-only files and the open cache.** With the option the kernel no
  longer checks permissions (JuiceFS switches `default_permissions` off);
  JuiceFS checks them itself, and it skips the check for a file the
  computer has opened recently (`open_cache_sec`, 2 seconds by default,
  renewed by every open). A program that writes to a file soon after the
  computer, or its helper, opened it can therefore write to a file whose
  mode is 444. Seen in the integration rig: a version copy (mode 444)
  could be appended to on the computer that had recorded it, and was
  refused on a computer that had not touched it. Version copies are
  therefore protected against accidents, not against a determined
  program. Only the copies are exposed; the original is never at risk
  from this.
- The option changes what `ls -l` shows: every file belongs to the drive's
  owner. Local group and user names that do not match are shown by number.
