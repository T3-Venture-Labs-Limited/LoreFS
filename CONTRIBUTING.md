# Contributing to LoreFS

Thank you for looking. A short guide to getting a change in.

## What to expect

LoreFS is small on purpose: it adds sharing, version history, safe work
areas for agents and backup on top of JuiceFS. Changes that keep it small
and keep the existing checks passing are easy to accept.

## Build and test

You need Go (the version in `go.mod`), Docker, `jq`, `curl` and FUSE 3.

```
make build        # bin/lorefs
make test         # go vet and the unit tests
scripts/itest.sh --fast        # the integration rig (Redis in Docker)
scripts/create-test.sh         # creating drives (MinIO and Redis in Docker)
scripts/recorder-test.sh       # version recording from the change log
scripts/slowlink-test.sh       # a computer far from the index
scripts/backup-test.sh         # backup and restore
scripts/server-test.sh         # the index bundle (Valkey in Docker)
```

Each script cleans up after itself; `--cleanup-only` removes what a
killed run left behind.

## Before you send a change

- `make check` runs the unit tests and the privacy check.
- `scripts/install-hooks.sh` installs the pre-commit hook that runs the
  privacy check, which looks for secrets and for words you list in a file
  outside the repository (`~/.config/lorefs-dev/private-words`).
- Describe what changed and why, and say which of the scripts above you ran.
- Keep user-facing text plain. People who are not programmers read it.

## Reporting a problem

Use the issue tracker for bugs and ideas. For a security problem, follow
`SECURITY.md` instead and do not open a public issue.
