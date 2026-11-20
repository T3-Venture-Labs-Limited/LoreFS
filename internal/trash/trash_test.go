package trash

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
)

// put creates a file in a fake trash, the way JuiceFS lays it out.
func put(t *testing.T, root, hour, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, ".trash", hour)
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o444); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestListFiltersAndOrders(t *testing.T) {
	root := t.TempDir()
	put(t, root, "2026-11-18-09", "12-340-reel.mp4", "old reel")
	put(t, root, "2026-11-19-04", "12-512-Reel Final.MP4", "new reel")
	put(t, root, "2026-11-19-04", "7-513-notes.md", "notes")
	// LoreFS's own leftovers must not be shown.
	put(t, root, "2026-11-19-04", "9-600-20261118T132920.609Z~alice~welcome.md", "version")
	put(t, root, "2026-11-19-04", "9-601-.deleted", "")
	put(t, root, "2026-11-19-04", "9-602-2026-11-18.jsonl", "{}")
	put(t, root, "2026-11-19-04", "9-603-.lorefs-restore-1~x.md", "tmp")

	all, err := List(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(all), all)
	}
	if all[len(all)-1].Name != "reel.mp4" {
		t.Fatalf("oldest should be last: %+v", all)
	}
	reels, _ := List(root, "REEL")
	if len(reels) != 2 {
		t.Fatalf("filter should ignore case: %+v", reels)
	}
	if none, _ := List(root, "nothing-like-this"); len(none) != 0 {
		t.Fatal("filter should be able to match nothing")
	}
	if e, _ := List(t.TempDir(), ""); e != nil {
		t.Fatal("a drive without a trash folder lists nothing")
	}
}

func TestFindPrefersExactThenAnyCase(t *testing.T) {
	root := t.TempDir()
	put(t, root, "2026-11-19-04", "1-2-Clip.mp4", "A")
	put(t, root, "2026-11-19-05", "1-3-clip.mp4", "B")
	e, ok, _ := Find(root, "Clip.mp4")
	if !ok || e.Name != "Clip.mp4" {
		t.Fatalf("exact name should win: %+v", e)
	}
	e, ok, _ = Find(root, "CLIP.MP4")
	if !ok {
		t.Fatal("should fall back to ignoring case")
	}
	if _, ok, _ := Find(root, "other.mp4"); ok {
		t.Fatal("should not find a missing name")
	}
}

func TestRestoreNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	put(t, root, "2026-11-19-04", "1-2-clip.mp4", "the deleted one")
	e, _, _ := Find(root, "clip.mp4")
	dest := filepath.Join(root, "media")

	p1, err := Restore(e, dest)
	if err != nil || filepath.Base(p1) != "clip.mp4" {
		t.Fatalf("first restore: %s %v", p1, err)
	}
	p2, err := Restore(e, dest)
	if err != nil || filepath.Base(p2) != "clip (restored).mp4" {
		t.Fatalf("second restore: %s %v", p2, err)
	}
	p3, _ := Restore(e, dest)
	if filepath.Base(p3) != "clip (restored 2).mp4" {
		t.Fatalf("third restore: %s", p3)
	}
	for _, p := range []string{p1, p2, p3} {
		b, _ := os.ReadFile(p)
		if string(b) != "the deleted one" {
			t.Fatalf("%s has wrong content %q", p, b)
		}
		st, _ := os.Stat(p)
		if st.Mode().Perm()&0o200 == 0 {
			t.Fatalf("%s should be writable", p)
		}
		if time.Since(st.ModTime()) > time.Minute {
			t.Fatalf("%s should carry the time it was restored", p)
		}
	}
}

func TestDir(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "media", "reels"), 0o755)
	s := history.New(root, "alice", jfs.Copy{})
	if got := Dir(s, filepath.Join(root, "media", "reels")); got != filepath.Join(s.Root, "media", "reels") {
		t.Fatalf("inside the drive: %s", got)
	}
	if got := Dir(s, t.TempDir()); got != s.Root {
		t.Fatalf("outside the drive should give the top: %s", got)
	}
	if got := Dir(s, filepath.Join(root, ".lorefs", "history")); got != s.Root {
		t.Fatalf("inside the bookkeeping should give the top: %s", got)
	}
}
