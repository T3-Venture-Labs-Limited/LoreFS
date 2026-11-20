# Verification of the first release

Written 21 November 2026 for the release candidate. It records which check
covers each requirement of the first release, the results of the last full
run, and what could only be verified by reading.

## Checks and last results

Every check is a script in `scripts/` or a Go test. `make test` runs the
unit tests (also under the race detector); the others need Docker and FUSE
and clean up after themselves.

| Short name | Command | What it does | Last result |
|---|---|---|---|
| unit | `go test -race ./...` | 20 packages: parsers, resolver, grouping, settings, codes, status, backup, restore, update, bundle, certificates | all pass; `staticcheck` and `govulncheck` clean |
| itest | `scripts/itest.sh --fast` | two computers on one drive (Redis); `INDEX_IMAGE=valkey/valkey:8-alpine` runs it on Valkey | RESULT_ITEST_REDIS / RESULT_ITEST_VALKEY |
| create | `scripts/create-test.sh` | creating drives (MinIO and Redis), joining, trial drives, prompts, ps | RESULT_CREATE |
| server | `scripts/server-test.sh` | the index bundle with Docker: TLS, password, certificate, restart, full index | RESULT_SERVER |
| squash | `scripts/squash-test.sh` | a second user with another user number in a container | RESULT_SQUASH |
| recorder | `scripts/recorder-test.sh` | version recording from the change log: resume, gaps, no log, bulk copy | RESULT_RECORDER |
| slow | `scripts/slowlink-test.sh` | a computer 510 ms from the index | RESULT_SLOW |
| backup | `scripts/backup-test.sh` | backup, schedule, restore, drill, restore from the backup alone | RESULT_BACKUP |
| outage | `scripts/storage-outage-test.sh` | storage refusing or silent, recovery | RESULT_OUTAGE |
| release | `scripts/release-test.sh` | release files, one-line installer, `lorefs update` | RESULT_RELEASE |
| distro | `scripts/distro-test.sh` | Ubuntu, Debian, Fedora and Arch containers | RESULT_DISTRO |
| walk | `scripts/distro-test.sh walkthrough` | the README's first section in a fresh container | RESULT_WALK |
| rehearse | `scripts/rehearse-join.sh` | a colleague's install and join from a release archive | RESULT_REHEARSE |
| compat | `scripts/compat-test.sh` | a computer joined with the earlier program | RESULT_COMPAT |
| privacy | `scripts/check-private-test.sh`, `scripts/check-private.sh` | secrets, addresses, private words | passes, 17 of 17 |
| ext | `python3 scripts/nautilus-extension-test.py` | the file-manager entry's logic against a stand-in API | passes |
| lint | `actionlint`, `shellcheck -S warning` | workflow files and every script | clean |

## Scenario to check

`itest:` names a check line in that script. "reading" means the scenario was
verified by reading the code and the text, not by running it.

### drive-creation
- Create on an S3-compatible bucket; Connect this computer straight away: create (a drive is created on MinIO and Redis; create --connect), server (create --server-dir)
- Wrong storage key; Index unreachable; Index already in use: create (Nothing is created unless every check passes)
- Typed at the prompt: create (Secrets typed at the prompt are not shown; No secret on a command line)
- Plain connection without the option: create (an unencrypted index is refused and the option is named)
- Defaults after creation: create (the drive keeps deleted files 7 days and has its change log on; the rules file is at the top)
- Try it; Invite on a trial drive: create (A trial drive needs no account anywhere, including create --local with no network)

### index-server
- Generate for a server address; Run twice; Create from the folder: unit (bundle, certificates), server
- Unencrypted connection; Wrong password; Certificate for another address; Restart; Memory limit reached: server

### machine-setup
- FUSE 3 present / missing; Corrupted download; Newer release exists; Already newest; No connection; Update while a program has a file open: release, unit (update)
- Successful join; Invalid code; Already joined; Invite another computer; File permissions; Leave: itest (a bad join code is refused; joining twice changes nothing; connection details are readable only by the user; leave removes the connection details), create
- Server without a desktop: distro (core checks run in containers without a desktop)
- GNOME Files present: ext (logic, run against a stand-in); the live file manager (sidebar entry, right-click, responsiveness) was not exercised, see below
- No user session manager: distro (join without a user session stops before changing anything)
- Install over the private program: compat

### drive-mount
- Connected after login; Network arrives late: reading (the services restart by themselves; the private deployment has run this way for days)
- Write while disconnected: itest (Folder refuses writes while the drive is not mounted)
- Power loss right after a save: itest (a file whose save returned is complete on beta after alpha's mount is killed)
- Different user numbers: squash
- Content update; Listing update: itest (new file content, changed content, a rename, a deletion are readable on beta within 5 s)
- Edit in place; Lock held on another computer; Save by replace: itest (editing bytes in the middle; 900 appends; exclusive creation, 15 of 15 rounds; save by replace never shows a mixture)
- Near the index; Far from the index: itest and slow (selftest on both; selftest names the distance)
- Cache reaches its limit: itest (the cache stays within a small limit)
- Index unreachable: itest (opening an uncached file fails with an error instead of hanging)
- Storage unreachable; Backend returns: outage
- Stop while a file manager shows the folder: itest (stop while in use returns within the limit)

### version-history
- New file; Changed file; Save by replace; Burst of saves; Editor swap file: itest (a new file gets a version; a changed file gets a second; save by replace records a version; five rapid saves are grouped; editor swap files are not recorded)
- Large file copied into a new folder; Program keeps a file open; Continuous writing: itest (a file still being written gets no version until the program closes it), recorder (bulk copy: exactly one version per file), unit (grouping: continuous writing still reports)
- Edit straight after the folder appeared; Folder renamed on the other computer: itest (an edit on beta inside a folder made on alpha; after alpha renames the folder; a folder deleted and made again)
- Two computers edit one file; Two seconds apart; Activity from two computers: itest (with two writers the later save is current; both writers' content is in history), rehearse
- Date set after copying; Large file: itest (a file whose date was set after it was recorded; a version of a 64 MB file is recorded within 5 s, no duplicate data)
- Slow connection, many folders; Stop while removing old versions on a slow connection: slow
- Recorder was stopped; Computer was away longer than the change log is kept; New computer: recorder (resume from the position; the log no longer holds what was missed), itest (a change made while the recorder was stopped is caught up), slow (it made versions of its own files only)
- Listing; File without history; Text file; Image; Roll back a change; Restore after deletion: itest (the list shows time, computer and size; history explains that such files are not recorded; diff for text and for non-text; restore makes the chosen version current; a deleted file can be brought back)
- Save during a large deletion: unit (deletions do not hold up saves)
- Renamed file; Name already taken: itest (history follows a renamed file and a moved folder; restoring again never overwrites)
- Version replaced long ago; Only version is old; Folder unreadable during removal: itest (removing old versions; only one computer removes at a time), unit (history store prune)
- Saving a video; Deleted video: itest (video files get no versions; a deleted video is brought back from the trash)
- Searching the drive: itest (a plain recursive search finishes; ripgrep does not look into history)
- Restore from the file manager; Cancel: ext (the entry opens `lorefs history` in a terminal); the restore itself is the history view's, itest (restore)

### fast-folders
- Another computer adds notes; Search after caching; Pin a folder; Unpin; Folder larger than the cache; Independent pins: itest (Fast folders section)

### agent-access
- Agent writes a note; New drive; Edited rules survive an update: itest (the rules file is created on a new drive; an edited rules file is never overwritten)
- A newer template exists: create (status mentions it, `rules diff` shows it, the file is unchanged), unit (status)
- Print the pointer: itest (agents-snippet names this computer's drive folder)
- Work in a sandbox; Keep the result; Discard the result; Large folder: itest (the Agent access section, including no duplicate data for a large folder)
- Forged record: unit (sandbox)
- Agents on two computers save the same file; Two agents claim one job: itest (with two writers the later save is current; exclusive creation)

### drive-status
- Healthy drive; Not joined; Index unreachable; Recorder stopped: itest (Status section)
- Trial drive: create (status says it is a trial drive)
- Index slow; Recorder starting: slow (status names a slow connection and a VPN; status says it is recording or starting), unit (status)
- Storage unreachable: outage (status shows the storage as reachable again), unit (status)
- Change log off: recorder (status warns about the missing change log and says how to turn it on)
- Backup failed: backup (status on both computers shows the reason, an old copy warns)
- This computer is behind; Another computer is behind: itest (status warns when another computer runs a newer version)
- Agent checks the drive: itest (status --json is valid and complete)
- Outage and recovery: itest (one notification says the drive is unavailable; ... and available again; only one per change of state)
- Output is safe to share: itest (status never shows the password), create (the output never contains a secret)

### drive-backup
- First run; Later run; Destination is the drive's own bucket; Destination does not keep removed objects; Drive's bucket was emptied: backup
- Run failed because the provider refused more data: unit (explain), backup (a refused key is reported with its reason; status shows it)
- Computer was off at the scheduled time: backup (the timer is persistent and ran by itself), reading for the catch-up itself (systemd's `Persistent=true`)
- Restore into an empty index; Index already holds a drive; Drive's bucket is lost; Drill: backup

### distribution
- Release archive; Checksums match; Runs on a minimal system: release
- Private name in a file; Something shaped like a key: privacy
- A test fails: lint (the workflow files run every step on each push and pull request)
- README walkthrough: walk
- Looking for macOS: reading (the README says Linux only for now)
- Agent follows the document: reading (docs/agents.md); not exercised with a live agent
- Reading the security note: reading (SECURITY.md)

## Verified by reading only, or not verified

- **The live file manager** (sidebar entry, right-click entry opening the
  view, responsiveness while a mounted folder is slow). The logic is tested
  against a stand-in for GNOME Files' API and the commands it runs are the
  tested ones, but no desktop session was driven. The first person to use it
  on GNOME should look.
- **Starting at login, a network that arrives late, desktop notifications.**
  Systemd user services, as in the private deployment; the notification
  command is the one the private deployment has used.
- **A real storage provider.** Every check used MinIO. The program speaks S3
  through JuiceFS and signs its own checks (versioning, listing) with the
  standard signature; neither has been run against a hosted provider.
- **Suspend and resume** of a computer with the drive mounted: the owner's
  own test.
- **An agent following `docs/agents.md`**: written, not tried with a live agent.
- **macOS and Windows**: not supported.
