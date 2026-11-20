package history

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
)

// clock is a settable time source for tests.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)}
	s := New(t.TempDir(), "Alice's PC", jfs.Copy{})
	s.Now = c.now
	return s, c
}

// write puts content in a file of the drive and gives it the clock's time.
func write(t *testing.T, s *Store, c *clock, rel, content string) {
	t.Helper()
	p := filepath.Join(s.Root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, c.t, c.t); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustRecord(t *testing.T, s *Store, rel string, want bool) {
	t.Helper()
	got, err := s.Record(rel)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Record(%s) made a version = %v, want %v", rel, got, want)
	}
}

func TestMachineNameIsCleaned(t *testing.T) {
	s, _ := newStore(t)
	if s.Machine != "alice-s-pc" {
		t.Fatalf("machine = %q", s.Machine)
	}
	if CleanMachine("  ~~ ") != "machine" {
		t.Fatal("empty name should fall back")
	}
}

func TestRecordNewAndChanged(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "notes/plan.md", "one")
	mustRecord(t, s, "notes/plan.md", true)
	mustRecord(t, s, "notes/plan.md", false) // unchanged: no second version

	c.advance(time.Minute)
	write(t, s, c, "notes/plan.md", "two!")
	mustRecord(t, s, "notes/plan.md", true)

	vs, err := s.List("notes/plan.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("want 2 versions, got %d", len(vs))
	}
	if read(t, vs[0].Path) != "two!" || read(t, vs[1].Path) != "one" {
		t.Fatal("versions are not newest first with the right content")
	}
	if vs[0].Machine != "alice-s-pc" || vs[0].Size != 4 {
		t.Fatalf("unexpected version: %+v", vs[0])
	}
	st, _ := os.Stat(vs[0].Path)
	if st.Mode().Perm()&0o222 != 0 {
		t.Fatalf("version should be read-only, mode %v", st.Mode().Perm())
	}
}

func TestSameSizeDifferentTimeIsANewVersion(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "a.txt", "aaaa")
	mustRecord(t, s, "a.txt", true)
	c.advance(time.Second)
	write(t, s, c, "a.txt", "bbbb")
	mustRecord(t, s, "a.txt", true)
}

func TestSkippedAndSpecialPaths(t *testing.T) {
	s, c := newStore(t)
	for _, rel := range []string{"x.swp", "doc.txt~", ".~lock.report.odt#", "movie.mp4.part",
		"repo/.git/config", "app/node_modules/x/index.js", ".lorefs-restore-1~x.md", ".goutputstream-AB12"} {
		write(t, s, c, rel, "tmp")
		mustRecord(t, s, rel, false)
	}
	if err := os.Mkdir(filepath.Join(s.Root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustRecord(t, s, "dir", false)
	mustRecord(t, s, "missing.txt", false)
	if _, err := os.Stat(s.historyRoot()); !os.IsNotExist(err) {
		t.Fatal("skipped paths must not create history")
	}
	// The drive's own bookkeeping is never versioned either.
	write(t, s, c, ".lorefs/journal/x/2026-11-18.jsonl", "{}")
	mustRecord(t, s, ".lorefs/journal/x/2026-11-18.jsonl", false)
}

func TestRelRejectsOutsideAndBookkeeping(t *testing.T) {
	s, _ := newStore(t)
	for _, p := range []string{"/etc/passwd", s.Root, filepath.Join(s.Root, ".lorefs", "history", "x"),
		filepath.Join(s.Root, "..", "other")} {
		if _, err := s.Rel(p); !errors.Is(err, ErrOutside) {
			t.Errorf("Rel(%s) = %v, want ErrOutside", p, err)
		}
	}
	rel, err := s.Rel(filepath.Join(s.Root, "a", "b.txt"))
	if err != nil || rel != filepath.Join("a", "b.txt") {
		t.Fatalf("Rel = %q, %v", rel, err)
	}
}

func TestRestoreKeepsReplacedContent(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "plan.md", "first")
	mustRecord(t, s, "plan.md", true)
	c.advance(time.Minute)
	write(t, s, c, "plan.md", "second")
	mustRecord(t, s, "plan.md", true)
	c.advance(time.Minute)
	// An unrecorded edit: restore must keep it too.
	write(t, s, c, "plan.md", "third, never recorded")
	c.advance(time.Minute)

	old, err := s.Find("plan.md", "2") // second newest = "first"
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Restore("plan.md", old); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(s.Root, "plan.md")); got != "first" {
		t.Fatalf("file content after restore = %q", got)
	}
	vs, _ := s.List("plan.md")
	var contents []string
	for _, v := range vs {
		contents = append(contents, read(t, v.Path))
	}
	want := []string{"first", "third, never recorded", "second", "first"}
	if len(contents) != len(want) {
		t.Fatalf("versions = %q, want %q", contents, want)
	}
	for i := range want {
		if contents[i] != want[i] {
			t.Fatalf("versions = %q, want %q", contents, want)
		}
	}
	// The restored file is writable again.
	st, _ := os.Stat(filepath.Join(s.Root, "plan.md"))
	if st.Mode().Perm()&0o200 == 0 {
		t.Fatal("restored file should be writable")
	}
}

func TestDeleteAndBringBack(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "docs/brief.md", "keep me")
	mustRecord(t, s, "docs/brief.md", true)
	if err := os.RemoveAll(filepath.Join(s.Root, "docs")); err != nil {
		t.Fatal(err)
	}
	c.advance(time.Hour)
	if err := s.MarkDeleted("docs/brief.md"); err != nil {
		t.Fatal(err)
	}
	if at, ok := s.DeletedAt("docs/brief.md"); !ok || !at.Equal(c.t) {
		t.Fatalf("DeletedAt = %v, %v", at, ok)
	}
	vs, _ := s.List("docs/brief.md")
	if len(vs) != 1 {
		t.Fatalf("a deleted file keeps its versions, got %d", len(vs))
	}
	c.advance(time.Hour)
	if err := s.Restore("docs/brief.md", vs[0]); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(s.Root, "docs/brief.md")) != "keep me" {
		t.Fatal("restore did not recreate the file")
	}
	if _, ok := s.DeletedAt("docs/brief.md"); ok {
		t.Fatal("the deleted mark should be gone after restore")
	}
}

func TestMarkDeletedWithoutHistoryDoesNothing(t *testing.T) {
	s, _ := newStore(t)
	if err := s.MarkDeleted("never/seen.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.HistDir("never/seen.txt")); !os.IsNotExist(err) {
		t.Fatal("no history folder should be created")
	}
}

func TestRenameFileAndFolder(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "a/one.md", "1")
	mustRecord(t, s, "a/one.md", true)
	c.advance(time.Minute)
	write(t, s, c, "a/one.md", "11")
	mustRecord(t, s, "a/one.md", true)

	// Rename the file.
	os.Rename(filepath.Join(s.Root, "a/one.md"), filepath.Join(s.Root, "a/uno.md"))
	if err := s.Rename("a/one.md", "a/uno.md"); err != nil {
		t.Fatal(err)
	}
	if vs, _ := s.List("a/uno.md"); len(vs) != 2 {
		t.Fatalf("history should follow a renamed file, got %d", len(vs))
	}
	if vs, _ := s.List("a/one.md"); len(vs) != 0 {
		t.Fatal("old path should have no history")
	}

	// Move the folder.
	os.MkdirAll(filepath.Join(s.Root, "b"), 0o755)
	os.Rename(filepath.Join(s.Root, "a"), filepath.Join(s.Root, "b/moved"))
	if err := s.Rename("a", "b/moved"); err != nil {
		t.Fatal(err)
	}
	if vs, _ := s.List("b/moved/uno.md"); len(vs) != 2 {
		t.Fatalf("history should follow a moved folder, got %d", len(vs))
	}
}

func TestRenameOntoFileWithHistoryKeepsBoth(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "a.md", "A")
	mustRecord(t, s, "a.md", true)
	write(t, s, c, "b.md", "B")
	mustRecord(t, s, "b.md", true)
	os.Rename(filepath.Join(s.Root, "b.md"), filepath.Join(s.Root, "a.md"))
	c.advance(time.Minute)
	if err := s.Rename("b.md", "a.md"); err != nil {
		t.Fatal(err)
	}
	if vs, _ := s.List("a.md"); len(vs) != 1 || read(t, vs[0].Path) != "A" {
		t.Fatal("target history must be untouched")
	}
	if _, ok := s.DeletedAt("b.md"); !ok {
		t.Fatal("source should be marked deleted")
	}
}

func day(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func TestPruneRules(t *testing.T) {
	s, c := newStore(t)
	// A file changed long ago and again recently: the old version was
	// replaced only 5 days ago, so it is kept.
	write(t, s, c, "recent.md", "v1")
	mustRecord(t, s, "recent.md", true)
	// A file with two old versions: the older was replaced 50 days ago.
	write(t, s, c, "old.md", "v1")
	mustRecord(t, s, "old.md", true)
	// A file whose only version is old.
	write(t, s, c, "single.md", "only")
	mustRecord(t, s, "single.md", true)
	// A deleted file.
	write(t, s, c, "gone/x.md", "bye")
	mustRecord(t, s, "gone/x.md", true)

	c.advance(day(10))
	write(t, s, c, "old.md", "v2")
	mustRecord(t, s, "old.md", true)
	os.RemoveAll(filepath.Join(s.Root, "gone"))
	if err := s.MarkDeleted("gone/x.md"); err != nil {
		t.Fatal(err)
	}

	c.advance(day(45)) // day 55
	write(t, s, c, "recent.md", "v2")
	mustRecord(t, s, "recent.md", true)

	c.advance(day(5)) // day 60: old.md v1 replaced 50 days ago, gone deleted 50 days ago
	res, err := s.Prune()
	if err != nil {
		t.Fatal(err)
	}
	count := func(rel string) int { vs, _ := s.List(rel); return len(vs) }
	if count("recent.md") != 2 {
		t.Errorf("recent.md: a version replaced 5 days ago must be kept, have %d", count("recent.md"))
	}
	if count("old.md") != 1 {
		t.Errorf("old.md: a version replaced 50 days ago must go, have %d", count("old.md"))
	}
	if count("single.md") != 1 {
		t.Errorf("single.md: the newest version of an existing file is always kept")
	}
	if count("gone/x.md") != 0 {
		t.Errorf("gone/x.md: a file deleted 50 days ago loses its versions")
	}
	if _, err := os.Stat(s.HistDir("gone")); !os.IsNotExist(err) {
		t.Errorf("emptied history folders should be removed")
	}
	if res.Versions != 2 {
		t.Errorf("removed %d versions, want 2", res.Versions)
	}
}

func TestPruneKeepsRecentlyDeleted(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "x.md", "content")
	mustRecord(t, s, "x.md", true)
	c.advance(day(100)) // the version itself is old
	os.Remove(filepath.Join(s.Root, "x.md"))
	s.MarkDeleted("x.md")
	c.advance(day(29))
	if _, err := s.Prune(); err != nil {
		t.Fatal(err)
	}
	if vs, _ := s.List("x.md"); len(vs) != 1 {
		t.Fatal("a file deleted 29 days ago must still be restorable")
	}
	c.advance(day(2))
	s.Prune()
	if vs, _ := s.List("x.md"); len(vs) != 0 {
		t.Fatal("a file deleted 31 days ago should be gone")
	}
}

func TestPruneAdoptsVanishedFiles(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "x.md", "content")
	mustRecord(t, s, "x.md", true)
	os.Remove(filepath.Join(s.Root, "x.md")) // deleted while nobody was watching
	c.advance(day(40))
	res, _ := s.Prune()
	if res.Adopted != 1 {
		t.Fatalf("adopted = %d, want 1", res.Adopted)
	}
	if vs, _ := s.List("x.md"); len(vs) != 1 {
		t.Fatal("retention starts when the deletion is noticed, not before")
	}
	c.advance(day(31))
	s.Prune()
	if vs, _ := s.List("x.md"); len(vs) != 0 {
		t.Fatal("should be removed 31 days after adoption")
	}
}

// halfCloner copies only the first half of a file, as the drive does
// for a file whose content has not all reached the storage yet.
type halfCloner struct{}

func (halfCloner) Clone(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b[:len(b)/2], 0o644)
}

func (halfCloner) RemoveTree(dir string) error { return os.RemoveAll(dir) }

// slowCloner takes a moment, as a clone on the real drive does, so that
// several requests for a version of one file overlap.
type slowCloner struct{ jfs.Copy }

func (c slowCloner) Clone(src, dst string) error {
	time.Sleep(30 * time.Millisecond)
	return c.Copy.Clone(src, dst)
}

func TestManyRequestsAtOnceMakeOneVersion(t *testing.T) {
	s, c := newStore(t)
	s.Cloner = slowCloner{}
	s.Now = time.Now // the test clock is not made for several goroutines
	write(t, s, c, "notes/plan.md", "the plan")
	var wg sync.WaitGroup
	made := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.Record("notes/plan.md")
			if err != nil {
				t.Error(err)
			}
			made <- ok
		}()
	}
	wg.Wait()
	close(made)
	n := 0
	for ok := range made {
		if ok {
			n++
		}
	}
	vs, _ := s.List("notes/plan.md")
	if n != 1 || len(vs) != 1 {
		t.Fatalf("8 requests at once: %d said they made a version, %d versions exist; want 1 and 1", n, len(vs))
	}
}

func TestAFileStillBeingWrittenGetsNoVersion(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "photo.jpg", "a picture that is still being written")
	s.Cloner = halfCloner{}
	mustRecord(t, s, "photo.jpg", false)
	if vs, _ := s.List("photo.jpg"); len(vs) != 0 {
		t.Fatalf("an incomplete copy was kept as a version: %v", vs)
	}
	if _, err := os.Stat(s.HistDir("photo.jpg")); !os.IsNotExist(err) {
		t.Fatalf("an empty history folder was left behind")
	}
	// Once the program has closed the file, it is recorded as usual.
	s.Cloner = jfs.Copy{}
	mustRecord(t, s, "photo.jpg", true)
}

func TestSameContentUnderANewTimeIsNotANewVersion(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "photo.jpg", "the picture")
	mustRecord(t, s, "photo.jpg", true)

	// A copy tool gives the file its original date after writing it.
	old := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	os.Chtimes(filepath.Join(s.Root, "photo.jpg"), old, old)
	c.advance(time.Minute)
	mustRecord(t, s, "photo.jpg", false)
	vs, _ := s.List("photo.jpg")
	if len(vs) != 1 {
		t.Fatalf("the same content must not be recorded twice: %d versions", len(vs))
	}
	if !vs[0].ModTime.Equal(old) {
		t.Fatalf("the version should now carry the file's time: %v", vs[0].ModTime)
	}
	// So the next check is settled by size and time alone.
	mustRecord(t, s, "photo.jpg", false)

	// Different content of the same length is a new version.
	c.advance(time.Minute)
	write(t, s, c, "photo.jpg", "THE PICTURE")
	mustRecord(t, s, "photo.jpg", true)
	if vs, _ := s.List("photo.jpg"); len(vs) != 2 {
		t.Fatalf("changed content of the same size needs a version: %d", len(vs))
	}
}

func TestSameContent(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o644)
		return p
	}
	big := bytes.Repeat([]byte("0123456789abcdef"), 40000) // spans several read buffers
	other := append([]byte(nil), big...)
	other[len(other)-1] = 'X'
	a, b, c := mk("a", big), mk("b", big), mk("c", other)
	if !sameContent(a, b) || sameContent(a, c) || sameContent(a, mk("short", big[:100])) {
		t.Fatal("sameContent compares every byte and the length")
	}
	if !sameContent(mk("e1", nil), mk("e2", nil)) || sameContent(a, filepath.Join(dir, "missing")) {
		t.Fatal("two empty files are the same; a missing file is not the same as anything")
	}
}

func TestChangeTimeSeesAFileCopiedWithAnOldDate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "old.md")
	os.WriteFile(p, []byte("x"), 0o644)
	old := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(p, old, old)
	info, _ := os.Stat(p)
	if time.Since(ChangeTime(info)) > time.Minute {
		t.Fatalf("a file that just arrived with an old date counts as changed now: %v", ChangeTime(info))
	}
}

func TestPruneRemovesEmptyHistoryFolders(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "a/b/gone.md", "x")
	write(t, s, c, "a/kept.md", "y")
	mustRecord(t, s, "a/b/gone.md", true)
	mustRecord(t, s, "a/kept.md", true)
	os.Remove(filepath.Join(s.Root, "a/b/gone.md"))
	s.MarkDeleted("a/b/gone.md")
	c.advance(day(31))
	res, err := s.Prune()
	if err != nil || res.Versions != 1 {
		t.Fatalf("prune: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(s.historyRoot(), "a", "b")); !os.IsNotExist(err) {
		t.Fatal("the emptied history folders should be removed")
	}
	if vs, _ := s.List("a/kept.md"); len(vs) != 1 {
		t.Fatal("the newest version of an existing file is always kept")
	}
}

func TestCatchUp(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "a.md", "a")
	write(t, s, c, "sub/b.md", "b")
	write(t, s, c, "sub/c.swp", "tmp")
	n, err := s.CatchUp(time.Time{})
	if err != nil || n != 2 {
		t.Fatalf("first catch-up recorded %d (%v), want 2", n, err)
	}
	last := c.t
	c.advance(time.Hour)
	write(t, s, c, "a.md", "a2")
	n, _ = s.CatchUp(last)
	if n != 1 {
		t.Fatalf("second catch-up recorded %d, want 1", n)
	}
	if vs, _ := s.List("a.md"); len(vs) != 2 {
		t.Fatalf("a.md should have 2 versions")
	}
}

func TestJournalAndLog(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "a.md", "a")
	mustRecord(t, s, "a.md", true)
	c.advance(time.Minute)
	os.Remove(filepath.Join(s.Root, "a.md"))
	s.MarkDeleted("a.md")
	if err := s.Journal.Flush(); err != nil {
		t.Fatal(err)
	}
	// A second machine writes its own journal file.
	other := New(s.Root, "remote-pc", jfs.Copy{})
	other.Now = func() time.Time { return c.t.Add(time.Minute) }
	write(t, s, c, "b.md", "b")
	if _, err := other.Record("b.md"); err != nil {
		t.Fatal(err)
	}
	other.Journal.Flush()

	evs, err := ReadJournals(s.Root, c.t.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 {
		t.Fatalf("want 3 events, got %d: %+v", len(evs), evs)
	}
	if evs[0].Machine != "remote-pc" || evs[0].Op != "save" || evs[1].Op != "delete" || evs[2].Op != "save" {
		t.Fatalf("events not newest first: %+v", evs)
	}
	if got := OtherMachines(s.Root, s.Machine); len(got) != 1 || got[0] != "remote-pc" {
		t.Fatalf("OtherMachines = %v", got)
	}
}

func TestReadJournalFileFollowsGrowth(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "a.md", "a")
	mustRecord(t, s, "a.md", true)
	s.Journal.Flush()
	path := JournalFile(s.Root, s.Machine, c.t)
	evs, off, err := ReadJournalFile(path, 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("first read: %d events, %v", len(evs), err)
	}
	c.advance(time.Minute)
	write(t, s, c, "a.md", "aa")
	mustRecord(t, s, "a.md", true)
	s.Journal.Flush()
	evs, off2, _ := ReadJournalFile(path, off)
	if len(evs) != 1 || off2 <= off {
		t.Fatalf("second read should return only the new event, got %d", len(evs))
	}
}

func TestUnversionedFilesAreLoggedAndWalkedButNotRecorded(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "media/reel.mp4", "video bytes")
	write(t, s, c, "media/notes.md", "notes")
	mustRecord(t, s, "media/reel.mp4", false)
	if !s.NoteSave("media/reel.mp4") || s.NoteSave("media/notes.md") || s.NoteSave("media/.x.swp") {
		t.Fatal("NoteSave should be true for unversioned content only")
	}
	if _, err := os.Stat(s.HistDir("media/reel.mp4")); !os.IsNotExist(err) {
		t.Fatal("a video must not get a history folder")
	}
	var walked []string
	s.WalkFiles(func(rel string, _ os.FileInfo) error { walked = append(walked, rel); return nil })
	if len(walked) != 2 {
		t.Fatalf("the walk must include videos so they can be cached: %v", walked)
	}
	os.Remove(filepath.Join(s.Root, "media/reel.mp4"))
	c.advance(time.Minute)
	if err := s.MarkDeleted("media/reel.mp4"); err != nil {
		t.Fatal(err)
	}
	s.Journal.Flush()
	evs, _ := ReadJournals(s.Root, c.t.Add(-time.Hour))
	if len(evs) != 2 || evs[0].Op != "delete" || evs[1].Op != "save" || evs[1].Version != "" || evs[1].Size != 11 {
		t.Fatalf("the log should show the save and the deletion of the video: %+v", evs)
	}
}

func TestLoadSkipFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "skip")
	os.WriteFile(p, []byte("# comment\n*.bak\nbuild/\n!*.lck\n!.lorefs/\n"), 0o644)
	s, err := LoadSkip(p)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Path("x.bak") || !s.Path("build/out.bin") {
		t.Fatal("added patterns should be skipped")
	}
	if s.Path("db.lck") {
		t.Fatal("a removed default should no longer be skipped")
	}
	if !s.Path(".lorefs/history/x") {
		t.Fatal("the drive's bookkeeping can never be un-skipped")
	}
	if s.Path("notes/plan.md") {
		t.Fatal("ordinary files are not skipped")
	}
}

func TestVideoFilesAreSkippedByDefault(t *testing.T) {
	s := DefaultSkip()
	for _, name := range []string{"reel.mp4", "REEL.MP4", "shoot/take 2.MOV", "a/b/clip.webm"} {
		if !s.Path(name) {
			t.Errorf("%s should be skipped", name)
		}
	}
	if !s.Media("REEL.MP4") || s.Media("notes.md") || s.Media("x.swp") {
		t.Error("Media() should be true for video files only")
	}
	for _, name := range []string{"poster.png", "brief.docx", "notes.md", "mp4", "video.mp4.md"} {
		if s.Path(name) {
			t.Errorf("%s should not be skipped", name)
		}
	}
	// Videos are ordinary content, not ignored: they are still walked,
	// logged and cached; they only get no versions.
	if s.Ignored("media/reel.mp4") || !s.Ignored("media/.reel.mp4.swp") || !s.Ignored("x/node_modules/a.mp4") {
		t.Error("Ignored() is for temporary files and skipped folders only")
	}
	// A machine can opt back in.
	p := filepath.Join(t.TempDir(), "skip")
	os.WriteFile(p, []byte("!*.mp4\n"), 0o644)
	custom, _ := LoadSkip(p)
	if custom.Path("reel.mp4") || !custom.Path("reel.mov") {
		t.Error("removing *.mp4 from the list should record mp4 but still skip mov")
	}
}

// A folder that cannot be read must not make its files look deleted.
func TestPruneStopsWhenAFolderCannotBeRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every folder")
	}
	s, c := newStore(t)
	write(t, s, c, "team/plan.md", "v1")
	mustRecord(t, s, "team/plan.md", true)
	locked := filepath.Join(s.Root, "team")
	os.Chmod(locked, 0o000)
	defer os.Chmod(locked, 0o755)
	c.advance(day(40))
	res, err := s.Prune()
	if err == nil || res.Adopted != 0 {
		t.Fatalf("prune went on with an unreadable folder: err %v, adopted %d", err, res.Adopted)
	}
	os.Chmod(locked, 0o755)
	if _, ok := s.DeletedAt("team/plan.md"); ok {
		t.Fatal("a file in an unreadable folder was noted as deleted")
	}
}

func TestPruneStopsWhenAsked(t *testing.T) {
	s, c := newStore(t)
	write(t, s, c, "x.md", "v1")
	mustRecord(t, s, "x.md", true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.PruneContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled prune should say so, got %v", err)
	}
}
