# Security

## Reporting a vulnerability

Please do not open a public issue for a security problem. Use GitHub's
private vulnerability reporting for this repository (the "Report a
vulnerability" button under the Security tab). Say what you found, how to
reproduce it and what you think it allows. You will get an answer within a
few days.

## What a join code can do

A join code holds the address and password of the drive's index and, for
an index with its own certificate authority, that certificate. Anyone who
has it can read, change and delete every file on the drive. Treat it like
a password: send it through a private channel, never in a public chat or
an email to many people.

## Replacing secrets

- **Index password:** change it on the index server, then give every
  computer a new join code (`lorefs leave --keep-cache`, then `lorefs join`).
- **Storage key:** create a new key at the storage provider, change it in
  the drive's settings (`juicefs config <index> --access-key ... --secret-key ...`,
  with the drive disconnected on every computer), then revoke the old key.
- **Backup key:** run `lorefs backup setup` again with the new key.

## What LoreFS protects

- Connections to the index are encrypted and the index's certificate is
  checked (a join code can carry the authority that signed it).
- Secrets are read from hidden prompts, the environment or a private file,
  never from command-line arguments, and are not printed.
- Settings and keys on a computer are readable only by its user.

## What it does not protect

- File contents are not encrypted by LoreFS; they are as private as the
  storage bucket that holds them.
- Any computer that holds a join code can act as the drive's owner.
