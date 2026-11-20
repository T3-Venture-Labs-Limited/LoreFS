#!/usr/bin/env bash
# Installs LoreFS for the current user: the `lorefs` command, JuiceFS, the
# background services and the file-manager entry. Everything goes under
# your home folder; no administrator password is needed.
#
# Run it again at any time to upgrade. Your settings are kept.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
BIN="${LOREFS_BIN_DIR:-$HOME/.local/bin}"

fail() { printf '\n%s\n' "$1" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || fail "LoreFS runs on Linux only for now."

# FUSE 3 is the one thing that needs an administrator to install.
if ! command -v fusermount3 >/dev/null 2>&1; then
  hint="sudo pacman -S fuse3"
  command -v apt-get >/dev/null 2>&1 && hint="sudo apt-get install fuse3"
  command -v dnf >/dev/null 2>&1 && hint="sudo dnf install fuse3"
  fail "LoreFS needs FUSE 3, which is not installed. Nothing was installed.
Run this once, then run the installer again:

  $hint"
fi

# Find the lorefs program: beside this script (release) or in bin/ (source).
if [ -x "$HERE/lorefs" ]; then SRC_LORE="$HERE/lorefs"
elif [ -x "$HERE/bin/lorefs" ]; then SRC_LORE="$HERE/bin/lorefs"
else fail "The lorefs program is missing from $HERE. Download the release again."; fi
if [ -x "$HERE/get-juicefs.sh" ]; then GET_JFS="$HERE/get-juicefs.sh"
else GET_JFS="$HERE/scripts/get-juicefs.sh"; fi

# Download and verify JuiceFS into a scratch folder first, so that a bad
# download leaves the machine untouched.
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
if [ -x "$BIN/juicefs" ]; then cp -p "$BIN/juicefs" "$stage/juicefs"; fi
"$GET_JFS" "$stage" || fail "JuiceFS could not be downloaded and verified. Nothing was installed."

mkdir -p "$BIN"
changed=0
put() { # put <source> <name>: replace only if different, in one step
  if ! cmp -s "$1" "$BIN/$2" 2>/dev/null; then
    install -m 0755 "$1" "$BIN/$2.new"
    mv -f "$BIN/$2.new" "$BIN/$2"
    changed=1
    echo "Installed $2"
  fi
}
put "$stage/juicefs" juicefs
put "$SRC_LORE" lorefs

# Remember where this was installed from, so that `lorefs update` can fetch
# and build the next version there. A release file has no such place.
if [ -d "$HERE/.git" ]; then "$BIN/lorefs" update --remember "$HERE" || true
else "$BIN/lorefs" update --forget || true; fi

# Service files and the file-manager entry come from inside lorefs itself.
units="$("$BIN/lorefs" setup-files || true)"
[ -n "$units" ] && echo "Updated the background services"

if [ -f "${LOREFS_CONFIG_DIR:-$HOME/.config/lorefs}/config.toml" ]; then
  # Already joined: this was an upgrade. The helper can always be
  # restarted safely. The drive itself is never reconnected from here:
  # that would pull the folder out from under whatever is using it.
  if [ "$changed" = 1 ] && [ -z "${LOREFS_NO_SYSTEMD:-}" ]; then
    systemctl --user try-restart lorefs-helper.service 2>/dev/null || true
  fi
  echo
  "$BIN/lorefs" version
  echo
  echo "LoreFS is up to date. Check the drive with: lorefs status"
  if [ -n "$units" ]; then
    echo
    echo "The drive itself keeps running the previous version until it next"
    echo "reconnects (at your next login). To switch now, close programs that"
    echo "are using the drive and run: systemctl --user restart lorefs-mount"
  fi
else
  echo
  "$BIN/lorefs" version
  echo
  echo "LoreFS is installed. To connect this computer to the drive, run:"
  echo
  echo "  lorefs join"
  echo
  echo "and paste the join code you were given."
fi

case ":$PATH:" in
  *":$BIN:"*) ;;
  *) echo; echo "Note: $BIN is not on your PATH. Use $BIN/lorefs, or add it to your PATH." ;;
esac
