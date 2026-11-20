package record

import (
	"context"
	"errors"
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

func (r *rec) Save(rel string)       { r.add("save " + rel) }
func (r *rec) Delete(rel string)     { r.add("delete " + rel) }
func (r *rec) Rename(a, b string)    { r.add("rename " + a + " -> " + b) }
func (r *rec) CatchUp(reason string) { r.add("catchup") }
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

// fakeSource stands in for `juicefs changelog`.
type fakeSource struct {
	mu    sync.Mutex
	froms []int64
	lines chan string
	fail  chan struct{}
}

func newFakeSource() *fakeSource {
	return &fakeSource{lines: make(chan string, 1024), fail: make(chan struct{}, 4)}
}

func (s *fakeSource) Follow(ctx context.Context, from int64, emit func(string)) error {
	s.mu.Lock()
	s.froms = append(s.froms, from)
	s.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return nil
		case l := <-s.lines:
			emit(l)
		case <-s.fail:
			return errors.New("the program died")
		}
	}
}

func (s *fakeSource) fromList() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.froms...)
}

const (
	quiet   = 60 * time.Millisecond
	long    = 500 * time.Millisecond
	maxWait = 400 * time.Millisecond
)

type testRig struct {
	t       *testing.T
	root    string
	h       *rec
	src     *fakeSource
	r       *Recorder
	ver     int64
	session int64
	sessMu  sync.Mutex
	cancel  context.CancelFunc
	done    chan error
	state   string
}

func (g *testRig) setSession(n int64) { g.sessMu.Lock(); g.session = n; g.sessMu.Unlock() }

func newRig(t *testing.T, h Handler, mut func(*Options)) *testRig {
	t.Helper()
	g := &testRig{t: t, root: t.TempDir(), src: newFakeSource(), session: me, done: make(chan error, 1)}
	g.state = filepath.Join(t.TempDir(), "changelog.json")
	if rr, ok := h.(*rec); ok {
		g.h = rr
	}
	if h == nil {
		g.h = &rec{}
		h = g.h
	}
	o := Options{
		Folder: g.root, Source: g.src, Skip: history.DefaultSkip(), Handler: h,
		Session:   func() (int64, error) { g.sessMu.Lock(); defer g.sessMu.Unlock(); return g.session, nil },
		Info:      func(int64) string { return "" },
		Group:     GroupOptions{Quiet: quiet, LongQuiet: long, MaxWait: maxWait, Workers: 8},
		StateFile: g.state, PositionEvery: 20 * time.Millisecond, SessionEvery: 30 * time.Millisecond,
		RetryMin: 10 * time.Millisecond, RetryMax: 40 * time.Millisecond,
	}
	if mut != nil {
		mut(&o)
	}
	g.r = New(o)
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	ready := make(chan struct{})
	go func() { g.done <- g.r.Run(ctx, ready) }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the recorder did not start")
	}
	t.Cleanup(func() { g.stop() })
	return g
}

func (g *testRig) stop() {
	g.cancel()
	select {
	case <-g.done:
	case <-time.After(5 * time.Second):
		g.t.Error("the recorder did not stop")
	}
	g.done <- nil // so that a second stop does not wait
}

// send makes the next log entry.
func (g *testRig) send(session int64, op string) {
	g.ver++
	g.src.lines <- fmt.Sprintf("%d: 1795175136.%09d|%s|(%d,%d)", g.ver, g.ver, op, session, g.ver)
}

func (g *testRig) own(op string) { g.send(me, op) }

func (g *testRig) write(rel, content string) {
	p := filepath.Join(g.root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		g.t.Fatal(err)
	}
}

func (g *testRig) settle() { time.Sleep(quiet*5 + 50*time.Millisecond) }

func wantEvents(t *testing.T, r *rec, want ...string) {
	t.Helper()
	got := r.snapshot()
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n  got  %q\n  want %q", got, want)
	}
}

func TestSaveInPlace(t *testing.T) {
	g := newRig(t, nil, nil)
	g.write("a.md", "one")
	g.own("CREATE(1,a.md,1000,1000,1,420,18,,linux,true):11")
	g.own("WRITE(11,0,0,1,3,1795175136,1):1")
	g.settle()
	wantEvents(t, g.h, "save a.md")
}

func TestBurstIsGrouped(t *testing.T) {
	g := newRig(t, nil, nil)
	g.write("a.md", "x")
	g.own("CREATE(1,a.md,1000,1000,1,420,18,,linux,true):11")
	for i := 0; i < 5; i++ {
		g.own("WRITE(11,0,0,1,1,1795175136,1):1")
		time.Sleep(5 * time.Millisecond)
	}
	g.settle()
	if n := g.h.count("save a.md"); n != 1 {
		t.Fatalf("5 rapid saves reported %d times, want 1", n)
	}
}

func TestContinuousWritingStillReports(t *testing.T) {
	g := newRig(t, nil, nil)
	g.write("log.txt", "x")
	g.own("CREATE(1,log.txt,1000,1000,1,420,18,,linux,true):11")
	deadline := time.Now().Add(maxWait*3 + maxWait/2)
	for time.Now().Before(deadline) {
		g.own("WRITE(11,0,0,1,5,1795175136,1):1")
		time.Sleep(20 * time.Millisecond)
	}
	if n := g.h.count("save log.txt"); n < 2 {
		t.Fatalf("a file written continuously was reported %d times while open, want at least 2", n)
	}
}

func TestFullSliceWaitsLongerForTheWriterToFinish(t *testing.T) {
	g := newRig(t, nil, func(o *Options) { o.Group.MaxWait = time.Minute })
	g.write("big.bin", "x")
	g.own("CREATE(1,big.bin,1000,1000,1,420,18,,linux,true):11")
	g.own(fmt.Sprintf("WRITE(11,0,0,1,%d,1795175136,1):1", 64<<20))
	time.Sleep(long / 2) // well past the ordinary quiet time
	if n := g.h.count("save big.bin"); n != 0 {
		t.Fatalf("a file whose last write filled a slice was recorded after %s", long/2)
	}
	g.own(fmt.Sprintf("WRITE(11,1,0,2,%d,1795175136,1):1", 64<<20)) // the writer goes on
	time.Sleep(long / 2)
	if n := g.h.count("save big.bin"); n != 0 {
		t.Fatal("the long wait must start again with each full slice")
	}
	g.own("WRITE(11,2,0,3,100,1795175136,1):1") // the last, small slice
	g.settle()
	if n := g.h.count("save big.bin"); n != 1 {
		t.Fatalf("once the writer is done the file is recorded once, got %d", n)
	}
}

func TestOnlyOwnEntriesAreRecorded(t *testing.T) {
	g := newRig(t, nil, nil)
	g.write("theirs.md", "x")
	g.send(other, "CREATE(1,theirs.md,1000,1000,1,420,18,,linux,true):11")
	g.send(other, "WRITE(11,0,0,1,3,1795175136,1):1")
	g.settle()
	wantEvents(t, g.h)
}

func TestSkippedFilesAreSilent(t *testing.T) {
	g := newRig(t, nil, nil)
	g.write(".a.md.swp", "x")
	g.own("CREATE(1,.a.md.swp,1000,1000,1,420,18,,linux,true):11")
	g.own("UNLINK(1,.a.md.swp,0):11")
	g.own("CREATE(1,.lorefs,1000,1000,2,493,18,,linux,true):12")
	g.own("CREATE(12,v,1000,1000,1,420,18,,linux,true):13")
	g.own("WRITE(13,0,0,1,3,1795175136,1):1")
	g.own("CREATE(1,node_modules,1000,1000,2,493,18,,linux,true):14")
	g.own("CREATE(14,i.js,1000,1000,1,420,18,,linux,true):15")
	g.own("WRITE(15,0,0,1,3,1795175136,1):1")
	g.settle()
	wantEvents(t, g.h)
}

func TestSaveByReplace(t *testing.T) {
	g := newRig(t, nil, nil)
	g.write("a.md", "one")
	g.own("CREATE(1,a.md,1000,1000,1,420,18,,linux,true):11")
	g.own("WRITE(11,0,0,1,3,1795175136,1):1")
	g.settle()
	g.write(".a.md.tmp", "two")
	g.own("CREATE(1,.a.md.tmp,1000,1000,1,420,18,,linux,true):12")
	g.own("WRITE(12,0,0,2,3,1795175137,1):1")
	os.Rename(filepath.Join(g.root, ".a.md.tmp"), filepath.Join(g.root, "a.md"))
	g.own("MOVE(1,.a.md.tmp,1,a.md,0,11,0):12")
	g.settle()
	wantEvents(t, g.h, "save a.md", "save a.md")
}

func TestEditorBackupDance(t *testing.T) {
	g := newRig(t, nil, nil)
	g.write("a.md", "one")
	g.own("CREATE(1,a.md,1000,1000,1,420,18,,linux,true):11")
	g.own("WRITE(11,0,0,1,3,1795175136,1):1")
	g.settle()
	// The original moves to a backup name, a new file is written, the backup goes.
	os.Rename(filepath.Join(g.root, "a.md"), filepath.Join(g.root, "a.md~"))
	g.own("MOVE(1,a.md,1,a.md%7E,0,0,0):11")
	g.write("a.md", "two")
	g.own("CREATE(1,a.md,1000,1000,1,420,18,,linux,true):12")
	g.own("WRITE(12,0,0,2,3,1795175137,1):1")
	os.Remove(filepath.Join(g.root, "a.md~"))
	g.own("UNLINK(1,a.md~,0):11")
	g.settle()
	wantEvents(t, g.h, "save a.md", "save a.md")
}

func TestRenameFile(t *testing.T) {
	g := newRig(t, nil, nil)
	g.write("a.md", "one")
	g.own("CREATE(1,a.md,1000,1000,1,420,18,,linux,true):11")
	g.own("WRITE(11,0,0,1,3,1795175136,1):1")
	g.settle()
	os.Rename(filepath.Join(g.root, "a.md"), filepath.Join(g.root, "b.md"))
	g.own("MOVE(1,a.md,1,b.md,0,0,0):11")
	g.settle()
	wantEvents(t, g.h, "save a.md", "rename a.md -> b.md", "save b.md")
}

func TestMoveFolderThenWriteInside(t *testing.T) {
	g := newRig(t, nil, nil)
	g.own("CREATE(1,a,1000,1000,2,493,18,,linux,true):10")
	g.own("CREATE(10,sub,1000,1000,2,493,18,,linux,true):12")
	g.own("CREATE(1,b,1000,1000,2,493,18,,linux,true):13")
	os.MkdirAll(filepath.Join(g.root, "a", "sub"), 0o755)
	os.MkdirAll(filepath.Join(g.root, "b"), 0o755)
	os.Rename(filepath.Join(g.root, "a"), filepath.Join(g.root, "b", "moved"))
	g.own("MOVE(1,a,13,moved,0,0,0):10")
	g.write("b/moved/sub/x.md", "x")
	g.own("CREATE(12,x.md,1000,1000,1,420,18,,linux,true):14")
	g.own("WRITE(14,0,0,1,1,1795175136,1):1")
	g.settle()
	wantEvents(t, g.h, "rename a -> b/moved", "save b/moved/sub/x.md")
}

func TestDelete(t *testing.T) {
	g := newRig(t, nil, nil)
	g.write("a.md", "one")
	g.own("CREATE(1,a.md,1000,1000,1,420,18,,linux,true):11")
	g.own("WRITE(11,0,0,1,3,1795175136,1):1")
	g.settle()
	os.Remove(filepath.Join(g.root, "a.md"))
	g.own("UNLINK(1,a.md,0):11")
	g.settle()
	wantEvents(t, g.h, "save a.md", "delete a.md")
}

func TestMovedIntoTheTrashIsReportedAsADelete(t *testing.T) {
	g := newRig(t, nil, nil)
	g.write("a.md", "one")
	g.own("CREATE(1,a.md,1000,1000,1,420,18,,linux,true):11")
	g.own("WRITE(11,0,0,1,3,1795175136,1):1")
	g.settle()
	os.Remove(filepath.Join(g.root, "a.md"))
	g.own("MOVE(1,a.md,3,gone,0,0,0):11")
	g.settle()
	wantEvents(t, g.h, "save a.md", "delete a.md")
}

func TestAFolderIsNeverSaved(t *testing.T) {
	g := newRig(t, nil, nil)
	os.MkdirAll(filepath.Join(g.root, "d"), 0o755)
	g.own("CREATE(1,d,1000,1000,2,493,18,,linux,true):10")
	g.own("MOVE(1,d,1,e,0,0,0):10")
	os.Rename(filepath.Join(g.root, "d"), filepath.Join(g.root, "e"))
	g.settle()
	wantEvents(t, g.h, "rename d -> e")
}

// slowDelete makes deletions slow, to show they do not hold up saves.
type slowDelete struct{ rec }

func (s *slowDelete) Delete(rel string) { time.Sleep(300 * time.Millisecond); s.rec.Delete(rel) }

func TestDeletionsDoNotHoldUpSaves(t *testing.T) {
	h := &slowDelete{}
	g := newRig(t, h, nil)
	g.h = &h.rec
	for i := 0; i < 16; i++ {
		g.own(fmt.Sprintf("CREATE(1,old%02d.md,1000,1000,1,420,18,,linux,true):%d", i, 100+i))
	}
	time.Sleep(10 * time.Millisecond)
	for i := 0; i < 16; i++ {
		g.own(fmt.Sprintf("UNLINK(1,old%02d.md,0):%d", i, 100+i))
	}
	g.write("new.md", "saved right after")
	g.own("CREATE(1,new.md,1000,1000,1,420,18,,linux,true):200")
	g.own("WRITE(200,0,0,1,3,1795175136,1):1")
	deadline := time.Now().Add(2 * time.Second)
	for h.rec.count("save new.md") == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("a save made after 16 deletions was not reported within 2 s: %q", h.rec.snapshot())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// slow records how many workers are inside Save for one file at once.
type slow struct {
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
func (s *slow) Delete(string)      {}
func (s *slow) Rename(a, b string) {}
func (s *slow) CatchUp(string)     {}

func TestOneFileIsNeverHandledTwiceAtOnce(t *testing.T) {
	h := &slow{inFlight: map[string]int{}}
	g := newRig(t, h, nil)
	g.write("a.md", "x")
	g.own("CREATE(1,a.md,1000,1000,1,420,18,,linux,true):11")
	for i := 0; i < 6; i++ {
		g.own("WRITE(11,0,0,1,1,1795175136,1):1")
		time.Sleep(2 * quiet)
	}
	time.Sleep(16 * quiet)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.overlap {
		t.Fatal("the same file was handled by two workers at once")
	}
	if h.calls < 2 {
		t.Fatalf("a save that arrives while the file is being handled must still be handled afterwards: %d calls", h.calls)
	}
}

func TestPositionIsHeldBackWhileAChangeWaits(t *testing.T) {
	g := newRig(t, nil, func(o *Options) {
		o.Group.Quiet = 300 * time.Millisecond
		o.Group.LongQuiet = 300 * time.Millisecond
		o.Group.MaxWait = time.Minute
	})
	g.write("a.md", "x")
	g.send(other, "CREATE(1,b.md,1000,1000,1,420,18,,linux,true):50") // v1, another computer
	g.own("CREATE(1,a.md,1000,1000,1,420,18,,linux,true):11")         // v2: waits
	g.send(other, "CREATE(1,c.md,1000,1000,1,420,18,,linux,true):51") // v3
	time.Sleep(100 * time.Millisecond)
	if v, p := g.r.Version(), g.r.Position(); v != 3 || p != 1 {
		t.Fatalf("read %d, position %d: the position must stay before the change that has not been recorded", v, p)
	}
	time.Sleep(500 * time.Millisecond)
	if p := g.r.Position(); p != 3 {
		t.Fatalf("after recording, the position is %d, want 3", p)
	}
	// And it is kept on disk for the next start.
	time.Sleep(100 * time.Millisecond)
	g.stop()
	data, _ := os.ReadFile(g.state)
	if !strings.Contains(string(data), `"version":3`) {
		t.Fatalf("saved position: %s", data)
	}
}

func TestRestartContinuesFromTheSavedPosition(t *testing.T) {
	g := newRig(t, nil, nil)
	g.send(other, "CREATE(1,b.md,1000,1000,1,420,18,,linux,true):50")
	g.send(other, "CREATE(1,c.md,1000,1000,1,420,18,,linux,true):51")
	time.Sleep(150 * time.Millisecond)
	g.stop()
	if got := g.src.fromList(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("a first start reads from now: %v", got)
	}
	// A second recorder with the same state file asks from where the first stopped.
	src2 := newFakeSource()
	r2 := New(Options{Folder: g.root, Source: src2, Skip: history.DefaultSkip(), Handler: &rec{},
		Session: func() (int64, error) { return me, nil }, StateFile: g.state, Group: GroupOptions{Quiet: quiet}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r2.Run(ctx, nil) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	if got := src2.fromList(); len(got) == 0 || got[0] != 2 {
		t.Fatalf("a second start should continue from 2, asked from %v", got)
	}
}

func TestMissingEntriesAskForACatchUp(t *testing.T) {
	g := newRig(t, nil, nil)
	g.own("CREATE(1,a.md,1000,1000,1,420,18,,linux,true):11") // v1
	g.ver += 5                                                // versions 2..6 are no longer in the log
	g.own("CREATE(1,b.md,1000,1000,1,420,18,,linux,true):12")
	time.Sleep(100 * time.Millisecond)
	if g.h.count("catchup") != 1 {
		t.Fatalf("%q", g.h.snapshot())
	}
}

func TestAReaderThatDiesIsRestartedFromWhereItWas(t *testing.T) {
	g := newRig(t, nil, nil)
	g.send(other, "CREATE(1,b.md,1000,1000,1,420,18,,linux,true):50")
	g.send(other, "CREATE(1,c.md,1000,1000,1,420,18,,linux,true):51")
	time.Sleep(100 * time.Millisecond)
	g.src.fail <- struct{}{}
	time.Sleep(200 * time.Millisecond)
	got := g.src.fromList()
	if len(got) < 2 || got[1] != 2 {
		t.Fatalf("the second reader should start after version 2: %v", got)
	}
	if g.r.Restarts() < 1 {
		t.Fatal("restarts not counted")
	}
	// It goes on working.
	g.write("a.md", "x")
	g.own("CREATE(1,a.md,1000,1000,1,420,18,,linux,true):11")
	g.settle()
	wantEvents(t, g.h, "save a.md")
	if g.h.count("catchup") != 0 {
		t.Fatal("a restart that continues from a position needs no catch-up")
	}
}

func TestAReaderThatDiesWithoutAPositionAsksForACatchUp(t *testing.T) {
	g := newRig(t, nil, nil)
	g.src.fail <- struct{}{}
	time.Sleep(200 * time.Millisecond)
	if g.h.count("catchup") < 1 {
		t.Fatal("a reader that had read nothing and died may have missed changes")
	}
}

func TestANewSessionRestartsTheReader(t *testing.T) {
	g := newRig(t, nil, nil)
	g.write("a.md", "x")
	g.own("CREATE(1,a.md,1000,1000,1,420,18,,linux,true):11")
	g.settle()
	g.setSession(me + 100) // the drive was mounted again
	time.Sleep(300 * time.Millisecond)
	if n := len(g.src.fromList()); n < 2 {
		t.Fatalf("the reader was not restarted after the session changed (%d starts)", n)
	}
	g.write("b.md", "y")
	g.send(me+100, "CREATE(1,b.md,1000,1000,1,420,18,,linux,true):12")
	g.send(me, "CREATE(1,old.md,1000,1000,1,420,18,,linux,true):13") // the old session is no longer ours
	g.settle()
	wantEvents(t, g.h, "save a.md", "save b.md")
}
