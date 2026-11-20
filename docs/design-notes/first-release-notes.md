# First release: what to look at on the first real run

Written 21 November 2026, with the release candidate.

## What exists

Everything the plan listed for the first release is in the repository and
passes its checks: see [verification.md](verification.md) for each
requirement, its check and the last results.

## What was not verified, and why

- **GNOME Files, live.** The right-click entry's logic is tested against a
  stand-in for the file manager's API; no desktop session was driven. The
  first person who installs on GNOME should check: the sidebar entry
  appears, right-click on a file shows "Version history", it opens the
  history view, restoring works, and the file manager stays responsive on
  other folders while the drive is slow.
- **A hosted storage provider.** All checks used MinIO. Creating a drive,
  `lorefs backup` and `lorefs restore-index` talk to providers through
  JuiceFS and through the program's own signed requests (listing, versioning).
- **Sleep and resume** of a computer with the drive mounted.
- **An agent following `docs/agents.md`.**

## What the first run on GitHub must show

1. The `test` workflow passes: unit tests, static analysis, vulnerability
   scan, privacy check, shellcheck, and the integration scripts on a hosted
   Ubuntu runner (FUSE 3 is installed by a step; if mounting is refused
   there, the integration job needs `--device /dev/fuse` or a different
   runner).
2. A tag `vX.Y.Z` runs the `release` workflow and the release holds both
   archives, `install.sh` and `SHA256SUMS`.
3. The one-line installer works against the real release, `lorefs update
   --check` reads it, and an update from a deliberately older build works.
4. The README's address for the installer
   (`releases/latest/download/install.sh`) resolves.

## Known limits worth knowing before publishing

- Linux only. macOS and Windows are not supported.
- With `--all-squash` the mount's open cache can let a program write to a
  version copy (mode 444) soon after the computer opened it: copies are
  protected against accidents, not against a determined program
  ([squash.md](squash.md)).
- A mount takes a few seconds to a minute to write to the change log after
  it is turned on; the recorder walks once more a minute later.
- The backup takes the drive's storage key on the command line of
  `juicefs sync` for the length of a run, and `restore-index` on the command
  line of `juicefs config` for a moment (JuiceFS offers no other way).
