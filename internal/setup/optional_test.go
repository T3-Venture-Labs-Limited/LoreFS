package setup

import (
	"errors"
	"strings"
	"testing"
)

func probe(tools map[string]bool, files map[string]bool, globs map[string][]string) Probe {
	return Probe{
		LookPath: func(n string) (string, error) {
			if tools[n] {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		Glob:   func(p string) ([]string, error) { return globs[p], nil },
		Exists: func(p string) bool { return files[p] },
		Home:   "/home/alice",
	}
}

func TestEverythingPresent(t *testing.T) {
	p := probe(map[string]bool{"nautilus": true, "notify-send": true, "xdg-terminal-exec": true, "fzf": true},
		nil, map[string][]string{"/usr/lib*/nautilus/extensions-*/libnautilus-python.so": {"/usr/lib/nautilus/extensions-4/libnautilus-python.so"}})
	pieces := DetectOptional(p)
	if len(Skipped(pieces)) != 0 || Report(pieces) != "" {
		t.Fatalf("%+v", Skipped(pieces))
	}
	for _, id := range []string{PieceFileManager, PieceBookmarks, PieceNotify, PieceTerminal, PieceChooser} {
		if !found(pieces, id) {
			t.Errorf("%s should be found", id)
		}
	}
}

func TestEverythingAbsent(t *testing.T) {
	pieces := DetectOptional(probe(nil, nil, nil))
	if len(Skipped(pieces)) != 5 {
		t.Fatalf("%+v", pieces)
	}
	r := Report(pieces)
	for _, want := range []string{"were skipped", "GNOME Files with Python extensions", "notify-send", "xdg-terminal-exec", "fzf or gum", "to get it:"} {
		if !strings.Contains(r, want) {
			t.Errorf("report lacks %q:\n%s", want, r)
		}
	}
}

func TestNautilusWithoutItsPythonSupportIsNotEnough(t *testing.T) {
	pieces := DetectOptional(probe(map[string]bool{"nautilus": true}, nil, nil))
	if found(pieces, PieceFileManager) {
		t.Fatal("the extension needs nautilus-python")
	}
	if !found(pieces, PieceBookmarks) {
		t.Fatal("GNOME Files reads GTK bookmarks")
	}
}

func TestBookmarksNeedAGTKFolderOrFileManager(t *testing.T) {
	if found(DetectOptional(probe(nil, nil, nil)), PieceBookmarks) {
		t.Fatal("no file manager, no sidebar")
	}
	if !found(DetectOptional(probe(nil, map[string]bool{"/home/alice/.config/gtk-3.0": true}, nil)), PieceBookmarks) {
		t.Fatal("a GTK settings folder is enough")
	}
	if !found(DetectOptional(probe(map[string]bool{"thunar": true}, nil, nil)), PieceBookmarks) {
		t.Fatal("Thunar reads GTK bookmarks")
	}
}

func TestGumAloneIsAChooser(t *testing.T) {
	if !found(DetectOptional(probe(map[string]bool{"gum": true}, nil, nil)), PieceChooser) {
		t.Fatal("gum is enough")
	}
}
