package record

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
)

// Handler receives settled changes. Paths are relative to the drive, with
// forward slashes. Calls may arrive on several goroutines at once, except
// Rename and CatchUp, which come from the goroutine reading the log.
type Handler interface {
	// Save: a file was written and has been quiet for a moment.
	Save(rel string)
	// Delete: a file is gone.
	Delete(rel string)
	// Rename: a file or folder moved inside the drive.
	Rename(oldRel, newRel string)
	// CatchUp: changes may have been missed; the caller should look for
	// them some other way (a walk of the changed files).
	CatchUp(reason string)
}

// GroupOptions tune the grouping of saves.
type GroupOptions struct {
	// Quiet is how long a file must be without entries before it is
	// recorded (default 2s; the helper uses 0.5s).
	Quiet time.Duration
	// LongQuiet replaces Quiet when the latest write committed a full
	// slice: the program is probably still writing (default 30s).
	LongQuiet time.Duration
	// MaxWait records a file that keeps changing this often (default 5m).
	MaxWait time.Duration
	// Workers is how many files are recorded side by side (default 4).
	Workers int
}

func (o *GroupOptions) fill() {
	if o.Quiet <= 0 {
		o.Quiet = 2 * time.Second
	}
	if o.LongQuiet <= 0 {
		o.LongQuiet = 30 * time.Second
	}
	if o.LongQuiet < o.Quiet {
		o.LongQuiet = o.Quiet
	}
	if o.MaxWait <= 0 {
		o.MaxWait = 5 * time.Minute
	}
	if o.Workers <= 0 {
		o.Workers = 4
	}
}

type pending struct {
	first   time.Time // first entry since the last recording
	last    time.Time // latest entry
	big     bool      // the latest write committed a full slice
	version int64     // the log version of the first entry
}

// group holds changes until their files are quiet, then hands each file to
// the handler once, never two workers on one file at the same time.
type group struct {
	opt  GroupOptions
	skip *history.Skip
	root string
	h    Handler
	now  func() time.Time

	mu       sync.Mutex
	pend     map[string]*pending
	jobs     chan string
	queued   map[string]bool
	busy     map[string]bool
	again    map[string]bool
	inflight map[string]int64 // file -> version of its first entry, while queued or busy
}

func newGroup(root string, skip *history.Skip, h Handler, opt GroupOptions) *group {
	opt.fill()
	return &group{
		opt: opt, skip: skip, root: root, h: h, now: time.Now,
		pend: map[string]*pending{}, jobs: make(chan string, 4096),
		queued: map[string]bool{}, busy: map[string]bool{}, again: map[string]bool{},
		inflight: map[string]int64{},
	}
}

// touch notes an entry about a file.
func (g *group) touch(rel string, big bool, version int64, at time.Time) {
	if rel == "" || rel == "." || g.skip.Ignored(rel) {
		return
	}
	g.mu.Lock()
	p := g.pend[rel]
	if p == nil {
		p = &pending{first: at, version: version}
		g.pend[rel] = p
	}
	p.last, p.big = at, big
	g.mu.Unlock()
}

// renamed applies a move of a file or folder. It tells the handler first,
// then makes sure the file under its new name is looked at.
func (g *group) renamed(from, to string, version int64, at time.Time) {
	oldSkipped, newSkipped := g.skip.Ignored(from), g.skip.Ignored(to)
	switch {
	case oldSkipped && newSkipped:
		return
	case newSkipped:
		// The real file became a backup or temporary name. If nothing
		// replaces it, settling finds it gone and reports a deletion.
		g.rekey(from, to, true)
		g.touch(from, false, version, at)
	case oldSkipped:
		g.touch(to, false, version, at) // save by replace
	default:
		g.rekey(from, to, false)
		g.h.Rename(from, to)
		g.touch(to, false, version, at)
	}
}

// rekey moves what is waiting under a path to its new path, or drops it.
func (g *group) rekey(from, to string, drop bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	prefix := from + "/"
	for p, v := range g.pend {
		switch {
		case p == from || strings.HasPrefix(p, prefix):
			delete(g.pend, p)
			if !drop {
				g.pend[path.Join(to, strings.TrimPrefix(p, from))] = v
			}
		}
	}
}

// due moves files whose time has come to the queue and returns them.
func (g *group) due(now time.Time) {
	var ready []string
	g.mu.Lock()
	for rel, p := range g.pend {
		quiet := g.opt.Quiet
		if p.big {
			quiet = g.opt.LongQuiet
		}
		if now.Sub(p.last) >= quiet || now.Sub(p.first) >= g.opt.MaxWait {
			delete(g.pend, rel)
			if !g.queued[rel] {
				g.queued[rel] = true
				if v, ok := g.inflight[rel]; !ok || p.version < v {
					g.inflight[rel] = p.version
				}
				ready = append(ready, rel)
			}
		}
	}
	g.mu.Unlock()
	for _, rel := range ready {
		g.jobs <- rel
	}
}

// work handles one file from the queue, never side by side with another
// worker handling the same file.
func (g *group) work(rel string) {
	g.mu.Lock()
	delete(g.queued, rel)
	if g.busy[rel] {
		g.again[rel] = true
		g.mu.Unlock()
		return
	}
	g.busy[rel] = true
	g.mu.Unlock()
	for {
		g.settle(rel)
		g.mu.Lock()
		if g.again[rel] {
			delete(g.again, rel)
			g.mu.Unlock()
			continue
		}
		delete(g.busy, rel)
		if !g.queued[rel] {
			delete(g.inflight, rel)
		}
		g.mu.Unlock()
		return
	}
}

// settle is called when a file's changes have calmed down.
func (g *group) settle(rel string) {
	if g.skip.Ignored(rel) {
		return
	}
	st, err := os.Lstat(filepath.Join(g.root, filepath.FromSlash(rel)))
	switch {
	case os.IsNotExist(err):
		g.h.Delete(rel)
	case err == nil && st.IsDir():
		// A folder: nothing to record.
	default:
		g.h.Save(rel)
	}
}

// safe returns the log version up to which everything has been handled:
// the one before the oldest change still waiting or being recorded, or
// read when nothing is.
func (g *group) safe(read int64) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	pos := read
	for _, p := range g.pend {
		if p.version-1 < pos {
			pos = p.version - 1
		}
	}
	for _, v := range g.inflight {
		if v-1 < pos {
			pos = v - 1
		}
	}
	if pos < 0 {
		pos = 0
	}
	return pos
}

// tickEvery is how often to look for files whose time has come.
func tickEvery(quiet time.Duration) time.Duration {
	d := quiet / 8
	if d < 10*time.Millisecond {
		d = 10 * time.Millisecond
	}
	if d > 250*time.Millisecond {
		d = 250 * time.Millisecond
	}
	return d
}
