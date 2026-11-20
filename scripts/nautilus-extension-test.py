#!/usr/bin/env python3
"""Checks the file-manager entry's logic against a stand-in for GNOME Files'
Python API (no desktop needed): when the entry is offered, what it runs, and
that it never touches the drive's own bookkeeping folder.

Usage: python3 scripts/nautilus-extension-test.py
"""
import importlib.util
import os
import pathlib
import shutil
import sys
import tempfile
import types

ROOT = pathlib.Path(__file__).resolve().parent.parent
EXT = ROOT / "internal" / "assets" / "files" / "lorefs_history.py"

launched = []


class _Item:
    def __init__(self, name, label, tip):
        self.name, self.label, self.tip, self.handlers = name, label, tip, []

    def connect(self, signal, fn, *args):
        self.handlers.append((signal, fn, args))


gi = types.ModuleType("gi")
gi.require_version = lambda *a, **k: None
repo = types.ModuleType("gi.repository")
class _GObject: pass
class _MenuProvider: pass
repo.GObject = types.SimpleNamespace(GObject=_GObject)
repo.Nautilus = types.SimpleNamespace(MenuProvider=_MenuProvider, MenuItem=_Item)


class _Subprocess:
    @staticmethod
    def new(argv, flags):
        launched.append(argv)


repo.Gio = types.SimpleNamespace(Subprocess=_Subprocess, SubprocessFlags=types.SimpleNamespace(NONE=0))
sys.modules.update({"gi": gi, "gi.repository": repo})

spec = importlib.util.spec_from_file_location("lorefs_history", EXT)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


class _File:
    def __init__(self, path, directory=False):
        self.path, self.directory = path, directory

    def get_location(self):
        return types.SimpleNamespace(get_path=lambda: self.path)

    def is_directory(self):
        return self.directory


failures = []


def check(name, cond):
    print(("ok   " if cond else "FAIL ") + name)
    if not cond:
        failures.append(name)


with tempfile.TemporaryDirectory() as home:
    folder = os.path.join(home, "Lore")
    os.makedirs(os.path.join(folder, "notes"))
    cfg = os.path.join(home, ".config", "lorefs")
    os.makedirs(cfg)
    with open(os.path.join(cfg, "config.toml"), "w") as fh:
        fh.write('drive_name = "x"\nfolder = "%s"\n' % folder)
    os.environ["HOME"] = home
    os.environ["LOREFS_CONFIG_DIR"] = cfg
    fake_bin = os.path.join(home, "bin")
    os.makedirs(fake_bin)
    for tool in ("xdg-terminal-exec", "lorefs"):
        p = os.path.join(fake_bin, tool)
        open(p, "w").write("#!/bin/sh\n")
        os.chmod(p, 0o755)
    os.environ["PATH"] = fake_bin  # only the pretend tools, so a real xdg-terminal-exec cannot interfere

    ext = mod.LoreFSHistory()
    inside = os.path.join(folder, "notes", "plan.md")
    items = ext.get_file_items(None, [_File(inside)])
    check("a file on the drive gets the Version history entry", len(items) == 1 and items[0].label == "Version history")
    check("a file elsewhere gets none", ext.get_file_items(None, [_File(os.path.join(home, "other.md"))]) == [])
    check("a folder gets none", ext.get_file_items(None, [_File(os.path.join(folder, "notes"), True)]) == [])
    check("two files selected get none", ext.get_file_items(None, [_File(inside), _File(inside)]) == [])
    check("the drive's bookkeeping folder gets none", ext.get_file_items(None, [_File(os.path.join(folder, ".lorefs", "history", "x"))]) == [])
    check("a folder whose name only starts like the drive's gets none", ext.get_file_items(None, [_File(folder + "-copy/a.md")]) == [])

    items[0].handlers[0][1](items[0], *items[0].handlers[0][2])
    argv = launched[-1] if launched else []
    check("activating it opens the terminal with lorefs history for that file",
          len(argv) == 4 and argv[0].endswith("xdg-terminal-exec") and argv[1:3] == ["sh", "-c"]
          and argv[3].endswith("history " + inside) and "lorefs" in argv[3])

    # Without xdg-terminal-exec the entry is not offered at all.
    os.remove(os.path.join(fake_bin, "xdg-terminal-exec"))
    check("without xdg-terminal-exec no entry is offered", ext.get_file_items(None, [_File(inside)]) == [])

    # Without a settings file there is no drive and no entry.
    os.remove(os.path.join(cfg, "config.toml"))
    open(os.path.join(fake_bin, "xdg-terminal-exec"), "w").write("#!/bin/sh\n")
    os.chmod(os.path.join(fake_bin, "xdg-terminal-exec"), 0o755)
    mod._cache.clear()
    check("a computer that has not joined gets no entry", ext.get_file_items(None, [_File(inside)]) == [])

sys.exit(1 if failures else 0)
