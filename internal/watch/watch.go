// Package watch reports changes made to the drive through this machine's
// mount. It uses inotify, which only sees local activity, so each machine
// reports exactly its own saves.
package watch

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
)

// Handler receives settled changes. Paths are relative to the drive.
// Calls may arrive on several goroutines at once.
type Handler interface {
	// Save: a file was written and has been quiet for a moment.
	Save(rel string)
	// Delete: a file is gone.
	Delete(rel string)
	// Rename: a file or folder moved inside the drive.
	Rename(oldRel, newRel string)
	// Overflow: events were lost; the caller should run a catch-up.
	Overflow()
	// NewDir: a folder was created on, or moved onto, the drive through
	// this machine. Other machines cannot see that happen (inotify is
	// local), so the caller tells them.
	NewDir(rel string)
}

// Options tune the grouping of saves.
type Options struct {
	Quiet   time.Duration // record this long after the last close (default 2s; the helper uses 0.5s)
	MaxWait time.Duration // record a file that keeps changing this often (default 5m)
	Workers int           // concurrent handler calls (default 4)
}

const watchMask = syscall.IN_CLOSE_WRITE | syscall.IN_MODIFY | syscall.IN_CREATE |
	syscall.IN_DELETE | syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO |
	syscall.IN_ONLYDIR | syscall.IN_DONT_FOLLOW

// movePairWindow is how long a "moved from" waits for its "moved to".
const movePairWindow = 500 * time.Millisecond

type pending struct {
	first  time.Time // first change since the last recording
	closed time.Time // last close after writing
}

type move struct {
	path  string
	isDir bool
	at    time.Time
}

// Watcher watches one drive folder.
type Watcher struct {
	root string
	skip *history.Skip
	h    Handler
	opt  Options

	file *os.File
	fd   int
	ctx  context.Context // ends when watching should stop

	// How many folders are watched, readable without mu: Run holds mu
	// for as long as it takes to look at every folder at the start.
	watched atomic.Int64

	mu    sync.Mutex
	wds   map[int32]string
	paths map[string]int32
	pend  map[string]*pending
	moves map[uint32]move

	// A file is handed to the handler by one worker at a time, and waits
	// in the queue at most once. Without this, two notices about one file
	// could be handled side by side and record the same content twice.
	jobs   chan string
	queued map[string]bool // waiting in jobs
	busy   map[string]bool // being handled now
	again  map[string]bool // changed while being handled: handle once more
}

// New prepares a watcher. Call Run to start it.
func New(root string, skip *history.Skip, h Handler, opt Options) *Watcher {
	if opt.Quiet <= 0 {
		opt.Quiet = 2 * time.Second
	}
	if opt.MaxWait <= 0 {
		opt.MaxWait = 5 * time.Minute
	}
	if opt.Workers <= 0 {
		opt.Workers = 4
	}
	return &Watcher{
		root: filepath.Clean(root), skip: skip, h: h, opt: opt,
		wds: map[int32]string{}, paths: map[string]int32{},
		pend: map[string]*pending{}, moves: map[uint32]move{},
		jobs:   make(chan string, 4096),
		queued: map[string]bool{}, busy: map[string]bool{}, again: map[string]bool{},
	}
}

// Run watches until the context ends. It returns once watching has
// stopped. ready, if not nil, is closed when every folder is watched.
func (w *Watcher) Run(ctx context.Context, ready chan<- struct{}) error {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return err
	}
	w.fd = fd
	w.file = os.NewFile(uintptr(fd), "inotify")
	defer w.file.Close()
	w.ctx = ctx

	w.mu.Lock()
	w.addTree(w.root, false)
	w.mu.Unlock()
	if ctx.Err() != nil {
		return nil // asked to stop while still looking at the folders
	}
	if ready != nil {
		close(ready)
	}

	var wg sync.WaitGroup
	for i := 0; i < w.opt.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range w.jobs {
				w.work(path)
			}
		}()
	}

	readErr := make(chan error, 1)
	go func() { readErr <- w.readLoop() }()

	tick := time.NewTicker(tickEvery(w.opt.Quiet))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			w.file.Close()
			<-readErr
			close(w.jobs)
			wg.Wait()
			return nil
		case err := <-readErr:
			close(w.jobs)
			wg.Wait()
			return err
		case now := <-tick.C:
			w.fireDue(now)
		}
	}
}

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

func (w *Watcher) rel(abs string) string {
	r, err := filepath.Rel(w.root, abs)
	if err != nil {
		return ""
	}
	return r
}

// work handles one file from the queue, never side by side with another
// worker handling the same file.
func (w *Watcher) work(path string) {
	w.mu.Lock()
	delete(w.queued, path)
	if w.busy[path] {
		w.again[path] = true
		w.mu.Unlock()
		return
	}
	w.busy[path] = true
	w.mu.Unlock()
	for {
		w.settle(path)
		w.mu.Lock()
		if w.again[path] {
			delete(w.again, path)
			w.mu.Unlock()
			continue
		}
		delete(w.busy, path)
		w.mu.Unlock()
		return
	}
}

// settle is called when a file's changes have calmed down.
func (w *Watcher) settle(abs string) {
	rel := w.rel(abs)
	if rel == "" || w.skip.Ignored(rel) {
		return
	}
	if _, err := os.Lstat(abs); os.IsNotExist(err) {
		w.h.Delete(rel)
		return
	}
	w.h.Save(rel)
}

func (w *Watcher) fireDue(now time.Time) {
	var due []string
	var gone []move
	w.mu.Lock()
	for path, p := range w.pend {
		closedDue := !p.closed.IsZero() && now.Sub(p.closed) >= w.opt.Quiet
		waitDue := !p.first.IsZero() && now.Sub(p.first) >= w.opt.MaxWait
		if closedDue || waitDue {
			delete(w.pend, path)
			if !w.queued[path] {
				w.queued[path] = true
				due = append(due, path)
			}
		}
	}
	for cookie, m := range w.moves {
		if now.Sub(m.at) >= movePairWindow {
			gone = append(gone, m)
			delete(w.moves, cookie)
		}
	}
	for _, m := range gone { // moved out of the drive, or into a skipped place
		if m.isDir {
			w.dropTree(m.path)
		} else if !w.queued[m.path] {
			// A worker reports the deletion (see IN_DELETE in handle).
			w.queued[m.path] = true
			due = append(due, m.path)
		}
	}
	w.mu.Unlock()
	for _, path := range due {
		w.jobs <- path
	}
}

func (w *Watcher) readLoop() error {
	buf := make([]byte, 64*1024)
	for {
		n, err := w.file.Read(buf)
		if err != nil {
			if strings.Contains(err.Error(), "file already closed") || strings.Contains(err.Error(), "use of closed file") {
				return nil
			}
			return err
		}
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			wd := int32(binary.LittleEndian.Uint32(buf[off:]))
			mask := binary.LittleEndian.Uint32(buf[off+4:])
			cookie := binary.LittleEndian.Uint32(buf[off+8:])
			nameLen := int(binary.LittleEndian.Uint32(buf[off+12:]))
			name := ""
			if nameLen > 0 {
				raw := buf[off+syscall.SizeofInotifyEvent : off+syscall.SizeofInotifyEvent+nameLen]
				name = strings.TrimRight(string(raw), "\x00")
			}
			off += syscall.SizeofInotifyEvent + nameLen
			w.handle(wd, mask, cookie, name)
		}
	}
}

func (w *Watcher) handle(wd int32, mask, cookie uint32, name string) {
	if mask&syscall.IN_Q_OVERFLOW != 0 {
		w.h.Overflow()
		return
	}
	w.mu.Lock()
	dir, ok := w.wds[wd]
	if mask&syscall.IN_IGNORED != 0 {
		if ok {
			delete(w.wds, wd)
			if w.paths[dir] == wd {
				delete(w.paths, dir)
			}
			w.watched.Store(int64(len(w.paths)))
		}
		w.mu.Unlock()
		return
	}
	if !ok || name == "" {
		w.mu.Unlock()
		return
	}
	abs := filepath.Join(dir, name)
	isDir := mask&syscall.IN_ISDIR != 0
	now := time.Now()
	var renames [][2]string // handler calls made after unlocking
	var newDirs []string

	switch {
	case mask&syscall.IN_MOVED_FROM != 0:
		if !(isDir && w.skip.Dir(name)) {
			w.moves[cookie] = move{path: abs, isDir: isDir, at: now}
		}

	case mask&syscall.IN_MOVED_TO != 0:
		m, paired := w.moves[cookie]
		if paired {
			delete(w.moves, cookie)
		}
		switch {
		case isDir && w.skip.Dir(name):
			if paired {
				w.dropTree(m.path)
			}
		case isDir && paired:
			w.moveTree(m.path, abs)
			renames = append(renames, [2]string{w.rel(m.path), w.rel(abs)})
		case isDir:
			for _, f := range w.addTree(abs, true) {
				w.markClosed(f, now)
			}
			newDirs = append(newDirs, w.rel(abs))
		case !paired:
			if !w.skip.File(name) {
				w.markClosed(abs, now)
			}
		default: // a file moved inside the drive
			delete(w.pend, m.path)
			oldSkipped := w.skip.File(filepath.Base(m.path))
			newSkipped := w.skip.File(name)
			switch {
			case newSkipped && !oldSkipped:
				// The real file became a backup or temporary name. If
				// nothing replaces it, settle() reports it as deleted.
				w.markClosed(m.path, now)
			case newSkipped:
			case oldSkipped:
				w.markClosed(abs, now) // save by replace
			default:
				renames = append(renames, [2]string{w.rel(m.path), w.rel(abs)})
				w.markClosed(abs, now)
			}
		}

	case mask&syscall.IN_CREATE != 0:
		if isDir && !w.skip.Dir(name) {
			for _, f := range w.addTree(abs, true) {
				w.markClosed(f, now)
			}
			newDirs = append(newDirs, w.rel(abs))
		}

	case mask&syscall.IN_DELETE != 0:
		if !isDir && !w.skip.File(name) {
			// Left to a worker like a save: settle finds the file gone
			// and reports the deletion. Reporting it from here would
			// hold up every event behind it, and noting a deletion takes
			// most of a second on the drive: deleting a folder of 410
			// files kept new saves from being recorded for four minutes.
			w.markClosed(abs, now)
		}

	case mask&syscall.IN_CLOSE_WRITE != 0:
		if !isDir && !w.skip.File(name) {
			w.markClosed(abs, now)
		}

	case mask&syscall.IN_MODIFY != 0:
		if !isDir && !w.skip.File(name) {
			p := w.pend[abs]
			if p == nil {
				p = &pending{}
				w.pend[abs] = p
			}
			if p.first.IsZero() {
				p.first = now
			}
		}
	}
	w.mu.Unlock()

	for _, r := range renames {
		if r[0] != "" && r[1] != "" {
			w.h.Rename(r[0], r[1])
		}
	}
	for _, rel := range newDirs {
		if rel != "" {
			w.h.NewDir(rel)
		}
	}
}

// Ensure makes sure a folder, and every folder below it, is being
// watched. Folders created on another machine produce no local event, so
// the caller reports them here when it learns of them. It returns how
// many folders were newly watched, and the files already in those
// folders: any of them may have been saved through this machine before
// the watch existed, which the caller has to check for, because no event
// will ever arrive for them.
func (w *Watcher) Ensure(absDir string) (int, []string) {
	absDir = filepath.Clean(absDir)
	rel := w.rel(absDir)
	if rel == "" || strings.HasPrefix(rel, "..") || (rel != "." && w.skip.Ignored(rel)) {
		return 0, nil
	}
	if st, err := os.Lstat(absDir); err != nil || !st.IsDir() {
		return 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, nil // not running yet; Run watches everything at start
	}
	// If the folders above it are unknown too, start from the highest one.
	for absDir != w.root {
		parent := filepath.Dir(absDir)
		if _, ok := w.paths[parent]; ok {
			break
		}
		absDir = parent
	}
	known := make(map[string]bool, len(w.paths))
	for p := range w.paths {
		known[p] = true
	}
	var files []string
	for _, f := range w.addTree(absDir, true) {
		if !known[filepath.Dir(f)] { // only folders that had no watch until now
			files = append(files, f)
		}
	}
	added := 0
	for p := range w.paths {
		if !known[p] {
			added++
		}
	}
	return added, files
}

// Has reports whether a folder is being watched.
func (w *Watcher) Has(absDir string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.paths[filepath.Clean(absDir)]
	return ok
}

// Moved tells the watcher that a folder was renamed on another machine.
// The kernel keeps watching the same folders; only the remembered paths
// need to follow. It returns what Ensure returns for the new path.
func (w *Watcher) Moved(oldAbs, newAbs string) (int, []string) {
	oldAbs, newAbs = filepath.Clean(oldAbs), filepath.Clean(newAbs)
	for _, p := range []string{oldAbs, newAbs} {
		if rel := w.rel(p); rel == "" || rel == "." || strings.HasPrefix(rel, "..") {
			return 0, nil
		}
	}
	w.mu.Lock()
	if _, known := w.paths[oldAbs]; known {
		w.moveTree(oldAbs, newAbs)
	}
	w.mu.Unlock()
	return w.Ensure(newAbs)
}

// markClosed notes that a file was closed after writing. Caller holds mu.
func (w *Watcher) markClosed(abs string, now time.Time) {
	p := w.pend[abs]
	if p == nil {
		p = &pending{}
		w.pend[abs] = p
	}
	if p.first.IsZero() {
		p.first = now
	}
	p.closed = now
}

// addTree watches dir and every folder below it that is not skipped.
// With listFiles it also returns the files found, in no particular order,
// so that files created before the watch existed are not missed. It reads
// several folders at once (see jfs.Walk): one at a time, starting the
// helper on a drive with 350 folders took a minute. Caller holds mu.
func (w *Watcher) addTree(dir string, listFiles bool) []string {
	if dir != w.root && w.skip.Dir(filepath.Base(dir)) {
		return nil
	}
	if !w.addWatch(dir) {
		return nil
	}
	ctx := w.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var files []string
	var reg sync.Mutex // the maps, among the folders being entered side by side
	jfs.WalkWith(dir, jfs.WalkOptions{Ctx: ctx, Enter: func(p string) {
		// Asking the kernel to watch a folder makes it look the folder
		// up, which is a request or two to the index. Done one folder
		// after another, that alone took 290 s for 311 folders on a
		// connection with half a second per request, so it is done here,
		// where several folders are handled at once.
		n, err := syscall.InotifyAddWatch(w.fd, p, watchMask)
		if err != nil {
			return
		}
		reg.Lock()
		w.register(p, int32(n))
		reg.Unlock()
	}}, func(p string, info os.FileInfo) error {
		if info.IsDir() {
			if w.skip.Dir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if listFiles && info.Mode().IsRegular() && !w.skip.File(info.Name()) {
			files = append(files, p)
		}
		return nil
	})
	return files
}

// addWatch asks the kernel to watch one folder and remembers it. It asks
// even for a folder we believe is watched: the kernel answers with the
// same number if so. A different number means the folder was replaced by
// a new one of the same name, and a number we hold under another path
// means the folder was renamed; both can happen on another machine
// without any event reaching this one. Caller holds mu, or is the only
// one touching the maps.
func (w *Watcher) addWatch(p string) bool {
	n, err := syscall.InotifyAddWatch(w.fd, p, watchMask)
	if err != nil {
		return false
	}
	w.register(p, int32(n))
	return true
}

// register remembers the number the kernel gave for a watched folder.
func (w *Watcher) register(p string, wd int32) {
	if old, have := w.paths[p]; have && old != wd && w.wds[old] == p {
		delete(w.wds, old)
	}
	if prev, have := w.wds[wd]; have && prev != p && w.paths[prev] == wd {
		delete(w.paths, prev)
	}
	w.wds[wd] = p
	w.paths[p] = wd
	w.watched.Store(int64(len(w.paths)))
}

// moveTree updates the remembered paths after a folder was renamed.
// The kernel keeps watching the same folders. Caller holds mu.
func (w *Watcher) moveTree(from, to string) {
	prefix := from + string(filepath.Separator)
	for path, wd := range w.paths {
		var np string
		switch {
		case path == from:
			np = to
		case strings.HasPrefix(path, prefix):
			np = filepath.Join(to, path[len(prefix):])
		default:
			continue
		}
		delete(w.paths, path)
		w.paths[np] = wd
		w.wds[wd] = np
	}
	for path, p := range w.pend {
		if strings.HasPrefix(path, prefix) {
			delete(w.pend, path)
			w.pend[filepath.Join(to, path[len(prefix):])] = p
		}
	}
}

// dropTree stops watching a folder that left the drive. Caller holds mu.
func (w *Watcher) dropTree(dir string) {
	prefix := dir + string(filepath.Separator)
	for path, wd := range w.paths {
		if path == dir || strings.HasPrefix(path, prefix) {
			syscall.InotifyRmWatch(w.fd, uint32(wd))
			delete(w.paths, path)
			delete(w.wds, wd)
		}
	}
	w.watched.Store(int64(len(w.paths)))
}

// Watched returns how many folders are being watched. It does not wait
// for anything, so it can be asked while Run is still starting.
func (w *Watcher) Watched() int { return int(w.watched.Load()) }
