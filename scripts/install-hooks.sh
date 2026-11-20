#!/usr/bin/env bash
# Installs the git hooks that run the privacy check before every commit
# (on the staged files) and on the commit message. Run once after cloning.
set -euo pipefail
cd "$(dirname "$0")/.."
hooks="$(git rev-parse --git-path hooks)"
mkdir -p "$hooks"
cat > "$hooks/pre-commit" <<'HOOK'
#!/usr/bin/env bash
exec "$(git rev-parse --show-toplevel)/scripts/check-private.sh" --staged
HOOK
cat > "$hooks/commit-msg" <<'HOOK'
#!/usr/bin/env bash
exec "$(git rev-parse --show-toplevel)/scripts/check-private.sh" --staged --message "$1"
HOOK
chmod +x "$hooks/pre-commit" "$hooks/commit-msg"
echo "Installed the privacy check as pre-commit and commit-msg hooks in $hooks"
