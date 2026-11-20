// Package history keeps earlier versions of each file on the drive.
//
// A version is a clone of the file taken after a save. On JuiceFS a clone
// copies index entries only, so a version costs no extra stored data
// until the file's content is later replaced.
//
// Layout under <drive>/.lorefs:
//
//	history/<path of file>/<UTC time>~<machine>~<file name>   one version
//	history/<path of file>/.deleted                           file was deleted
//	journal/<machine>/<date>.jsonl                            activity log
package history

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
)

const (
	stampLayout   = "20060102T150405.000Z"
	deletedMarker = ".deleted"
)

var (
	versionRe = regexp.MustCompile(`^(\d{8}T\d{6}\.\d{3}Z)~([^~]+)~(.+)$`)
	machineRe = regexp.MustCompile(`[^a-z0-9-]+`)
)

// ErrOutside is returned for paths that are not ordinary files of the drive.
var ErrOutside = errors.New("that path is not inside the drive")

// ErrNoVersion is returned when a requested version does not exist.
var ErrNoVersion = errors.New("no such version")

// Store reads and writes the history of one drive.
type Store struct {
	Root      string        // top folder of the drive
	Machine   string        // name recorded with each version
	Cloner    jfs.Cloner    // how versions are made
	Retention time.Duration // how long replaced versions are kept
	Skip      *Skip
	Journal   *Journal
	Now       func() time.Time

	// One of these is held while a version of a file is being made,
	// chosen by the file's path. The watcher, the catch-up walk and the
	// commands can all ask for a version of the same file at the same
	// moment; without this, two of them would each find no version of the
	// current content and each make one.
	recording [64]sync.Mutex
}

// New returns a Store with the default retention and skip list.
func New(root, machine string, cloner jfs.Cloner) *Store {
	machine = CleanMachine(machine)
	return &Store{
		Root:      filepath.Clean(root),
		Machine:   machine,
		Cloner:    cloner,
		Retention: 30 * 24 * time.Hour,
		Skip:      DefaultSkip(),
		Journal:   NewJournal(filepath.Clean(root), machine),
		Now:       time.Now,
	}
}

// CleanMachine turns any text into a safe machine name.
func CleanMachine(name string) string {
	name = machineRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	name = strings.Trim(name, "-")
	if name == "" {
		return "machine"
	}
	return name
}

// Version is one recorded version of a file.
type Version struct {
	Name    string    // file name inside the history folder
	Path    string    // absolute path of the version
	Time    time.Time // when it was recorded
	Machine string    // who recorded it
	Size    int64
	ModTime time.Time // modification time of the content it holds
}

// Rel converts any path to one relative to the drive. It rejects paths
// outside the drive and paths inside the drive's own bookkeeping.
func (s *Store) Rel(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		abs = filepath.Join(resolved, filepath.Base(abs))
	}
	root := s.Root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrOutside
	}
	if first, _, _ := strings.Cut(filepath.ToSlash(rel), "/"); first == MetaDir || first == ".trash" {
		return "", ErrOutside
	}
	return rel, nil
}

func (s *Store) abs(rel string) string { return filepath.Join(s.Root, rel) }

// HistDir is the folder holding the versions of one file.
func (s *Store) HistDir(rel string) string {
	return filepath.Join(s.Root, MetaDir, "history", rel)
}

func (s *Store) historyRoot() string { return filepath.Join(s.Root, MetaDir, "history") }

// List returns the versions of a file, newest first.
func (s *Store) List(rel string) ([]Version, error) {
	return listDir(s.HistDir(rel))
}

func listDir(dir string) ([]Version, error) {
	entries, err := jfs.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Version
	for _, info := range entries {
		if v, ok := versionOf(dir, info); ok {
			out = append(out, v)
		}
	}
	newestFirst(out)
	return out, nil
}

// versionOf reads a version from an entry of a history folder.
func versionOf(dir string, info os.FileInfo) (Version, bool) {
	if info.IsDir() {
		return Version{}, false
	}
	m := versionRe.FindStringSubmatch(info.Name())
	if m == nil {
		return Version{}, false
	}
	t, err := time.Parse(stampLayout, m[1])
	if err != nil {
		return Version{}, false
	}
	return Version{
		Name: info.Name(), Path: filepath.Join(dir, info.Name()), Time: t,
		Machine: m[2], Size: info.Size(), ModTime: info.ModTime(),
	}, true
}

func newestFirst(vs []Version) {
	sort.Slice(vs, func(a, b int) bool { return vs[a].Name > vs[b].Name })
}

// DeletedAt reports when a file was deleted, if its history says so.
func (s *Store) DeletedAt(rel string) (time.Time, bool) {
	st, err := os.Stat(filepath.Join(s.HistDir(rel), deletedMarker))
	if err != nil {
		return time.Time{}, false
	}
	return st.ModTime(), true
}

func sameMoment(a, b time.Time) bool {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d < time.Millisecond
}

// Record makes a version of the file's current content unless its newest
// version already holds that content. It reports whether one was made.
func (s *Store) Record(rel string) (bool, error) {
	v, err := s.record(rel, "save")
	return v != nil, err
}

func (s *Store) record(rel, op string) (*Version, error) {
	if s.Skip.Path(rel) {
		return nil, nil
	}
	h := fnv.New32a()
	h.Write([]byte(rel))
	lock := &s.recording[h.Sum32()%uint32(len(s.recording))]
	lock.Lock()
	defer lock.Unlock()
	src := s.abs(rel)
	st, err := os.Lstat(src)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, nil
	}
	dir := s.HistDir(rel)
	versions, err := listDir(dir)
	if err != nil {
		return nil, err
	}
	if len(versions) > 0 && versions[0].Size == st.Size() {
		if sameMoment(versions[0].ModTime, st.ModTime()) {
			os.Remove(filepath.Join(dir, deletedMarker)) // the file exists again
			return nil, nil
		}
		// Same size, different time. Copy tools set a file's time after
		// writing it (rclone does so seconds later), so this is often the
		// content already recorded. Compare, and if it is, let the
		// existing version carry the new time instead of making another.
		if st.Size() <= sameContentLimit && sameContent(src, versions[0].Path) {
			os.Chtimes(versions[0].Path, st.ModTime(), st.ModTime())
			os.Remove(filepath.Join(dir, deletedMarker))
			return nil, nil
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	now := s.Now().UTC()
	name := now.Format(stampLayout) + "~" + s.Machine + "~" + filepath.Base(rel)
	for len(versions) > 0 && name <= versions[0].Name { // clock went backwards or same millisecond
		now = now.Add(time.Millisecond)
		name = now.Format(stampLayout) + "~" + s.Machine + "~" + filepath.Base(rel)
	}
	dst := filepath.Join(dir, name)
	if err := s.Cloner.Clone(src, dst); err != nil {
		return nil, fmt.Errorf("recording a version of %s: %w", rel, err)
	}
	// A program may still be writing the file: the drive then shows it
	// at the size written so far, while the copy holds only what has
	// reached the storage (often nothing). That is not a version of the
	// file. Drop it; the file is recorded when the program closes it.
	// This happens when a file is found in a folder created a moment ago,
	// where the close of the file is still to come (measured on the real
	// drive: 7 of 8 photos copied side by side into a new folder got an
	// empty version first).
	if vst, err := os.Lstat(dst); err == nil && vst.Size() != st.Size() {
		os.Remove(dst)
		if len(versions) == 0 {
			os.Remove(dir) // only succeeds if nothing else is in it
		}
		return nil, nil
	}
	// Give the version the content's own time, and make it read-only so
	// that opening it in an application cannot change it.
	os.Chtimes(dst, st.ModTime(), st.ModTime())
	os.Chmod(dst, 0o444)
	os.Remove(filepath.Join(dir, deletedMarker))
	v := &Version{Name: name, Path: dst, Time: now, Machine: s.Machine, Size: st.Size(), ModTime: st.ModTime()}
	s.Journal.Append(Event{Time: now, Op: op, Path: filepath.ToSlash(rel), Size: st.Size(), Version: name})
	return v, nil
}

// sameContentLimit is the largest file whose content is compared with its
// newest version before recording another. Larger files simply get the
// new version, which costs about 200 bytes of index and no storage.
const sameContentLimit = 32 << 20

func sameContent(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	ba, bb := make([]byte, 256<<10), make([]byte, 256<<10)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		if ea != nil || eb != nil {
			// Both must have ended, and ended cleanly.
			endA := ea == io.EOF || ea == io.ErrUnexpectedEOF
			endB := eb == io.EOF || eb == io.ErrUnexpectedEOF
			return endA && endB
		}
	}
}

// ChangeTime is when a file last changed in any way: its content, or its
// details such as the time a copy tool gave it. A file copied onto the
// drive with its old date looks unchanged by date alone.
func ChangeTime(info os.FileInfo) time.Time {
	t := info.ModTime()
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if c := time.Unix(st.Ctim.Sec, st.Ctim.Nsec); c.After(t) {
			t = c
		}
	}
	return t
}

// NoteSave logs that a file which gets no versions (a video, by default)
// was saved, so that the activity log shows it and other machines know
// to fetch it. It reports whether the file is of that kind.
func (s *Store) NoteSave(rel string) bool {
	if s.Skip.Ignored(rel) || !s.Skip.Unversioned(filepath.Base(rel)) {
		return false
	}
	st, err := os.Lstat(s.abs(rel))
	if err != nil || !st.Mode().IsRegular() {
		return true
	}
	s.Journal.Append(Event{Time: s.Now().UTC(), Op: "save", Path: filepath.ToSlash(rel), Size: st.Size()})
	return true
}

// MarkDeleted notes that a file was deleted. Its versions stay, and the
// newest one is kept for the retention period from now.
func (s *Store) MarkDeleted(rel string) error {
	if s.Skip.Ignored(rel) {
		return nil
	}
	if s.Skip.Unversioned(filepath.Base(rel)) {
		// No versions exist, but the deletion still belongs in the log.
		s.Journal.Append(Event{Time: s.Now().UTC(), Op: "delete", Path: filepath.ToSlash(rel)})
		return nil
	}
	dir := s.HistDir(rel)
	if versions, _ := listDir(dir); len(versions) == 0 {
		return nil // never recorded: nothing to bring back
	}
	now := s.Now()
	marker := filepath.Join(dir, deletedMarker)
	if _, err := os.Stat(marker); err == nil {
		return nil // already known to be deleted
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		return err
	}
	os.Chtimes(marker, now, now)
	s.Journal.Append(Event{Time: now.UTC(), Op: "delete", Path: filepath.ToSlash(rel)})
	return nil
}

// Rename moves the history of a file or folder to its new path.
func (s *Store) Rename(oldRel, newRel string) error {
	if s.Skip.Path(newRel) {
		return s.MarkDeleted(oldRel)
	}
	from, to := s.HistDir(oldRel), s.HistDir(newRel)
	if _, err := os.Stat(from); err != nil {
		return nil // nothing recorded under the old path
	}
	if _, err := os.Stat(to); err == nil {
		// The target already has history (a file was replaced by another
		// real file). Keep both histories where they are.
		return s.MarkDeleted(oldRel)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	s.Journal.Append(Event{Time: s.Now().UTC(), Op: "rename", Path: filepath.ToSlash(oldRel), To: filepath.ToSlash(newRel)})
	return nil
}

// Find returns the version of a file with the given name, or the n-th
// newest when ref is a number starting at 1.
func (s *Store) Find(rel, ref string) (Version, error) {
	versions, err := s.List(rel)
	if err != nil {
		return Version{}, err
	}
	var n int
	if _, err := fmt.Sscanf(ref, "%d", &n); err == nil && fmt.Sprint(n) == ref {
		if n < 1 || n > len(versions) {
			return Version{}, fmt.Errorf("%w: %s has %d version(s)", ErrNoVersion, rel, len(versions))
		}
		return versions[n-1], nil
	}
	for _, v := range versions {
		if v.Name == ref {
			return v, nil
		}
	}
	return Version{}, fmt.Errorf("%w: %s", ErrNoVersion, ref)
}

// Restore makes a version's content the file's current content in one
// step. The content it replaces is kept as a version first, so a restore
// can be undone.
func (s *Store) Restore(rel string, v Version) error {
	target := s.abs(rel)
	if _, err := s.record(rel, "save"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(target), fmt.Sprintf(".lorefs-restore-%d~%s", s.Now().UnixNano(), filepath.Base(rel)))
	if err := s.Cloner.Clone(v.Path, tmp); err != nil {
		return fmt.Errorf("restoring %s: %w", rel, err)
	}
	now := s.Now()
	os.Chmod(tmp, 0o644)
	os.Chtimes(tmp, now, now)
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	if _, err := s.record(rel, "restore"); err != nil {
		return err
	}
	return s.Journal.Flush()
}

// ReplaceWith puts the content of another file of the drive in place of
// rel in one step, keeping what it replaces as a version. Sandboxes use
// it to copy their results back.
func (s *Store) ReplaceWith(rel, srcAbs string) error {
	target := s.abs(rel)
	if _, err := s.record(rel, "save"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(target), fmt.Sprintf(".lorefs-apply-%d~%s", s.Now().UnixNano(), filepath.Base(rel)))
	if err := s.Cloner.Clone(srcAbs, tmp); err != nil {
		return err
	}
	now := s.Now()
	os.Chtimes(tmp, now, now)
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	_, err := s.record(rel, "save")
	return err
}

// PruneResult counts what a Prune run removed.
type PruneResult struct {
	Versions int // versions removed
	Adopted  int // histories of vanished files newly marked as deleted
	Journals int // journal files removed
}

// Prune removes versions that were replaced longer ago than the retention
// period. The newest version of a file that still exists is always kept.
// A deleted file keeps its versions until the retention period has passed
// since the deletion. Journal files older than 90 days are removed.
func (s *Store) Prune() (PruneResult, error) { return s.PruneContext(context.Background()) }

// PruneContext is Prune that stops when ctx ends. Stopping half-way is
// safe: every step removes something that is no longer needed, and the
// next run carries on.
//
// It needs to see the whole drive: a file in a folder that could not be
// read would look deleted, so any folder that cannot be read stops the
// run before anything is removed.
func (s *Store) PruneContext(ctx context.Context) (PruneResult, error) {
	var res PruneResult
	now := s.Now()
	root := s.historyRoot()
	if _, err := os.Stat(root); os.IsNotExist(err) {
		res.Journals = s.pruneJournals(now)
		return res, nil
	}

	// Which files exist now. One walk of the drive answers that for every
	// file at once; asking about each file separately would cost a
	// request per file.
	live := map[string]bool{}
	strict := jfs.WalkOptions{Ctx: ctx, Strict: true}
	err := jfs.WalkWith(s.Root, strict, func(p string, info os.FileInfo) error {
		if info.IsDir() {
			if filepath.Dir(p) == s.Root && (info.Name() == MetaDir || info.Name() == ".trash") {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Mode().IsRegular() {
			rel, _ := filepath.Rel(s.Root, p)
			live[rel] = true
		}
		return nil
	})
	if err != nil {
		return res, err
	}

	// What the history holds, folder by folder.
	type folder struct {
		versions []Version
		marker   *time.Time // when the file was noted as deleted
		entries  int
	}
	folders := map[string]*folder{root: {}}
	get := func(dir string) *folder {
		f := folders[dir]
		if f == nil {
			f = &folder{}
			folders[dir] = f
		}
		return f
	}
	err = jfs.WalkWith(root, strict, func(p string, info os.FileInfo) error {
		parent := get(filepath.Dir(p))
		parent.entries++
		switch {
		case info.IsDir():
			get(p)
		case info.Name() == deletedMarker:
			t := info.ModTime()
			parent.marker = &t
		default:
			if v, ok := versionOf(filepath.Dir(p), info); ok {
				parent.versions = append(parent.versions, v)
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}

	// Deepest folders first, so emptied folders can be removed on the way up.
	dirs := make([]string, 0, len(folders))
	for dir := range folders {
		dirs = append(dirs, dir)
	}
	sort.Slice(dirs, func(a, b int) bool {
		if len(dirs[a]) != len(dirs[b]) {
			return len(dirs[a]) > len(dirs[b])
		}
		return dirs[a] < dirs[b]
	})
	for _, dir := range dirs {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		f := folders[dir]
		if len(f.versions) > 0 {
			newestFirst(f.versions)
			rel, _ := filepath.Rel(root, dir)
			one, removed := s.pruneOne(live[rel], dir, f.versions, f.marker, now)
			res.add(one)
			f.entries -= removed
		} else if f.marker != nil && now.Sub(*f.marker) > s.Retention {
			// A deletion note with no versions left beside it.
			if os.Remove(filepath.Join(dir, deletedMarker)) == nil {
				f.entries--
			}
		}
		if dir != root && f.entries <= 0 && os.Remove(dir) == nil {
			get(filepath.Dir(dir)).entries--
		}
	}
	res.Journals = s.pruneJournals(now)
	return res, nil
}

func (r *PruneResult) add(o PruneResult) {
	r.Versions += o.Versions
	r.Adopted += o.Adopted
}

// pruneOne applies retention to the versions of one file. exists says
// whether the file itself is still on the drive; marker is when it was
// noted as deleted, if it was. It also returns how many entries it took
// out of the history folder (a negative number if it added the marker).
func (s *Store) pruneOne(exists bool, dir string, versions []Version, marker *time.Time, now time.Time) (res PruneResult, removed int) {
	markerPath := filepath.Join(dir, deletedMarker)
	if !exists {
		if marker == nil {
			// The file vanished without the recorder seeing it. Start the
			// retention clock now.
			if os.WriteFile(markerPath, nil, 0o644) == nil {
				os.Chtimes(markerPath, now, now)
				res.Adopted++
				removed--
			}
			return res, removed
		}
		if now.Sub(*marker) > s.Retention {
			for _, v := range versions {
				if os.Remove(v.Path) == nil {
					res.Versions++
					removed++
				}
			}
			if os.Remove(markerPath) == nil {
				removed++
			}
		}
		return res, removed
	}
	if marker != nil && os.Remove(markerPath) == nil {
		removed++
	}
	// versions[0] is the newest and is always kept. An older version was
	// replaced at the time the next newer one was recorded.
	for i := 1; i < len(versions); i++ {
		replacedAt := versions[i-1].Time
		if now.Sub(replacedAt) > s.Retention {
			if os.Remove(versions[i].Path) == nil {
				res.Versions++
				removed++
			}
		}
	}
	return res, removed
}

func (s *Store) pruneJournals(now time.Time) int {
	removed := 0
	cutoff := now.Add(-90 * 24 * time.Hour).UTC().Format("2006-01-02")
	machines, _ := os.ReadDir(journalDir(s.Root))
	for _, m := range machines {
		files, _ := os.ReadDir(filepath.Join(journalDir(s.Root), m.Name()))
		for _, f := range files {
			if day, ok := strings.CutSuffix(f.Name(), ".jsonl"); ok && day < cutoff {
				if os.Remove(filepath.Join(journalDir(s.Root), m.Name(), f.Name())) == nil {
					removed++
				}
			}
		}
	}
	return removed
}

// WalkFiles calls fn for every ordinary file of the drive that is not on
// the skip list, with its path relative to the drive.
func (s *Store) WalkFiles(fn func(rel string, info os.FileInfo) error) error {
	return jfs.Walk(s.Root, func(p string, info os.FileInfo) error {
		name := info.Name()
		if info.IsDir() {
			if s.Skip.Dir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() || s.Skip.File(name) {
			return nil
		}
		rel, _ := filepath.Rel(s.Root, p)
		return fn(rel, info)
	})
}

// CatchUp records a version of every file changed since the given time
// whose newest version does not hold its current content. A zero time
// means every file. It returns how many versions were recorded.
func (s *Store) CatchUp(since time.Time) (int, error) {
	n := 0
	err := s.WalkFiles(func(rel string, info os.FileInfo) error {
		if !since.IsZero() && ChangeTime(info).Before(since) {
			return nil
		}
		made, err := s.Record(rel)
		if err != nil {
			return nil // one unreadable file must not stop the walk
		}
		if made {
			n++
		}
		return nil
	})
	if ferr := s.Journal.Flush(); err == nil {
		err = ferr
	}
	return n, err
}
