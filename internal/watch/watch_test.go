package watch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
)

// rec collects handler calls.
type rec struct {
	mu     sync.Mutex
	events []string
}

func (r *rec) add(s string) { r.mu.Lock(); r.events = append(r.events, s); r.mu.Unlock() }

func (r *rec) Save(rel string)    { r.add("save " + rel) }
func (r *rec) Delete(rel string)  { r.add("delete " + rel) }
func (r *rec) Rename(a, b string) { r.add("rename " + a + " -> " + b) }
func (r *rec) Overflow()          { r.add("overflow") }
func (r *rec) NewDir(rel string)  { r.add("newdir " + rel) }
func (r *rec) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}
func (r *rec) count(s string) int {
	n := 0
	for _, e := range r.snapshot() {
		if e == s {
			n++
		}
	}
	return n
}

const (
	quiet   = 60 * time.Millisecond
	maxWait = 400 * time.Millisecond
)

func start(t *testing.T) (string, *rec, *Watcher) {
	t.Helper()
	root := t.TempDir()
	r := &rec{}
	w := New(root, history.DefaultSkip(), r, Options{Quiet: quiet, MaxWait: maxWait})
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, ready) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("watcher did not stop")
		}
	})
	return root, r, w
}

// settle waits long enough for grouped saves to be reported.
func settle() { time.Sleep(quiet*4 + movePairWindow) }

func wantEvents(t *testing.T, r *rec, want ...string) {
	t.Helper()
	var got []string
	for _, e := range r.snapshot() {
		if !strings.HasPrefix(e, "newdir ") { // covered by its own test
			got = append(got, e)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n  got  %q\n  want %q", got, want)
	}
}

func TestSaveInPlace(t *testing.T) {
	root, r, _ := start(t)
	os.WriteFile(filepath.Join(root, "a.md"), []byte("one"), 0o644)
	settle()
	wantEvents(t, r, "save a.md")
}

func TestSaveByReplace(t *testing.T) {
	root, r, _ := start(t)
	os.WriteFile(filepath.Join(root, "a.md"), []byte("one"), 0o644)
	settle()
	// Write a temporary file and rename it over the original.
	os.WriteFile(filepath.Join(root, ".a.md.tmp"), []byte("two"), 0o644)
	os.Rename(filepath.Join(root, ".a.md.tmp"), filepath.Join(root, "a.md"))
	settle()
	wantEvents(t, r, "save a.md", "save a.md")
}

func TestEditorBackupDance(t *testing.T) {
	root, r, _ := start(t)
	p := filepath.Join(root, "a.md")
	os.WriteFile(p, []byte("one"), 0o644)
	settle()
	// Move the original to a backup name, write the new file, drop the backup.
	os.Rename(p, p+"~")
	os.WriteFile(p, []byte("two"), 0o644)
	os.Remove(p + "~")
	settle()
	wantEvents(t, r, "save a.md", "save a.md")
}

func TestBurstIsGrouped(t *testing.T) {
	root, r, _ := start(t)
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(root, "a.md"), []byte{byte('0' + i)}, 0o644)
		time.Sleep(5 * time.Millisecond)
	}
	settle()
	if n := r.count("save a.md"); n != 1 {
		t.Fatalf("5 rapid saves reported %d times, want 1", n)
	}
}

func TestContinuousWritingStillReports(t *testing.T) {
	root, r, _ := start(t)
	f, err := os.Create(filepath.Join(root, "log.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// Keep the file open and keep writing for about three max-wait periods.
	deadline := time.Now().Add(maxWait*3 + maxWait/2)
	for time.Now().Before(deadline) {
		f.WriteString("line\n")
		time.Sleep(20 * time.Millisecond)
	}
	if n := r.count("save log.txt"); n < 2 {
		t.Fatalf("a file written continuously was reported %d times while open, want at least 2", n)
	}
	f.Close()
}

func TestSkippedFilesAreSilent(t *testing.T) {
	root, r, _ := start(t)
	os.WriteFile(filepath.Join(root, ".a.md.swp"), []byte("x"), 0o644)
	os.Remove(filepath.Join(root, ".a.md.swp"))
	os.MkdirAll(filepath.Join(root, ".lorefs", "history"), 0o755)
	os.WriteFile(filepath.Join(root, ".lorefs", "history", "v"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(root, "node_modules", "pkg", "i.js"), []byte("x"), 0o644)
	settle()
	wantEvents(t, r)
}

func TestNewFolderWithFiles(t *testing.T) {
	root, r, _ := start(t)
	// Create a folder and write into it at once, before a watch can exist.
	os.MkdirAll(filepath.Join(root, "new", "deep"), 0o755)
	os.WriteFile(filepath.Join(root, "new", "deep", "x.md"), []byte("x"), 0o644)
	settle()
	if n := r.count("save new/deep/x.md"); n != 1 {
		t.Fatalf("file in a brand-new folder reported %d times: %q", n, r.snapshot())
	}
	// And later writes in that folder are seen too.
	os.WriteFile(filepath.Join(root, "new", "deep", "y.md"), []byte("y"), 0o644)
	settle()
	if n := r.count("save new/deep/y.md"); n != 1 {
		t.Fatalf("later file reported %d times: %q", n, r.snapshot())
	}
}

func TestRenameFile(t *testing.T) {
	root, r, _ := start(t)
	os.WriteFile(filepath.Join(root, "a.md"), []byte("one"), 0o644)
	settle()
	os.Rename(filepath.Join(root, "a.md"), filepath.Join(root, "b.md"))
	settle()
	wantEvents(t, r, "save a.md", "rename a.md -> b.md", "save b.md")
}

func TestMoveFolderThenWriteInside(t *testing.T) {
	root, r, w := start(t)
	os.MkdirAll(filepath.Join(root, "a", "sub"), 0o755)
	os.MkdirAll(filepath.Join(root, "b"), 0o755)
	settle()
	before := w.Watched()
	os.Rename(filepath.Join(root, "a"), filepath.Join(root, "b", "moved"))
	settle()
	if w.Watched() != before {
		t.Fatalf("watch count changed from %d to %d after a move", before, w.Watched())
	}
	os.WriteFile(filepath.Join(root, "b", "moved", "sub", "x.md"), []byte("x"), 0o644)
	settle()
	wantEvents(t, r, "rename a -> b/moved", "save b/moved/sub/x.md")
}

func TestDelete(t *testing.T) {
	root, r, _ := start(t)
	os.WriteFile(filepath.Join(root, "a.md"), []byte("one"), 0o644)
	settle()
	os.Remove(filepath.Join(root, "a.md"))
	settle()
	wantEvents(t, r, "save a.md", "delete a.md")
}

func TestMovedOutOfDriveIsADelete(t *testing.T) {
	root, r, _ := start(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.md"), []byte("one"), 0o644)
	settle()
	os.Rename(filepath.Join(root, "a.md"), filepath.Join(outside, "a.md"))
	settle()
	wantEvents(t, r, "save a.md", "delete a.md")
}

func TestMovedIntoDriveIsASave(t *testing.T) {
	root, r, _ := start(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "a.md"), []byte("one"), 0o644)
	os.Rename(filepath.Join(outside, "a.md"), filepath.Join(root, "a.md"))
	settle()
	wantEvents(t, r, "save a.md")
}

// A folder created on another machine produces no local event. Simulate
// that by dropping the watch a local mkdir just caused.
func forgetFolder(w *Watcher, dir string) {
	w.mu.Lock()
	w.dropTree(dir)
	w.mu.Unlock()
}

func TestFolderCreatedElsewhereIsWatchedOnceReported(t *testing.T) {
	root, r, w := start(t)
	dir := filepath.Join(root, "project-x")
	os.MkdirAll(filepath.Join(dir, "deep"), 0o755)
	settle()
	forgetFolder(w, dir)

	// Unwatched: a save here goes unnoticed, as it would in a folder the
	// other machine created.
	os.WriteFile(filepath.Join(dir, "early.md"), []byte("saved before the watch"), 0o644)
	settle()
	if r.count("save project-x/early.md") != 0 {
		t.Fatal("test setup: the folder should have been unwatched")
	}
	if w.Has(dir) {
		t.Fatal("test setup: Has should say the folder is unwatched")
	}

	// The helper learns of a file saved deep inside it from the other
	// machine's journal; the folders above are picked up with it.
	n, files := w.Ensure(filepath.Join(dir, "deep"))
	if n != 2 {
		t.Fatalf("Ensure should start watching the folder and the one above it, got %d", n)
	}
	if len(files) != 1 || files[0] != filepath.Join(dir, "early.md") {
		t.Fatalf("the file saved before the watch should be handed back: %q", files)
	}
	settle()
	if r.count("save project-x/early.md") != 0 {
		t.Fatalf("Ensure must not report saves itself; the file may be another machine's: %q", r.snapshot())
	}
	os.WriteFile(filepath.Join(dir, "deep", "later.md"), []byte("x"), 0o644)
	settle()
	if r.count("save project-x/deep/later.md") != 1 {
		t.Fatalf("saves after Ensure should be seen: %q", r.snapshot())
	}
	// Reporting it again changes nothing and re-checks nothing.
	before := len(r.snapshot())
	if n, files := w.Ensure(dir); n != 0 || len(files) != 0 {
		t.Fatalf("second Ensure should add nothing, got %d %q", n, files)
	}
	settle()
	if len(r.snapshot()) != before {
		t.Fatalf("second Ensure should cause no events: %q", r.snapshot())
	}
	// Skipped, outside and missing folders are refused.
	os.MkdirAll(filepath.Join(root, ".lorefs", "history"), 0o755)
	for _, bad := range []string{filepath.Join(root, ".lorefs", "history"), t.TempDir(), filepath.Join(root, "missing")} {
		if n, _ := w.Ensure(bad); n != 0 {
			t.Fatalf("Ensure must ignore skipped, outside and missing folders: %s", bad)
		}
	}
}

func TestFolderReplacedElsewhereIsWatchedAgain(t *testing.T) {
	root, r, w := start(t)
	dir := filepath.Join(root, "plans")
	os.MkdirAll(dir, 0o755)
	settle()
	// Replace the folder by a new one of the same name while keeping the
	// watcher's memory of the old one, as happens when the other machine
	// deletes and recreates it.
	w.mu.Lock()
	stale := w.paths[dir]
	w.mu.Unlock()
	os.Remove(dir)
	os.MkdirAll(dir, 0o755)
	settle()
	w.mu.Lock()
	w.dropTree(dir)
	w.paths[dir] = stale // believed watched, but that folder is gone
	w.mu.Unlock()

	w.Ensure(root)
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("x"), 0o644)
	settle()
	if r.count("save plans/a.md") != 1 {
		t.Fatalf("a save in the recreated folder should be seen: %q", r.snapshot())
	}
}

func TestFolderRenamedElsewhere(t *testing.T) {
	root, r, w := start(t)
	os.MkdirAll(filepath.Join(root, "old-name"), 0o755)
	settle()
	// The rename happens "elsewhere": do it locally, then put the
	// watcher's memory back to how it would be without the local event.
	os.Rename(filepath.Join(root, "old-name"), filepath.Join(root, "new-name"))
	settle()
	w.mu.Lock()
	w.moveTree(filepath.Join(root, "new-name"), filepath.Join(root, "old-name"))
	w.mu.Unlock()

	w.Moved(filepath.Join(root, "old-name"), filepath.Join(root, "new-name"))
	os.WriteFile(filepath.Join(root, "new-name", "x.md"), []byte("x"), 0o644)
	settle()
	if r.count("save new-name/x.md") != 1 || r.count("save old-name/x.md") != 0 {
		t.Fatalf("a save in the renamed folder should be reported under the new name: %q", r.snapshot())
	}

	// Even without being told, a full check heals the remembered name.
	os.Rename(filepath.Join(root, "new-name"), filepath.Join(root, "third-name"))
	settle()
	w.mu.Lock()
	w.moveTree(filepath.Join(root, "third-name"), filepath.Join(root, "new-name"))
	w.mu.Unlock()
	w.Ensure(root)
	os.WriteFile(filepath.Join(root, "third-name", "y.md"), []byte("y"), 0o644)
	settle()
	if r.count("save third-name/y.md") != 1 {
		t.Fatalf("a full check should pick up the new name: %q", r.snapshot())
	}
	// Paths outside the drive are refused.
	w.Moved(filepath.Join(root, "third-name"), t.TempDir())
	if !w.Has(filepath.Join(root, "third-name")) {
		t.Fatal("Moved must refuse a target outside the drive")
	}
}

func TestNewFolderIsAnnounced(t *testing.T) {
	root, r, _ := start(t)
	os.MkdirAll(filepath.Join(root, "made-here"), 0o755)
	settle()
	if r.count("newdir made-here") != 1 {
		t.Fatalf("a folder created locally should be announced: %q", r.snapshot())
	}
}

// slow is a handler whose Save takes a while, to catch two workers
// handling one file side by side.
type slow struct {
	rec
	mu       sync.Mutex
	inFlight map[string]int
	overlap  bool
	calls    int
}

func (s *slow) Save(rel string) {
	s.mu.Lock()
	s.inFlight[rel]++
	if s.inFlight[rel] > 1 {
		s.overlap = true
	}
	s.calls++
	s.mu.Unlock()
	time.Sleep(4 * quiet)
	s.mu.Lock()
	s.inFlight[rel]--
	s.mu.Unlock()
}

// slowDelete is a handler whose Delete takes a while, as noting a
// deletion does on the real drive.
type slowDelete struct{ rec }

func (s *slowDelete) Delete(rel string) {
	time.Sleep(300 * time.Millisecond)
	s.rec.Delete(rel)
}

func TestDeletionsDoNotHoldUpSaves(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 16; i++ {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("old%02d.md", i)), []byte("x"), 0o644)
	}
	h := &slowDelete{}
	w := New(root, history.DefaultSkip(), h, Options{Quiet: quiet, MaxWait: maxWait, Workers: 8})
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, ready) }()
	<-ready
	defer func() { cancel(); <-done }()

	for i := 0; i < 16; i++ {
		os.Remove(filepath.Join(root, fmt.Sprintf("old%02d.md", i)))
	}
	os.WriteFile(filepath.Join(root, "new.md"), []byte("saved right after"), 0o644)
	// One after another the 16 deletions take 4.8 s. Side by side they
	// take 0.6 s, and the save is reported right behind them.
	deadline := time.Now().Add(2 * time.Second)
	for h.count("save new.md") == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("a save made after 16 deletions was not reported within 2 s: %q", h.snapshot())
		}
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(3 * time.Second)
	for len(h.snapshot()) < 17 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	n := 0
	for _, e := range h.snapshot() {
		if strings.HasPrefix(e, "delete old") {
			n++
		}
	}
	if n != 16 {
		t.Fatalf("16 deletions, %d reported: %q", n, h.snapshot())
	}
}

func TestOneFileIsNeverHandledTwiceAtOnce(t *testing.T) {
	root := t.TempDir()
	h := &slow{inFlight: map[string]int{}}
	w := New(root, history.DefaultSkip(), h, Options{Quiet: quiet, MaxWait: maxWait, Workers: 8})
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, ready) }()
	<-ready
	p := filepath.Join(root, "a.md")
	// Saves spaced so that each one settles while the one before is still
	// being handled.
	for i := 0; i < 6; i++ {
		os.WriteFile(p, []byte{byte('0' + i)}, 0o644)
		time.Sleep(2 * quiet)
	}
	time.Sleep(16 * quiet)
	cancel()
	<-done
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.overlap {
		t.Fatal("the same file was handled by two workers at once")
	}
	if h.calls < 2 {
		t.Fatalf("a save that arrives while the file is being handled must still be handled afterwards: %d calls", h.calls)
	}
}
