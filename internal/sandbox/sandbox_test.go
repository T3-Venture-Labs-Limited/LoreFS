package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
)

func setup(t *testing.T) (*history.Store, func(time.Duration)) {
	t.Helper()
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	s := history.New(t.TempDir(), "alice", jfs.Copy{})
	s.Now = func() time.Time { return now }
	return s, func(d time.Duration) { now = now.Add(d) }
}

func put(t *testing.T, s *history.Store, rel, content string) {
	t.Helper()
	p := filepath.Join(s.Root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	when := s.Now()
	os.Chtimes(p, when, when)
}

func get(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSandboxLifecycle(t *testing.T) {
	s, advance := setup(t)
	put(t, s, "plans/a.md", "A")
	put(t, s, "plans/sub/b.md", "B")
	put(t, s, "plans/keep.md", "KEEP")
	s.Record("plans/a.md")

	info, err := Create(s, "plans", "try1")
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != filepath.Join(s.Root, ".lorefs", "sandboxes", "try1") {
		t.Fatalf("path = %s", info.Path)
	}
	if _, _, err := Diff(s.Root, "try1"); err != nil {
		t.Fatal(err)
	}
	if _, ch, _ := Diff(s.Root, "try1"); len(ch) != 0 {
		t.Fatalf("a fresh sandbox has no changes, got %+v", ch)
	}

	// Work in the sandbox: change one, add one, delete one.
	advance(time.Hour)
	sb := func(rel string) string { return filepath.Join(info.Path, rel) }
	os.WriteFile(sb("a.md"), []byte("A changed"), 0o644)
	os.WriteFile(sb("new.md"), []byte("NEW"), 0o644)
	os.Remove(sb("keep.md"))

	// The original is untouched.
	if get(t, filepath.Join(s.Root, "plans/a.md")) != "A" {
		t.Fatal("original changed by sandbox work")
	}
	if _, err := os.Stat(filepath.Join(s.Root, "plans/new.md")); !os.IsNotExist(err) {
		t.Fatal("new file leaked into the original")
	}

	_, ch, err := Diff(s.Root, "try1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ch) != 2 || ch[0].Path != "a.md" || ch[0].Added || ch[1].Path != "new.md" || !ch[1].Added {
		t.Fatalf("diff = %+v", ch)
	}

	list, _ := List(s.Root)
	if len(list) != 1 || list[0].Name != "try1" || list[0].Source != "plans" {
		t.Fatalf("list = %+v", list)
	}

	applied, err := Apply(s, "try1")
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 {
		t.Fatalf("applied %d", len(applied))
	}
	if get(t, filepath.Join(s.Root, "plans/a.md")) != "A changed" || get(t, filepath.Join(s.Root, "plans/new.md")) != "NEW" {
		t.Fatal("apply did not copy the results")
	}
	if get(t, filepath.Join(s.Root, "plans/keep.md")) != "KEEP" {
		t.Fatal("apply must not delete files that were deleted in the sandbox")
	}
	vs, _ := s.List("plans/a.md")
	if len(vs) != 2 || get(t, vs[1].Path) != "A" {
		t.Fatalf("the replaced content must stay in history: %d versions", len(vs))
	}

	if err := Remove(s.Root, "try1", jfs.Copy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(info.Path); !os.IsNotExist(err) {
		t.Fatal("sandbox folder should be gone")
	}
	if get(t, filepath.Join(s.Root, "plans/a.md")) != "A changed" {
		t.Fatal("removing a sandbox must not touch the original")
	}
	if err := Remove(s.Root, "try1", jfs.Copy{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove: %v", err)
	}
}

func TestOriginalChangedMeanwhileIsFlagged(t *testing.T) {
	s, advance := setup(t)
	put(t, s, "docs/a.md", "A")
	info, _ := Create(s, "docs", "x")
	advance(time.Hour)
	os.WriteFile(filepath.Join(info.Path, "a.md"), []byte("from sandbox"), 0o644)
	put(t, s, "docs/a.md", "from colleague")
	_, ch, _ := Diff(s.Root, "x")
	if len(ch) != 1 || !ch[0].OriginalChanged {
		t.Fatalf("want a flagged change, got %+v", ch)
	}
	// Applying still works, and the colleague's content is in history.
	if _, err := Apply(s, "x"); err != nil {
		t.Fatal(err)
	}
	vs, _ := s.List("docs/a.md")
	found := false
	for _, v := range vs {
		if get(t, v.Path) == "from colleague" {
			found = true
		}
	}
	if !found {
		t.Fatal("the colleague's content must be recoverable from history")
	}
}

func TestNamesAndErrors(t *testing.T) {
	s, _ := setup(t)
	put(t, s, "docs/a.md", "A")
	if _, err := Create(s, "docs", "Bad Name"); !errors.Is(err, ErrBadName) {
		t.Fatalf("bad name: %v", err)
	}
	if _, err := Create(s, "docs/a.md", "x"); err == nil {
		t.Fatal("a file is not a folder")
	}
	if _, err := Create(s, "docs", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(s, "docs", "x"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	info, err := Create(s, "docs", "")
	if err != nil || info.Name == "" {
		t.Fatalf("default name: %+v %v", info, err)
	}
	if _, _, err := Diff(s.Root, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

// Sandbox records live on the shared drive, so anyone can edit them. A
// forged record must not make apply write outside the drive.
func TestForgedRecordsAreRefused(t *testing.T) {
	s, _ := setup(t)
	put(t, s, "notes/a.md", "a")
	if _, err := Create(s, "notes", "try1"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../../x", "../sandboxes/try1", "UPPER"} {
		if _, _, err := Diff(s.Root, name); !errors.Is(err, ErrBadName) {
			t.Errorf("name %q: want ErrBadName, got %v", name, err)
		}
	}
	_, meta := paths(s.Root, "try1")
	for _, src := range []string{"../outside", "/etc", ".lorefs/history", ".", ""} {
		os.WriteFile(meta, []byte(`{"name":"try1","source":"`+src+`"}`), 0o644)
		if _, err := Apply(s, "try1"); err == nil {
			t.Errorf("source %q was accepted", src)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.Root), "outside")); !os.IsNotExist(err) {
		t.Fatal("something was written outside the drive")
	}
}
