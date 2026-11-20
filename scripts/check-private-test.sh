#!/usr/bin/env bash
# Proves that scripts/check-private.sh catches what it must, and nothing
# else: plants private words and key-shaped text in a throwaway folder.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
CHECK="$HERE/check-private.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
export LOREFS_PRIVATE_WORDS="$T/words"
printf '# test list\nfoobarcompany\nre:\\bproject-[0-9]{3}\\b\n' > "$T/words"
pass=0; failn=0
expect() { # expect <exit code> <description> <file name> <content>
  local want="$1" desc="$2" name="$3" content="$4"
  rm -rf "$T/tree"; mkdir -p "$T/tree"; printf '%s\n' "$content" > "$T/tree/$name"
  "$CHECK" --path "$T/tree" >/dev/null 2>&1; local got=$?
  if [ "$got" = "$want" ]; then pass=$((pass+1)); printf '  ok   %s\n' "$desc"
  else failn=$((failn+1)); printf '  FAIL %s (exit %s, wanted %s)\n' "$desc" "$got" "$want"; fi
}
expect 0 "clean text passes"                       a.md "A drive shared by a team."
expect 1 "a private word is caught"                a.md "written for FooBarCompany staff"
expect 1 "a private pattern is caught"             a.md "see project-482 for details"
expect 0 "a near miss of the pattern passes"       a.md "see project-48 for details"
expect 1 "a private key is caught"                 k.txt "-----BEGIN OPENSSH PRIVATE KEY-----"
expect 1 "a join code is caught"                   c.txt "lorefs2_$(printf 'A%.0s' $(seq 1 60))"
expect 0 "a short example join code passes"        c.txt "lorefs2_<code>"
expect 1 "a DigitalOcean token is caught"          t.txt "dop_v1_$(printf 'a%.0s' $(seq 1 40))"
expect 1 "an access key is caught"                 t.txt "key AKIAABCDEFGHIJKLMNOP"
expect 1 "a password assignment is caught"         e.env "META_PASSWORD=abcdefghijklmnop1234"
expect 0 "a password placeholder passes"           e.env "META_PASSWORD=<your password>"
expect 1 "a public address is caught"              n.md "the index is at 8.8.4.4 port 6379"
expect 0 "a documentation address passes"          n.md "the index is at 203.0.113.7 port 6379"
expect 0 "a private address passes"                n.md "try 192.168.1.20 or 10.0.0.5 or 127.0.0.1"
expect 0 "a version number passes"                 n.md "JuiceFS 1.4.2.1 and 2026.11.20.1"
rm -rf "$T/tree"; mkdir -p "$T/tree"; printf 'foobarcompany\0\1\2' > "$T/tree/b.bin"
"$CHECK" --path "$T/tree" >/dev/null 2>&1; got=$?
if [ "$got" = 0 ]; then pass=$((pass+1)); echo "  ok   a binary file is skipped"; else failn=$((failn+1)); echo "  FAIL a binary file is skipped (exit $got)"; fi
# --require-words must fail without a list.
LOREFS_PRIVATE_WORDS="$T/missing" "$CHECK" --path "$T/tree" --require-words >/dev/null 2>&1; got=$?
if [ "$got" = 2 ]; then pass=$((pass+1)); echo "  ok   --require-words fails without a list"; else failn=$((failn+1)); echo "  FAIL --require-words (exit $got)"; fi
printf '\n%d passed, %d failed\n' "$pass" "$failn"
[ "$failn" = 0 ]
