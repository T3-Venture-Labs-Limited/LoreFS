# Creating a drive

A shared drive is made of two parts that you provide. LoreFS joins them
(through JuiceFS) and keeps nothing of its own on a server.

## 1. Storage for the files

Any S3-compatible bucket works. You need:

- the **bucket address**, in one of two forms:
  - `https://<bucket>.<endpoint>`, for example
    `https://my-drive.s3.us-west-004.backblazeb2.com` or
    `https://my-drive.nyc3.digitaloceanspaces.com`
  - `https://<endpoint>/<bucket>`, for example `http://127.0.0.1:9000/my-drive`
    (typical for a server you run yourself)
- an **access key** and a **secret key** that may read, write and delete in
  that bucket.

The bucket can be empty or already hold other things; LoreFS keeps
everything under a folder named after the drive. Pick a drive name of 3 to
63 letters, digits and hyphens (`--name`, default `lorefs`). The bucket is
created by `lorefs create` if it does not exist and the key may do so.

Cost: you pay your provider for the bytes stored. Versions of a file cost
nothing until its content is replaced, and then only the replaced part.

Setup notes for common providers: create a bucket, create an application key
limited to that bucket with read and write access, and note its id and secret.
(Backblaze B2: the S3-compatible endpoint is shown on the bucket's page;
DigitalOcean Spaces: Spaces, create a key; Cloudflare R2: S3 API token with
object read and write; AWS: an IAM user with access to the bucket.)

## 2. An index

The index remembers file names, folders and which stored blocks make up
each file. It is small (a few megabytes for thousands of files) and it must
answer quickly: every folder listing and file open asks it a few questions.
Put it near the people who use the drive.

It can be any **Redis-compatible server** with an address that accepts
encrypted connections (`rediss://`): a managed Redis or Valkey service, or
one you run. The simplest way to run one is the bundle `lorefs server init`
writes: see [running-an-index.md](running-an-index.md).

Requirements: a password; data kept on disk (append-only file) so that a
restart loses nothing; no eviction of keys (a full index must refuse new
files, never forget old ones); room for a few hundred megabytes.

An index that is not encrypted can be used for testing only, with
`--allow-unencrypted-index`; anyone on the network path could read file
names and the password.

## 3. Create the drive

```
lorefs create --bucket https://my-drive.s3.us-west-004.backblazeb2.com \
              --access-key <key> --index rediss://index.example.net:6379/1
```

- The storage secret key and the index password are asked for at hidden
  prompts. For scripts, set `LOREFS_STORAGE_SECRET` and
  `LOREFS_INDEX_PASSWORD`, or put them in a file only you can read and pass
  `--secrets-file`. They are never accepted as command-line arguments and
  never printed.
- If the index certificate comes from the server's own authority, give its
  file with `--index-ca-file`.
- With a bundle from `lorefs server init`: `--server-dir <folder>` instead of
  `--index` (and no password prompt).

Before anything is written, `lorefs create` checks that the index can be
reached, accepts the password and holds nothing, and that the bucket can be
written with the key. If any check fails it says which, and leaves both as
they were. It refuses an index that already holds a drive (use `lorefs join`
for that one) and a bucket that already holds a drive of the same name.

A new drive keeps deleted and replaced files for 7 days, has its change log
on (24 hours, 100,000 entries) and carries the rules file for agents at its
top level.

It prints the first **join code** and offers to connect this computer.
Colleagues install LoreFS, run `lorefs join` and paste the code. Treat the
code like a password.

## Try it first

`lorefs create --local` makes a drive on this computer only: its index is a
file and its files are a folder under `~/.local/share/lorefs/local/<name>/`.
It needs no account, network or password. Version history, sandboxes and the
trash work as on a shared drive. `lorefs leave --delete-drive` removes it.

## Joining and leaving

- `lorefs invite` prints a join code again from a computer that is connected.
- `lorefs join` connects a computer. Different user numbers on different
  computers do not matter: every computer acts as the user who created the
  drive.
- `lorefs leave` disconnects this computer and leaves the drive untouched.
