# AGENTS.md

Guidance for AI coding agents working in this repository, and for agents using LoreFS as a tool.

## Using LoreFS

Use the command line with `--json` where offered. Start with `lorefs status --json`. The workflow for working in a drive is in [skills/lorefs/SKILL.md](skills/lorefs/SKILL.md). Never run `lorefs create`, `leave`, `backup` or `restore-index` unless the user asks.

## Codebase map

Go, standard library only (no third-party modules). The program runs `juicefs` as a separate process.

- `cmd/lorefs/`: the commands (one file per area: `create.go`, `backup.go`, `server.go`, `history.go`, ...).
- `internal/record/`: version recording. `resolver.go` turns change-log entries into file paths, `group.go` waits until a file is quiet, `recorder.go` follows the log, keeps its position and handles gaps.
- `internal/changelog/`: parser for JuiceFS's change-log lines.
- `internal/history/`: versions, journals, skip list, pruning. `internal/sandbox/`, `internal/trash/`: sandboxes and the trash.
- `internal/helper/`: the background service (recording, cache warming, pruning, health, notifications).
- `internal/create/`: creating drives. `internal/server/`: the index bundle and certificates. `internal/backup/`, `internal/restore/`, `internal/s3check/`: backup, restore and signed S3 checks.
- `internal/config/`, `internal/token/`, `internal/setup/`, `internal/mount/`, `internal/index/`, `internal/jfs/`, `internal/status/`, `internal/update/`: settings, join codes, joining and leaving, the mount command line, the small index client, JuiceFS wrappers, the status page, releases.
- `scripts/`: the test rigs (`itest.sh`, `create-test.sh`, `recorder-test.sh`, `backup-test.sh`, ...). Each cleans up after itself; `--cleanup-only` removes what a killed run left.
- `docs/design-notes/`: measurements and decisions (change log, owner squash, verification).

## Conventions

- `make test` (vet and unit tests) before finishing; `make check` adds the privacy check. Run the rig that covers what you changed (`scripts/itest.sh --fast` for most things).
- Text shown to people is plain language; people who are not programmers read it.
- Secrets are never taken from command-line arguments, never printed, never logged.
- Never commit secrets, real addresses, private names or files from a real drive. `scripts/install-hooks.sh` installs the hook that checks.
- Do not use `pkill -f` patterns that could match a real mount (`juicefs mount`); stop test mounts by the exact path.
- Changes reach `main` through a pull request; CI must pass.
