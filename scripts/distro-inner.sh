#!/bin/sh
# Runs inside a container (see distro-test.sh): installs LoreFS from the
# release served at $LOREFS_RELEASE_URL as an ordinary user, creates a trial
# drive, saves a file twice and looks for two versions. Prints "ok ..." and
# "FAIL ..." lines and exits non-zero on the first failure.
# Mode "walkthrough" runs the first section of the README instead.
set -u
MODE="${1:-core}"
say() { printf '%s\n' "$*"; }
die() { say "FAIL $*"; exit 1; }

# Packages: what the installer and the tests need, by package manager.
if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq >/dev/null && apt-get install -y -qq curl ca-certificates fuse3 jq procps coreutils diffutils util-linux >/dev/null 2>&1 || die "installing packages (apt)"
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y -q curl fuse3 jq procps-ng diffutils util-linux >/dev/null 2>&1 || die "installing packages (dnf)"
elif command -v pacman >/dev/null 2>&1; then
  pacman -Syu --noconfirm --needed curl fuse3 jq procps-ng diffutils util-linux >/dev/null 2>&1 || die "installing packages (pacman)"
else die "no known package manager"; fi
command -v fusermount3 >/dev/null 2>&1 || die "fusermount3 is missing after installing fuse3"
chmod 666 /dev/fuse 2>/dev/null
chmod u+s "$(command -v fusermount3)" 2>/dev/null
id tester >/dev/null 2>&1 || useradd -m tester || die "useradd"

# What the ordinary user runs.
{
  cat <<SCRIPT
set -u
export HOME=/home/tester LOREFS_RELEASE_URL="$LOREFS_RELEASE_URL" LOREFS_NO_SYSTEMD=1 PATH="/home/tester/.local/bin:\$PATH"
export LOREFS_QUIET=300ms LOREFS_FOLLOW=2s LOREFS_HEALTH=2s
cd \$HOME
say() { printf '%s\n' "\$*"; }
die() { say "FAIL \$*"; exit 1; }
within() { s=\$1; shift; end=\$((\$(date +%s)+s)); until "\$@" >/dev/null 2>&1; do [ \$(date +%s) -ge \$end ] && return 1; sleep 0.3; done; }
start=\$(date +%s)
SCRIPT
  if [ "$MODE" = walkthrough ]; then
    # The commands of the README's first section, with the release address replaced.
    cat /tmp/readme-commands.sh
    cat <<'SCRIPT'
# Without systemd the services are started by hand, as for any place without a user session.
lorefs mount pre; lorefs mount run >$HOME/mount.log 2>&1 &
within 40 test -e $HOME/Lore/.config || die "the drive did not mount: $(tail -3 $HOME/mount.log)"
lorefs helper run >$HOME/helper.log 2>&1 &
sleep 2
echo "first version" > $HOME/Lore/notes.md; sleep 1; echo "second version" > $HOME/Lore/notes.md
within 30 sh -c '[ "$(lorefs history --json $HOME/Lore/notes.md | jq ".versions | length")" = 2 ]' || die "the README's example did not give two versions"
say "ok  the README's first section works: install, a trial drive, two saves, two versions"
lorefs status >/dev/null; code=$?
[ $code -le 1 ] && say "ok  lorefs status runs (exit $code)" || die "lorefs status exit $code"
say "ok  that took $(( $(date +%s) - start )) s after the packages were installed"
SCRIPT
  else
    cat <<'SCRIPT'
curl -fsSL "$LOREFS_RELEASE_URL/install.sh" | sh >$HOME/install.log 2>&1 || die "install: $(tail -3 $HOME/install.log)"
command -v lorefs >/dev/null || die "lorefs is not on the PATH after installing"
say "ok  installed: $(lorefs version | head -1)"
# No systemd user session here: join must stop before changing anything and explain.
before=$(find $HOME -mindepth 1 -not -path "$HOME/.local*" | sort | md5sum)
out=$(echo lorefs2_not-a-real-code | env -u LOREFS_NO_SYSTEMD lorefs join --token-stdin 2>&1); code=$?
after=$(find $HOME -mindepth 1 -not -path "$HOME/.local*" | sort | md5sum)
[ $code -ne 0 ] && echo "$out" | grep -q "no systemd user session" && [ "$before" = "$after" ] && [ ! -e $HOME/.config/lorefs ] \
  && say "ok  without a user session, join stops before changing anything and explains" || die "join without a user session: exit $code: $out"
# With no systemd session (a container) create writes the settings and leaves starting the
# mount to the person.
lorefs create --local --name distro --folder $HOME/Lore >$HOME/create.log 2>&1 || die "create --local: $(tail -3 $HOME/create.log)"
lorefs mount pre; lorefs mount run >$HOME/mount.log 2>&1 &
within 40 test -e $HOME/Lore/.config || die "the trial drive did not mount: $(tail -3 $HOME/mount.log)"
say "ok  a trial drive is created and mounts"
lorefs helper run >$HOME/helper.log 2>&1 &
sleep 2
mkdir -p $HOME/Lore/notes
echo "one" > $HOME/Lore/notes/plan.md; sleep 1; echo "two" > $HOME/Lore/notes/plan.md
within 30 sh -c '[ "$(lorefs history --json notes/plan.md | jq ".versions | length")" = 2 ]' || die "two saves did not give two versions: $(tail -3 $HOME/helper.log)"
say "ok  two saves give two versions"
[ "$(cat "$(lorefs open --path notes/plan.md 2)")" = one ] && say "ok  the older version holds the older content" || die "an older version holds the wrong content"
lorefs restore notes/plan.md 2 >/dev/null 2>&1; [ "$(cat $HOME/Lore/notes/plan.md)" = one ] && say "ok  a restore makes it current again" || die "restore"
rm $HOME/Lore/notes/plan.md; sleep 2; lorefs trash 2>&1 | grep -q plan && say "ok  a deleted file is in the trash" || die "the trash lacks the deleted file"
lorefs status >/dev/null; code=$?
[ $code -le 1 ] && say "ok  lorefs status runs (exit $code)" || die "lorefs status exit $code"
SCRIPT
  fi
} >/tmp/as-tester.sh
chmod 755 /tmp/as-tester.sh
runuser -u tester -- sh /tmp/as-tester.sh
