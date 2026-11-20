#!/usr/bin/env bash
# Fails when a file holds something that must never be published:
#
#   - a word from a private list (the names of the project's first users,
#     their computers, companies, projects, addresses and buckets),
#   - text shaped like a private key, a provider token, a storage key or a
#     join code,
#   - a public IPv4 address (examples use the documentation ranges).
#
# The private list is NOT part of this repository. It is read from
# $LOREFS_PRIVATE_WORDS or ~/.config/lorefs-dev/private-words: one entry
# per line, matched as plain text ignoring case; a line starting with
# "re:" is an extended regular expression. Without the list only the
# shape checks run (as on the automatic test machines) and the script says
# so; add --require-words to fail instead.
#
# Usage:
#   scripts/check-private.sh              every tracked and not-ignored file
#   scripts/check-private.sh --staged     what is about to be committed
#   scripts/check-private.sh --message F  also check the commit message in F
#   scripts/check-private.sh --history    every commit in the history
#   scripts/check-private.sh --path DIR   a folder that is not a repository
#   scripts/check-private.sh --require-words
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2
WORDS="${LOREFS_PRIVATE_WORDS:-$HOME/.config/lorefs-dev/private-words}"
MODE=worktree; MSG=""; REQUIRE=0; DIR=""
while [ $# -gt 0 ]; do
  case "$1" in
    --staged) MODE=staged ;;
    --history) MODE=history ;;
    --message) MSG="$2"; shift ;;
    --path) MODE=path; DIR="$2"; shift ;;
    --require-words) REQUIRE=1 ;;
    -h|--help) sed -n '2,25p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 64 ;;
  esac
  shift
done

# Shape rules: name<TAB>extended regular expression.
SHAPES=$(printf '%s\n' \
  'a private key	-----BEGIN [A-Z ]*PRIVATE KEY-----' \
  'a join code	lorefs[12]_[A-Za-z0-9_-]{40,}' \
  'a DigitalOcean token	dop_v1_[a-f0-9]{20,}' \
  'a DigitalOcean storage key	DO00[A-Z0-9]{16,}' \
  'an AWS access key	AKIA[0-9A-Z]{16}' \
  'a GitHub token	gh[pousr]_[A-Za-z0-9]{30,}' \
  'a password or key assignment	(META_PASSWORD|SECRET_KEY|APP_KEY|secret-key|REDIS_PASSWORD)[=: ]+["'"'"']?[A-Za-z0-9/+_-]{16,}')
PUBLIC_IP='\b([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\b'

fail=0
report() { printf '  %s:%s  %s\n' "$1" "$2" "$3"; fail=1; }

word_args=(); regex_args=()
if [ -r "$WORDS" ]; then
  while IFS= read -r line; do
    case "$line" in ''|'#'*) continue ;; re:*) regex_args+=(-e "${line#re:}") ;; *) word_args+=(-e "$line") ;; esac
  done < "$WORDS"
elif [ "$REQUIRE" = 1 ]; then
  echo "The private word list was not found at $WORDS" >&2; exit 2
else
  echo "note: no private word list at $WORDS; only the shape checks ran" >&2
fi

# scan <label> <file with the content>
scan() {
  local label="$1" file="$2" n
  grep -qI . "$file" 2>/dev/null || return 0                  # binary or empty
  if [ ${#word_args[@]} -gt 0 ]; then
    while IFS=: read -r n _; do report "$label" "$n" "contains a word from the private list"; done \
      < <(grep -n -i -F "${word_args[@]}" "$file" 2>/dev/null)
  fi
  if [ ${#regex_args[@]} -gt 0 ]; then
    while IFS=: read -r n _; do report "$label" "$n" "matches a pattern from the private list"; done \
      < <(grep -n -i -E "${regex_args[@]}" "$file" 2>/dev/null)
  fi
  local name re
  while IFS=$'\t' read -r name re; do
    [ -n "$re" ] || continue
    while IFS=: read -r n _; do report "$label" "$n" "looks like $name"; done < <(grep -n -E -e "$re" "$file" 2>/dev/null)
  done <<< "$SHAPES"
  while IFS=: read -r n rest; do
    while read -r oa ob oc od; do
      [ "$oa" -le 255 ] && [ "$ob" -le 255 ] && [ "$oc" -le 255 ] && [ "$od" -le 255 ] || continue
      case "$oa.$ob.$oc" in
        127.*|10.*|0.0.0|192.168.*|169.254.*|203.0.113|198.51.100|192.0.2|255.255.255) continue ;;
        172.1[6-9].*|172.2[0-9].*|172.3[01].*) continue ;;
      esac
      case "$oa" in 1) [ "$ob" = 4 ] && continue ;; esac       # 1.4.x version numbers
      report "$label" "$n" "holds a public IPv4 address"
    done < <(printf '%s\n' "$rest" | grep -o -E "$PUBLIC_IP" | tr '.' ' ')
  done < <(grep -n -E "$PUBLIC_IP" "$file" 2>/dev/null)
}

tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
selfs="scripts/check-private.sh scripts/check-private-test.sh"   # they hold planted examples

case "$MODE" in
  worktree)
    if git rev-parse --git-dir >/dev/null 2>&1; then
      files=$(git ls-files --cached --others --exclude-standard)
    else
      files=$(find . -type f -not -path './.git/*' -not -path './openspec/*' | sed 's|^\./||')
    fi
    while IFS= read -r f; do [ -f "$f" ] && [[ " $selfs " != *" $f "* ]] && scan "$f" "$f"; done <<< "$files" ;;
  path)
    while IFS= read -r f; do scan "${f#"$DIR"/}" "$f"; done < <(find "$DIR" -type f -not -path '*/.git/*') ;;
  staged)
    while IFS= read -r f; do
      [[ " $selfs " == *" $f "* ]] && continue
      git show ":$f" > "$tmp" 2>/dev/null && scan "$f (staged)" "$tmp"
    done < <(git diff --cached --name-only --diff-filter=ACMR) ;;
  history)
    for c in $(git rev-list --reverse HEAD); do
      while IFS= read -r f; do
        [[ " $selfs " == *" $f "* ]] && continue
        git show "$c:$f" > "$tmp" 2>/dev/null && scan "$(git rev-parse --short "$c"):$f" "$tmp"
      done < <(git ls-tree -r --name-only "$c")
      git log -1 --format='%an <%ae>%n%B' "$c" > "$tmp"; scan "$(git rev-parse --short "$c") (message and author)" "$tmp"
    done ;;
esac
[ -n "$MSG" ] && [ -f "$MSG" ] && scan "commit message" "$MSG"

if [ "$fail" = 0 ]; then echo "privacy check: nothing private found"; exit 0; fi
echo "privacy check: FAILED. Remove or replace the lines above." >&2
exit 1
