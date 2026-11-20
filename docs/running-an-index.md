# Running your own index

`lorefs server init` writes a folder that runs the drive's index, a
[Valkey](https://valkey.io) server, with Docker. You need a computer that
the drive's users can reach over the network and that has Docker with the
compose plugin.

```
lorefs server init --address 203.0.113.7 lorefs-index
```

`--address` is the IP address or name the other computers will use. The
certificate is made for exactly that address. Other options: `--port`
(default 6379), `--memory-mb` (default 400), `--image`, `--name`.

The command refuses a folder that already exists and writes everything
or nothing. The folder holds:

| | |
|---|---|
| `compose.yaml` | how Docker runs the index (runs as your user, restarts by itself) |
| `valkey.conf` | encrypted connections only, a password, append-only file synced every second, no eviction, a memory limit |
| `tls/ca.crt` | the certificate authority; computers need it, it is public |
| `tls/server.crt`, `tls/server.key` | the server's certificate (valid ten years, for the given address only) and key |
| `secrets/` | the password and the authority's key; keep it private and keep a copy |
| `data/` | the index's data |
| `server.json` | the address and port, read by `lorefs create --server-dir` |

Copy the folder to the server (if it is not this computer), then:

```
cd lorefs-index && docker compose up -d
```

Open the port to the computers that will use the drive and to nobody else.
The password is what protects the index; the connection is encrypted.

Then, from any computer: `lorefs create --server-dir lorefs-index --bucket ...`.

## When the memory is full

The index uses memory in proportion to the number of files (about 1 KB per
file with its versions). At the limit it refuses new files and keeps
everything it has. `lorefs status` warns at 80%. Raise `maxmemory` in
`valkey.conf` and run `docker compose restart`.

## Moving or replacing it

The index can be rebuilt from the copy JuiceFS writes into the bucket every
hour: see [backup-and-restore.md](backup-and-restore.md). If the address
changes, make a new bundle for the new address, restore into it, and give
each computer a new join code.
