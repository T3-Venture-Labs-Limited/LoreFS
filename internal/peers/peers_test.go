package peers

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteAndRead(t *testing.T) {
	root := t.TempDir()
	if got := Read(root); len(got) != 0 {
		t.Fatalf("no notes yet, got %v", got)
	}
	seen := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	for _, n := range []Note{
		{Machine: "alice", Version: "3ac46d7", CommitTime: 100, Seen: seen},
		{Machine: "aaron", Version: "9a93131", CommitTime: 90, Seen: seen},
		{Machine: "alice", Version: "abc1234", CommitTime: 120, Seen: seen}, // an update replaces the note
	} {
		if err := Write(root, n); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(Dir(root), "broken.json"), []byte("{not json"), 0o644)
	got := Read(root)
	if len(got) != 2 || got[0].Machine != "aaron" || got[1].Version != "abc1234" || got[1].CommitTime != 120 {
		t.Fatalf("unexpected notes: %+v", got)
	}
	Remove(root, "aaron")
	if left := Read(root); len(left) != 1 || left[0].Machine != "alice" {
		t.Fatalf("after aaron left: %+v", left)
	}
	if !got[0].Seen.Equal(seen) {
		t.Fatalf("seen time lost: %v", got[0].Seen)
	}
}
