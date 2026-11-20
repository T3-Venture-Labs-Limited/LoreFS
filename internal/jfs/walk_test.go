package jfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestReadDirAndWalk(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"b.md", "a.md", "sub/c.md", "sub/deep/d.md", "skipme/e.md"} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(p), 0o644)
	}
	os.Symlink("a.md", filepath.Join(root, "link"))

	infos, err := ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, i := range infos {
		names = append(names, i.Name())
	}
	if got := strings.Join(names, " "); got != "a.md b.md link skipme sub" {
		t.Fatalf("ReadDir should sort by name: %q", got)
	}
	if infos[0].Size() != 4 || infos[2].Mode()&os.ModeSymlink == 0 {
		t.Fatalf("ReadDir should give details without following links: %v %v", infos[0].Size(), infos[2].Mode())
	}
	if _, err := ReadDir(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatalf("a missing folder should say so: %v", err)
	}

	var seen []string
	pos := map[string]int{}
	active := 0
	err = Walk(root, func(p string, info os.FileInfo) error {
		active++
		if active != 1 {
			t.Error("fn must never run in two places at once")
		}
		defer func() { active-- }()
		rel, _ := filepath.Rel(root, p)
		if info.IsDir() && info.Name() == "skipme" {
			return filepath.SkipDir
		}
		pos[rel] = len(seen)
		seen = append(seen, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sorted := append([]string(nil), seen...)
	sort.Strings(sorted)
	if got := strings.Join(sorted, " "); got != "a.md b.md link sub sub/c.md sub/deep sub/deep/d.md" {
		t.Fatalf("Walk should visit everything except skipped folders: %q", got)
	}
	if pos["sub"] > pos["sub/c.md"] || pos["sub/deep"] > pos["sub/deep/d.md"] || pos["sub"] > pos["sub/deep"] {
		t.Fatalf("a folder must be reported before its content: %q", seen)
	}
	stop := errors.New("stop")
	n := 0
	if err := Walk(root, func(string, os.FileInfo) error { n++; return stop }); err != stop || n != 1 {
		t.Fatalf("an error should stop the walk at once: %v after %d", err, n)
	}
}

func TestWalkManyFolders(t *testing.T) {
	root := t.TempDir()
	want := 0
	for i := 0; i < 60; i++ {
		d := filepath.Join(root, "d"+string(rune('a'+i%26)), "e"+string(rune('a'+i%7)), "f"+string(rune('a'+i%3)))
		os.MkdirAll(d, 0o755)
		p := filepath.Join(d, "file"+string(rune('a'+i%5))+".md")
		if _, err := os.Stat(p); err != nil {
			want++
		}
		os.WriteFile(p, []byte("x"), 0o644)
	}
	files := 0
	if err := Walk(root, func(_ string, info os.FileInfo) error {
		if !info.IsDir() {
			files++
		}
		return nil
	}); err != nil || files != want {
		t.Fatalf("found %d files, want %d (%v)", files, want, err)
	}
}

func TestRemoveTree(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "x", "y"), 0o755)
	os.WriteFile(filepath.Join(root, "x", "y", "f"), []byte("x"), 0o644)
	// With no usable juicefs program the real remover falls back to
	// deleting file by file.
	for _, r := range []Cloner{Copy{}, Exec{Bin: "/nonexistent/juicefs"}} {
		os.MkdirAll(filepath.Join(root, "x", "y"), 0o755)
		if err := r.RemoveTree(filepath.Join(root, "x")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, "x")); !os.IsNotExist(err) {
			t.Fatal("the folder should be gone")
		}
		if err := r.RemoveTree(filepath.Join(root, "x")); err != nil {
			t.Fatalf("removing a folder that is not there is not an error: %v", err)
		}
	}
}

func TestWalkWithEntersEveryFolderOnceAndStops(t *testing.T) {
	root := t.TempDir()
	want := map[string]bool{}
	for _, d := range []string{"a", "a/b", "a/b/c", "d", "skipped", "skipped/inner"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
		os.WriteFile(filepath.Join(root, d, "f.txt"), []byte("x"), 0o644)
		if !strings.HasPrefix(d, "skipped") {
			want[filepath.Join(root, d)] = true
		}
	}
	var mu sync.Mutex
	entered := map[string]int{}
	err := WalkWith(root, WalkOptions{Enter: func(dir string) {
		mu.Lock()
		entered[dir]++
		mu.Unlock()
	}}, func(p string, info os.FileInfo) error {
		if info.IsDir() && info.Name() == "skipped" {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entered) != len(want) {
		t.Fatalf("entered %v, want %v", entered, want)
	}
	for d, n := range entered {
		if !want[d] || n != 1 {
			t.Fatalf("folder %s entered %d times (wanted: %v)", d, n, want[d])
		}
	}

	// A walk that is told to stop begins no more folders and says why.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	seen := 0
	err = WalkWith(root, WalkOptions{Ctx: ctx, Enter: func(string) { seen++ }}, func(string, os.FileInfo) error { return nil })
	if !errors.Is(err, context.Canceled) || seen != 0 {
		t.Fatalf("a stopped walk: err %v, %d folders entered", err, seen)
	}
}
