# LoreFS

LoreFS is a shared folder for a small team and its AI agents. Everyone sees
the same files within seconds, every save keeps the previous version, and
agents get a private copy of a folder to work in before anything changes
for everybody else. It runs on Linux. It is built on
[JuiceFS](https://juicefs.com), and it is unrelated to other projects that
happen to be called Lore.

It is for people who want one place for notes, research, plans and handoffs
that works for humans and agents alike, on storage and a server they
control, with undo for every file.

## Try it in two minutes

You need Linux with FUSE 3 (`fuse3`) installed. Nothing else, and no account
anywhere.

```
curl -fsSL https://github.com/T3-Venture-Labs-Limited/LoreFS/releases/latest/download/install.sh | sh
lorefs create --local
```

A folder called `Lore` appears in your home folder. Save a file in it, save
it again, and look at what happened:

```
lorefs history ~/Lore/notes.md
lorefs status
```

A trial drive lives on this computer only. When you are ready to share:

## Share a drive with a colleague

A shared drive needs two things you provide:

- **Storage** for the files: any S3-compatible bucket (Backblaze B2,
  DigitalOcean Spaces, Cloudflare R2, AWS S3, MinIO, ...), and an access key
  that can write to it.
- **An index**, the small database that remembers file names and folders: a
  Redis-compatible server that accepts encrypted connections. If you do not
  have one, `lorefs server init` writes a folder that runs one with Docker.

```
lorefs create --bucket https://my-bucket.s3.us-west-004.backblazeb2.com \
              --access-key <key> --index rediss://index.example.net:6379/1
```

Secrets are asked for at a hidden prompt, never taken from the command line.
It prints a join code. Send it to your colleague through a private channel
(it works like a password), and they run:

```
curl -fsSL https://github.com/T3-Venture-Labs-Limited/LoreFS/releases/latest/download/install.sh | sh
lorefs join
```

See [docs/creating-a-drive.md](docs/creating-a-drive.md) for what to ask your
storage provider for and worked examples.

## Everyday commands

| | |
|---|---|
| `lorefs status` | Is the drive working, and what to do if not |
| `lorefs history <file>` | Earlier versions of a file; restore one |
| `lorefs diff <file>`, `lorefs restore <file> <n>` | What changed; make a version current |
| `lorefs log` | Recent saves, deletions and restores on every computer |
| `lorefs trash` | Files deleted or replaced in the last 7 days |
| `lorefs sandbox create <folder>` | A private copy for an agent to work in; `sandbox diff` and `sandbox apply` bring changes back |
| `lorefs pin <folder>` | Keep a folder ready on this computer |
| `lorefs invite` | A join code for another computer |
| `lorefs backup setup` / `run` / `schedule` | A second copy at another provider |
| `lorefs update` | The newest release |

Agents read `~/Lore/AGENTS.md`, which the drive carries. It explains the
same commands to them and asks them to keep folders shallow.

## Known limits

- Linux only for now. macOS and Windows are not supported yet.
- Two people saving the same file at once do not merge: the later save wins,
  and the earlier one stays in the file's history.
- Video files are not versioned (they are large and usually replaced rather
  than edited); a deleted or replaced video is kept for 7 days in the trash.
- File contents are not encrypted by LoreFS; they are as private as your
  storage bucket. Connections to the index are encrypted.
- Every computer acts as the user who created the drive, so file ownership
  and permissions inside the drive are not separate per person.
- The version history of a drive is only as complete as its change log;
  see [docs/version-history.md](docs/version-history.md).

## More

- [Creating a drive](docs/creating-a-drive.md)
- [Running your own index](docs/running-an-index.md)
- [Backup and restore](docs/backup-and-restore.md)
- [How version history works](docs/version-history.md)
- [Security](SECURITY.md) and [contributing](CONTRIBUTING.md)
- [Setting up LoreFS for an agent](docs/agents.md)
- [Performance notes](docs/performance.md)

Licensed under the Apache License 2.0 (see [LICENSE](LICENSE) and
[NOTICE](NOTICE)).
