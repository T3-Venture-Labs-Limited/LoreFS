package record

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/changelog"
)

const me, other = 7, 9

type feed struct {
	t   *testing.T
	r   *Resolver
	ver int64
}

func newFeed(t *testing.T, info InfoFunc) *feed {
	return &feed{t: t, r: NewResolver(me, info)}
}

// do parses a line the way JuiceFS prints it and returns the events.
func (f *feed) do(session int, op string) []Event {
	f.t.Helper()
	f.ver++
	line := fmt.Sprintf("%d: 1795175136.%09d|%s|(%d,%d)", f.ver, f.ver, op, session, f.ver)
	e, err := changelog.Parse(line)
	if err != nil {
		f.t.Fatalf("%s: %v", line, err)
	}
	return f.r.Handle(e)
}

func paths(evs []Event, k Kind) []string {
	var out []string
	for _, e := range evs {
		if e.Kind == k {
			out = append(out, e.Path)
		}
	}
	return out
}

func TestCreateAndWriteAreSavesOfTheRightPath(t *testing.T) {
	f := newFeed(t, nil)
	f.do(other, "CREATE(1,notes,1000,1000,2,493,18,,linux,true):10") // a folder made by another computer
	if evs := f.do(me, "CREATE(10,plan.md,1000,1000,1,420,18,,linux,true):11"); !reflect.DeepEqual(paths(evs, Save), []string{"notes/plan.md"}) {
		t.Fatalf("an empty new file is a save: %v", evs)
	}
	evs := f.do(me, "WRITE(11,0,0,1,3,1795175136,574465020):1")
	if !reflect.DeepEqual(paths(evs, Save), []string{"notes/plan.md"}) || evs[0].Big {
		t.Fatalf("%v", evs)
	}
}

func TestOtherComputersChangesAreLearnedButNotReported(t *testing.T) {
	f := newFeed(t, nil)
	if evs := f.do(other, "CREATE(1,theirs.md,1000,1000,1,420,18,,linux,true):20"); len(evs) != 0 {
		t.Fatalf("%v", evs)
	}
	if evs := f.do(other, "WRITE(20,0,0,1,3,1795175136,574465020):1"); len(evs) != 0 {
		t.Fatalf("%v", evs)
	}
	// This computer then writes to a file the other one made: the path is known.
	evs := f.do(me, "WRITE(20,0,0,2,5,1795175137,1):1")
	if !reflect.DeepEqual(paths(evs, Save), []string{"theirs.md"}) {
		t.Fatalf("%v", evs)
	}
}

func TestRenamedFolderGivesTheNewPath(t *testing.T) {
	f := newFeed(t, nil)
	f.do(me, "CREATE(1,old,1000,1000,2,493,18,,linux,true):10")
	f.do(me, "CREATE(10,a.md,1000,1000,1,420,18,,linux,true):11")
	evs := f.do(me, "MOVE(1,old,1,new,0,0,0):10")
	if len(evs) != 1 || evs[0].Kind != Rename || evs[0].From != "old" || evs[0].Path != "new" {
		t.Fatalf("%v", evs)
	}
	evs = f.do(me, "WRITE(11,0,0,1,3,1795175136,1):1")
	if !reflect.DeepEqual(paths(evs, Save), []string{"new/a.md"}) {
		t.Fatalf("after renaming the folder: %v", evs)
	}
	// And by another computer: learned, so the next write still resolves.
	f.do(other, "MOVE(1,new,1,newer,0,0,0):10")
	evs = f.do(me, "WRITE(11,0,0,1,3,1795175136,1):1")
	if !reflect.DeepEqual(paths(evs, Save), []string{"newer/a.md"}) {
		t.Fatalf("after another computer renamed it: %v", evs)
	}
}

func TestSaveByRenameReplacesTheOriginal(t *testing.T) {
	f := newFeed(t, nil)
	f.do(me, "CREATE(1,doc.md,1000,1000,1,420,18,,linux,true):11")
	f.do(me, "WRITE(11,0,0,1,5,1795175136,1):1")
	f.do(me, "CREATE(1,.doc.md.swp,1000,1000,1,420,18,,linux,true):12")
	f.do(me, "WRITE(12,0,0,2,9,1795175137,1):1")
	// The editor renames its temporary file over the original; inode 11 is overwritten.
	evs := f.do(me, "MOVE(1,.doc.md.swp,1,doc.md,0,11,0):12")
	if len(evs) != 1 || evs[0].Kind != Rename || evs[0].From != ".doc.md.swp" || evs[0].Path != "doc.md" {
		t.Fatalf("%v", evs)
	}
	// Later writes to the new inode go to doc.md; the replaced inode is gone.
	if evs := f.do(me, "WRITE(12,0,0,3,9,1795175138,1):1"); !reflect.DeepEqual(paths(evs, Save), []string{"doc.md"}) {
		t.Fatalf("%v", evs)
	}
	if evs := f.do(me, "WRITE(11,0,0,3,9,1795175138,1):1"); len(evs) != 0 {
		t.Fatalf("a write to a replaced file means nothing: %v", evs)
	}
}

func TestDeleteAndHardLinks(t *testing.T) {
	f := newFeed(t, nil)
	f.do(me, "CREATE(1,a.md,1000,1000,1,420,18,,linux,true):11")
	f.do(me, "LINK(11,1,b.md):11")
	evs := f.do(me, "WRITE(11,0,0,1,3,1795175136,1):1")
	got := paths(evs, Save)
	if len(got) != 2 {
		t.Fatalf("both names of a hard linked file are saved: %v", got)
	}
	evs = f.do(me, "UNLINK(1,a.md,0):11")
	if len(evs) != 1 || evs[0].Kind != Delete || evs[0].Path != "a.md" {
		t.Fatalf("%v", evs)
	}
	if got := paths(f.do(me, "WRITE(11,0,0,1,3,1795175136,1):1"), Save); !reflect.DeepEqual(got, []string{"b.md"}) {
		t.Fatalf("after one name was removed: %v", got)
	}
}

func TestUnknownInodeIsLookedUpOnceAndCached(t *testing.T) {
	asked := 0
	f := newFeed(t, func(ino int64) string { asked++; return "/projects/report.md" })
	for i := 0; i < 3; i++ {
		evs := f.do(me, "WRITE(500,0,0,1,3,1795175136,1):1")
		if !reflect.DeepEqual(paths(evs, Save), []string{"projects/report.md"}) {
			t.Fatalf("%v", evs)
		}
	}
	if asked != 1 {
		t.Fatalf("asked the drive %d times for entries that arrived together", asked)
	}
}

func TestDeletedAndUnfindableInodesMeanNothing(t *testing.T) {
	f := newFeed(t, func(ino int64) string {
		if ino == 600 {
			return "/.trash/2026-11-20-10/600"
		}
		return ""
	})
	if evs := f.do(me, "WRITE(600,0,0,1,3,1795175136,1):1"); len(evs) != 0 {
		t.Fatalf("an inode in the trash: %v", evs)
	}
	f.r.Now = func() time.Time { return time.Now().Add(time.Minute) }
	if evs := f.do(me, "WRITE(601,0,0,1,3,1795175136,1):1"); len(evs) != 0 {
		t.Fatalf("an inode nobody can find: %v", evs)
	}
}

func TestBigSlicesAreFlagged(t *testing.T) {
	f := newFeed(t, nil)
	f.do(me, "CREATE(1,big.bin,1000,1000,1,420,18,,linux,true):11")
	evs := f.do(me, fmt.Sprintf("WRITE(11,0,0,1,%d,1795175136,1):1", 64<<20))
	if len(evs) != 1 || !evs[0].Big {
		t.Fatalf("%v", evs)
	}
	evs = f.do(me, fmt.Sprintf("WRITE(11,1,0,2,%d,1795175136,1):1", 1<<20))
	if len(evs) != 1 || evs[0].Big {
		t.Fatalf("%v", evs)
	}
}

func TestFoldersAndLinksAreNotSaved(t *testing.T) {
	f := newFeed(t, nil)
	if evs := f.do(me, "CREATE(1,dir,1000,1000,2,493,18,,linux,true):10"); len(evs) != 0 {
		t.Fatalf("a folder is not a file: %v", evs)
	}
	if evs := f.do(me, "CREATE(1,ln,1000,1000,3,511,18,target,linux,true):12"); len(evs) != 0 {
		t.Fatalf("a symbolic link: %v", evs)
	}
}

func TestNamesWithEscapesAreDecoded(t *testing.T) {
	f := newFeed(t, nil)
	f.do(me, "CREATE(1,caf%C3%A9%2C%5Bone%5D%7Ctwo.md,1000,1000,1,420,18,,linux,true):11")
	evs := f.do(me, "WRITE(11,0,0,1,3,1795175136,1):1")
	if !reflect.DeepEqual(paths(evs, Save), []string{"café,[one]|two.md"}) {
		t.Fatalf("%q", paths(evs, Save))
	}
}

func TestMovedIntoTheTrashIsADelete(t *testing.T) {
	f := newFeed(t, func(int64) string { return "" })
	f.do(me, "CREATE(1,gone.md,1000,1000,1,420,18,,linux,true):11")
	// Destination parent 3 (the trash folder) is unknown and has no path.
	evs := f.do(me, "MOVE(1,gone.md,3,2026-11-20-10,0,0,0):11")
	if len(evs) != 1 || evs[0].Kind != Delete || evs[0].Path != "gone.md" {
		t.Fatalf("%v", evs)
	}
}

func TestMemoryIsBounded(t *testing.T) {
	f := newFeed(t, func(int64) string { return "" })
	f.r.MaxNodes = 100
	for i := int64(10); i < 1010; i++ {
		f.do(other, fmt.Sprintf("CREATE(1,f%d,1000,1000,1,420,18,,linux,true):%d", i, i))
	}
	if n := len(f.r.nodes); n > 100 {
		t.Fatalf("%d nodes kept", n)
	}
}

func TestParseInfo(t *testing.T) {
	out := "/home/alice/Lore/x.md :\n  inode: 5\n  files: 1\n  length: 3\n  size: 4096\n  path: /notes/x.md\n"
	if got := ParseInfo([]byte(out)); got != "/notes/x.md" {
		t.Fatalf("%q", got)
	}
	if ParseInfo([]byte("nothing here")) != "" {
		t.Fatal("expected empty")
	}
}

func TestParseSession(t *testing.T) {
	cfg := `{"Meta":{"Strict":true,"Retries":10,"Sid":12,"MountPoint":"/x"},"Chunk":{}}`
	if n, ok := ParseSession([]byte(cfg)); !ok || n != 12 {
		t.Fatalf("%d %v", n, ok)
	}
	if _, ok := ParseSession([]byte(`{"Meta":{}}`)); ok {
		t.Fatal("no session")
	}
	if _, ok := ParseSession([]byte(strings.Repeat("x", 10))); ok {
		t.Fatal("garbage")
	}
}
