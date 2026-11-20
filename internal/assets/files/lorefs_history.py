"""Nautilus entry "Version history" for files on the LoreFS drive.

Shown when exactly one file inside the drive folder is selected. It opens
`lorefs history <file>` in a floating terminal, where a version can be
previewed and restored.
"""
import os
import shlex
import shutil

from gi import require_version

require_version("Nautilus", "4.1")

from gi.repository import GObject, Gio, Nautilus


_cache = {"mtime": None, "folder": None}


def _drive_folder():
    """Read the drive folder from the machine's LoreFS settings.

    This runs inside the file manager on every right-click, so it must
    never touch a slow or remote folder: it reads one small local file and
    remembers the answer until that file changes.
    """
    base = os.environ.get("LOREFS_CONFIG_DIR") or os.path.expanduser("~/.config/lorefs")
    path = os.path.join(base, "config.toml")
    try:
        mtime = os.stat(path).st_mtime_ns
    except OSError:
        _cache["mtime"], _cache["folder"] = None, None
        return None
    if mtime == _cache["mtime"]:
        return _cache["folder"]
    folder = None
    try:
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                key, _, value = line.partition("=")
                if key.strip() == "folder":
                    folder = os.path.normpath(value.strip().strip('"'))
    except OSError:
        pass
    _cache["mtime"], _cache["folder"] = mtime, folder
    return folder


def _lore():
    return shutil.which("lorefs") or os.path.expanduser("~/.local/bin/lorefs")


class LoreFSHistory(GObject.GObject, Nautilus.MenuProvider):
    def _open_history(self, _item, path):
        command = shlex.join([_lore(), "history", path])
        # xdg-terminal-exec opens the user's preferred terminal. Without it
        # the entry is not offered at all (see get_file_items).
        terminal = shutil.which("xdg-terminal-exec")
        if not terminal:
            return
        argv = [terminal, "sh", "-c", command]
        Gio.Subprocess.new(argv, Gio.SubprocessFlags.NONE)

    def get_file_items(self, *args):
        files = args[-1]
        if len(files) != 1:
            return []
        location = files[0].get_location()
        path = location.get_path() if location else None
        if not path or files[0].is_directory():
            return []
        if not shutil.which("xdg-terminal-exec"):
            return []  # nothing to open the history view with
        folder = _drive_folder()
        if not folder:
            return []
        # Compare the text of the path only. Resolving it would ask the
        # filesystem about every folder on the way, and for a file on a
        # slow network mount that would freeze the file manager.
        real = os.path.normpath(path)
        if not real.startswith(folder + os.sep):
            return []
        if os.sep + ".lorefs" + os.sep in real:
            return []
        item = Nautilus.MenuItem(
            name="LoreFSHistory::open",
            label="Version history",
            tip="See earlier versions of this file and restore one",
        )
        item.connect("activate", self._open_history, real)
        return [item]
