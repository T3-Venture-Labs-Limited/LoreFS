// Package helper is the background service that runs beside the mount.
// It records versions of files saved on this machine, keeps small files
// and pinned folders in the local cache, removes old versions once a
// day, and tells the user when the drive stops or resumes working.
package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/assets"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/index"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/mount"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/peers"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/watch"
)

// Heartbeat is what the helper writes for `lorefs status` to read.
type Heartbeat struct {
	PID         int       `json:"pid"`
	Time        time.Time `json:"time"`
	Started     time.Time `json:"started"`
	LastVersion time.Time `json:"last_version"`
	Watched     int       `json:"watched_folders"`
	// Ready is false while the helper is still looking at every folder
	// after starting. Saves made meanwhile are recorded once it is ready.
	Ready     bool      `json:"ready"`
	LastWalk  time.Time `json:"last_walk"`
	LastPrune time.Time `json:"last_prune"`
	Healthy   bool      `json:"healthy"`
}

// HeartbeatFile is where the heartbeat is written.
func HeartbeatFile() string { return filepath.Join(config.StateDir(), "helper.json") }

// ReadHeartbeat returns the last heartbeat and whether it is fresh
// (written within the last 90 seconds by a process that still exists).
func ReadHeartbeat() (Heartbeat, bool) {
	var hb Heartbeat
	data, err := os.ReadFile(HeartbeatFile())
	if err != nil || json.Unmarshal(data, &hb) != nil {
		return hb, false
	}
	alive := hb.PID > 0 && syscall.Kill(hb.PID, 0) == nil
	return hb, alive && time.Since(hb.Time) < 90*time.Second
}

// Timings are the helper's intervals; tests and the local integration
// test shorten them through environment variables.
type Timings struct {
	Quiet, MaxWait time.Duration
	JournalFlush   time.Duration
	Follow         time.Duration // read other machines' journals
	LateIdle       time.Duration // other machines quiet this long before late checks
	LateMax        time.Duration // a late check waits at most this long
	Walk           time.Duration // catch-up walk
	Health         time.Duration
	DownAfter      time.Duration // unhealthy this long before notifying
	StartProgress  time.Duration // say how far the start is, this often
}

func envDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// DefaultTimings returns the production intervals.
func DefaultTimings() Timings {
	return Timings{
		// How long a file must be left alone after a save before its
		// version is taken. Short, because the version is a copy of the
		// file as it is then: when another computer saves the same file
		// within this time, the first save never gets a version. With 2 s,
		// two people saving one file 2 s apart lost the first save from
		// history on the real drive.
		Quiet:        envDuration("LOREFS_QUIET", 500*time.Millisecond),
		MaxWait:      envDuration("LOREFS_MAX_WAIT", 5*time.Minute),
		JournalFlush: envDuration("LOREFS_JOURNAL_FLUSH", 2*time.Second),
		Follow:       envDuration("LOREFS_FOLLOW", 10*time.Second),
		LateIdle:     envDuration("LOREFS_LATE_IDLE", 30*time.Second),
		LateMax:      envDuration("LOREFS_LATE_MAX", 10*time.Minute),
		Walk:         envDuration("LOREFS_WALK", 6*time.Hour),
		Health:       envDuration("LOREFS_HEALTH", time.Minute),
		DownAfter:    envDuration("LOREFS_DOWN_AFTER", 2*time.Minute),

		StartProgress: envDuration("LOREFS_START_PROGRESS", 15*time.Second),
	}
}

type helper struct {
	cfg   config.Config
	t     Timings
	store *history.Store
	jfs   jfs.Exec
	log   *log.Logger

	mu          sync.Mutex
	started     time.Time
	lastVersion time.Time
	lastWalk    time.Time
	lastPrune   time.Time
	healthy     bool
	ready       bool
	walkNow     chan struct{}
	watcher     *watch.Watcher
}

// Program is the version of lorefs that is running, set by the command
// before Run. The helper tells the other computers on the drive.
var Program struct {
	Version    string
	CommitTime int64
}

// Run starts the helper and returns when the context ends or the drive
// is no longer mounted.
func Run(ctx context.Context, cfg config.Config) error {
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if err := waitMounted(ctx, cfg.Folder, logger); err != nil {
		return err
	}
	skip, err := history.LoadSkip(config.SkipFile())
	if err != nil {
		logger.Printf("skip list: %v (using defaults)", err)
	}
	j := jfs.NewExec()
	store := history.New(cfg.Folder, cfg.Machine, j)
	store.Skip = skip
	if cfg.RetentionDays > 0 {
		store.Retention = time.Duration(cfg.RetentionDays) * 24 * time.Hour
	}
	h := &helper{cfg: cfg, t: DefaultTimings(), store: store, jfs: j, log: logger,
		started: time.Now(), healthy: true, walkNow: make(chan struct{}, 1)}
	h.lastWalk = h.loadLastWalk()
	h.writeHeartbeat() // alive and starting, before anything that waits for the index

	if err := EnsureDrive(cfg); err != nil {
		logger.Printf("preparing the drive: %v", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errc := make(chan error, 2)

	// Recording a version is a handful of requests to the index and no
	// work for this computer, so many can run side by side. With 4, a
	// 2,000-file import was still being recorded 13 minutes after the
	// copy had finished.
	h.watcher = watch.New(cfg.Folder, skip, h, watch.Options{Quiet: h.t.Quiet, MaxWait: h.t.MaxWait, Workers: 12})
	ready := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := h.watcher.Run(ctx, ready); err != nil {
			errc <- fmt.Errorf("watching the drive: %w", err)
		}
	}()
	// Looking at every folder takes a few seconds on a nearby index and
	// minutes on a slow connection. Say that the helper is alive and how
	// far it is, and stop at once when asked to.
	h.writeHeartbeat()
	progress := time.NewTicker(h.t.StartProgress)
	for waiting := true; waiting; {
		select {
		case <-ready:
			waiting = false
		case <-ctx.Done():
			waiting = false
		case err := <-errc:
			progress.Stop()
			cancel()
			wg.Wait()
			return err
		case <-progress.C:
			logger.Printf("still starting: %d folders looked at so far (this is slow when the connection to the index is slow)", h.watcher.Watched())
			h.writeHeartbeat()
		}
	}
	progress.Stop()
	if ctx.Err() != nil {
		wg.Wait()
		return nil // stopped before it was ready
	}
	h.mu.Lock()
	h.ready = true
	h.mu.Unlock()
	logger.Printf("recording versions for %s as %q, watching %d folders", cfg.Folder, cfg.Machine, h.watcher.Watched())

	loops := []func(context.Context){h.flushLoop, h.heartbeatLoop, h.walkLoop, h.followLoop, h.pruneLoop, h.noteLoop}
	for _, loop := range loops {
		wg.Add(1)
		go func(f func(context.Context)) { defer wg.Done(); f(ctx) }(loop)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := h.healthLoop(ctx); err != nil {
			errc <- err
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
		cancel()
	}
	wg.Wait()
	store.Journal.Flush()
	h.writeHeartbeat()
	return runErr
}

func waitMounted(ctx context.Context, folder string, logger *log.Logger) error {
	for i := 0; ; i++ {
		if jfs.IsMounted(folder) {
			return nil
		}
		if i%30 == 0 {
			logger.Printf("waiting for the drive to be mounted at %s", folder)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// EnsureDrive creates the drive's bookkeeping folder and its rules file
// for agents if they do not exist yet. It never overwrites either.
func EnsureDrive(cfg config.Config) error {
	metaDir := filepath.Join(cfg.Folder, history.MetaDir)
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return err
	}
	metaFile := filepath.Join(metaDir, "meta.json")
	if _, err := os.Stat(metaFile); os.IsNotExist(err) {
		body, _ := json.MarshalIndent(map[string]any{
			"layout": 1, "owner_uid": os.Getuid(), "created": time.Now().UTC().Format(time.RFC3339),
			"created_by": cfg.Machine,
		}, "", "  ")
		if err := writeExclusive(metaFile, append(body, '\n')); err != nil {
			return err
		}
	}
	rules := filepath.Join(cfg.Folder, "AGENTS.md")
	if _, err := os.Stat(rules); os.IsNotExist(err) {
		return writeExclusive(rules, []byte(assets.AgentsRules))
	}
	return nil
}

// writeExclusive creates a file only if it does not exist, so that two
// machines starting at once cannot overwrite each other.
func writeExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// --- watch.Handler ---

func (h *helper) Save(rel string) {
	if h.store.NoteSave(rel) {
		return // a file type that gets no versions; it is only logged
	}
	made, err := h.store.Record(rel)
	if err != nil {
		h.log.Printf("could not record %s: %v", rel, err)
		return
	}
	if made {
		h.mu.Lock()
		h.lastVersion = time.Now()
		h.mu.Unlock()
		h.log.Printf("recorded a version of %s", rel)
	}
}

func (h *helper) Delete(rel string) {
	if err := h.store.MarkDeleted(rel); err != nil {
		h.log.Printf("could not note the deletion of %s: %v", rel, err)
	}
}

func (h *helper) Rename(oldRel, newRel string) {
	if err := h.store.Rename(oldRel, newRel); err != nil {
		h.log.Printf("could not move the history of %s to %s: %v", oldRel, newRel, err)
	}
	// Tell the other machines about a renamed folder (see NewDir).
	if st, err := os.Lstat(filepath.Join(h.cfg.Folder, newRel)); err == nil && st.IsDir() {
		h.store.Journal.Append(history.Event{Time: h.store.Now().UTC(), Op: history.OpMoveFolder,
			Path: filepath.ToSlash(oldRel), To: filepath.ToSlash(newRel)})
	}
}

// NewDir writes down a folder created through this machine. The other
// machines cannot see it being created, and without this note they would
// not notice files saved into it until their next catch-up.
func (h *helper) NewDir(rel string) {
	h.store.Journal.Append(history.Event{Time: h.store.Now().UTC(), Op: history.OpNewFolder, Path: filepath.ToSlash(rel)})
}

// watchFolder starts watching a folder this machine did not know about.
// It returns the files already in it (see watch.Watcher.Ensure).
func (h *helper) watchFolder(abs string) []string {
	if h.watcher == nil {
		return nil
	}
	n, files := h.watcher.Ensure(abs)
	if n > 0 {
		rel, _ := filepath.Rel(h.cfg.Folder, abs)
		h.log.Printf("now watching %d new folder(s) at %s", n, rel)
	}
	return files
}

func (h *helper) Overflow() {
	h.log.Printf("too many changes at once; scheduling a catch-up")
	select {
	case h.walkNow <- struct{}{}:
	default:
	}
}

// --- loops ---

func every(ctx context.Context, d time.Duration, f func()) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f()
		}
	}
}

func (h *helper) flushLoop(ctx context.Context) {
	every(ctx, h.t.JournalFlush, func() {
		if err := h.store.Journal.Flush(); err != nil {
			h.log.Printf("writing the journal: %v", err)
		}
	})
}

// noteLoop tells the other computers which version of lorefs this one
// runs: at start, which is also right after an update, and twice a day
// so that a computer that is no longer used can be told from one that is.
func (h *helper) noteLoop(ctx context.Context) {
	if Program.Version == "" {
		return
	}
	write := func() {
		err := peers.Write(h.cfg.Folder, peers.Note{Machine: h.cfg.Machine, Version: Program.Version,
			CommitTime: Program.CommitTime, JuiceFS: h.jfs.Version(), Seen: time.Now().UTC()})
		if err != nil {
			h.log.Printf("could not note this computer's version on the drive: %v", err)
		}
	}
	write()
	every(ctx, 12*time.Hour, write)
}

func (h *helper) heartbeatLoop(ctx context.Context) {
	h.writeHeartbeat()
	every(ctx, 20*time.Second, h.writeHeartbeat)
}

func (h *helper) writeHeartbeat() {
	h.mu.Lock()
	hb := Heartbeat{PID: os.Getpid(), Time: time.Now(), Started: h.started, LastVersion: h.lastVersion,
		LastWalk: h.lastWalk, LastPrune: h.lastPrune, Healthy: h.healthy, Ready: h.ready}
	h.mu.Unlock()
	if h.watcher != nil {
		hb.Watched = h.watcher.Watched()
	}
	data, _ := json.Marshal(hb)
	config.WritePrivate(HeartbeatFile(), data)
}

func (h *helper) stateFile() string { return filepath.Join(config.StateDir(), "state.json") }

func (h *helper) loadLastWalk() time.Time {
	var st struct {
		LastWalk time.Time `json:"last_walk"`
		Folder   string    `json:"folder"`
	}
	data, err := os.ReadFile(h.stateFile())
	if err != nil || json.Unmarshal(data, &st) != nil || st.Folder != h.cfg.Folder {
		return time.Time{}
	}
	return st.LastWalk
}

func (h *helper) saveLastWalk(t time.Time) {
	data, _ := json.Marshal(map[string]any{"last_walk": t, "folder": h.cfg.Folder})
	config.WritePrivate(h.stateFile(), data)
}

// walkLoop runs the catch-up walk at start, on request and periodically.
func (h *helper) walkLoop(ctx context.Context) {
	h.walk(ctx, true)
	t := time.NewTicker(h.t.Walk)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-h.walkNow:
		}
		h.walk(ctx, false)
	}
}

// walk visits every file changed since the previous walk (with a margin),
// records a version where the newest one is out of date, and warms small
// files and files in pinned folders.
func (h *helper) walk(ctx context.Context, atStart bool) {
	h.mu.Lock()
	since := h.lastWalk
	h.mu.Unlock()
	// The first walk on this computer. Files that were on the drive
	// before it joined were saved through other computers, which recorded
	// them; asking about each one again costs a folder read per file (70
	// minutes for 2,100 files at half a second per request). So only
	// files changed since this computer joined are looked at for
	// versions. Small files are still all brought into the cache.
	first := since.IsZero()
	if first {
		since = config.JoinedAt()
	}
	if !since.IsZero() {
		since = since.Add(-10 * time.Minute)
	}
	begun := time.Now()
	// Make sure every folder is watched, in case a note from another
	// machine about a new or renamed folder was missed. At start the
	// watcher has only just looked at every folder.
	if !atStart {
		h.watchFolder(h.cfg.Folder)
	}
	pins, _ := config.Pins()
	small := int64(h.cfg.SmallFileKB) * 1024
	type changedFile struct {
		rel string
		at  time.Time
	}
	var warm []string
	var check []changedFile
	err := h.store.WalkFiles(func(rel string, info os.FileInfo) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// By change time, not by the file's date: a file copied onto the
		// drive keeps its old date.
		changed := since.IsZero() || !history.ChangeTime(info).Before(since)
		if !changed && !first {
			return nil
		}
		if changed {
			check = append(check, changedFile{rel, history.ChangeTime(info)})
		}
		if info.Size() <= small || inPins(pins, rel) {
			warm = append(warm, filepath.Join(h.cfg.Folder, rel))
		}
		return nil
	})
	if err != nil {
		if ctx.Err() == nil {
			h.log.Printf("catch-up walk: %v", err)
		}
		return
	}
	// Asking whether a file needs a version is a folder read on the
	// index, so the files are asked about side by side, not one by one,
	// and the most recently changed first: those are the ones saved while
	// the helper was not watching.
	sort.Slice(check, func(a, b int) bool { return check[a].at.After(check[b].at) })
	var recorded atomic.Int64
	var cwg sync.WaitGroup
	jobs := make(chan string)
	for i := 0; i < 8; i++ {
		cwg.Add(1)
		go func() {
			defer cwg.Done()
			for rel := range jobs {
				if ctx.Err() != nil {
					continue
				}
				if made, err := h.store.Record(rel); err == nil && made {
					recorded.Add(1)
				}
			}
		}()
	}
	for _, c := range check {
		jobs <- c.rel
	}
	close(jobs)
	cwg.Wait()
	if ctx.Err() != nil {
		return // stopped half-way: the next start looks again
	}
	h.store.Journal.Flush()
	h.warm(ctx, warm)
	h.mu.Lock()
	h.lastWalk = begun
	if recorded.Load() > 0 {
		h.lastVersion = time.Now()
	}
	h.mu.Unlock()
	h.saveLastWalk(begun)
	if recorded.Load() > 0 || len(warm) > 0 {
		h.log.Printf("catch-up: recorded %d version(s), cached %d file(s)", recorded.Load(), len(warm))
	}
}

func inPins(pins []string, rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range pins {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

func (h *helper) warm(ctx context.Context, paths []string) {
	const batch = 500
	for len(paths) > 0 {
		n := len(paths)
		if n > batch {
			n = batch
		}
		if err := h.jfs.WarmContext(ctx, paths[:n]); err != nil {
			if ctx.Err() == nil {
				h.log.Printf("caching files: %v", err)
			}
			return
		}
		paths = paths[n:]
	}
}

// followLoop reads what other machines did. It starts watching folders
// they created or renamed, and brings small files and files in pinned
// folders into the local cache.
//
// Late checks: a file found in a folder this machine was not yet watching
// may have been saved here in the few seconds before the watch began, in
// which case nobody has recorded it. More often the other machine saved
// it and will record it itself. So such files are set aside until the
// other machines have been quiet for a while, and then checked: whatever
// still has no version of its current content is recorded here.
func (h *helper) followLoop(ctx context.Context) {
	offsets := map[string]int64{}
	// Start at the current end of each journal: the walk covers the past.
	for _, m := range history.OtherMachines(h.cfg.Folder, h.store.Machine) {
		for _, day := range []time.Time{time.Now().Add(-24 * time.Hour), time.Now()} {
			f := history.JournalFile(h.cfg.Folder, m, day)
			if st, err := os.Stat(f); err == nil {
				offsets[f] = st.Size()
			}
		}
	}
	late := map[string]time.Time{} // file -> when it was set aside
	setAside := func(files []string) {
		for _, f := range files {
			if _, ok := late[f]; !ok {
				late[f] = time.Now()
			}
		}
	}
	lastRemote := time.Now()
	every(ctx, h.t.Follow, func() {
		defer func() {
			now := time.Now()
			idle := now.Sub(lastRemote) >= h.t.LateIdle
			recorded := 0
			for abs, since := range late {
				if !idle && now.Sub(since) < h.t.LateMax {
					continue
				}
				delete(late, abs)
				rel, err := filepath.Rel(h.cfg.Folder, abs)
				if err != nil {
					continue
				}
				if made, err := h.store.Record(rel); err == nil && made {
					recorded++
				}
			}
			if recorded > 0 {
				h.mu.Lock()
				h.lastVersion = now
				h.mu.Unlock()
				h.log.Printf("recorded %d file(s) saved here before their folder was being watched", recorded)
			}
		}()
		pins, _ := config.Pins()
		small := int64(h.cfg.SmallFileKB) * 1024
		seen := map[string]bool{}
		var warm []string
		for _, m := range history.OtherMachines(h.cfg.Folder, h.store.Machine) {
			for _, day := range []time.Time{time.Now().Add(-24 * time.Hour), time.Now()} {
				f := history.JournalFile(h.cfg.Folder, m, day)
				evs, off, err := history.ReadJournalFile(f, offsets[f])
				if err != nil {
					continue
				}
				offsets[f] = off
				if len(evs) > 0 {
					lastRemote = time.Now()
				}
				for _, e := range evs {
					rel := filepath.FromSlash(e.Path)
					abs := filepath.Join(h.cfg.Folder, rel)
					switch e.Op {
					case history.OpNewFolder:
						setAside(h.watchFolder(abs))
						continue
					case history.OpMoveFolder:
						if h.watcher != nil && e.To != "" {
							_, files := h.watcher.Moved(abs, filepath.Join(h.cfg.Folder, filepath.FromSlash(e.To)))
							setAside(files)
						}
						continue
					case "save", "restore":
						// A file in a folder nobody told us about.
						if h.watcher != nil && !h.watcher.Has(filepath.Dir(abs)) {
							setAside(h.watchFolder(filepath.Dir(abs)))
						}
					default:
						continue
					}
					if seen[abs] {
						continue
					}
					seen[abs] = true
					st, err := os.Lstat(abs)
					if err != nil || !st.Mode().IsRegular() {
						continue
					}
					if st.Size() <= small || inPins(pins, rel) {
						warm = append(warm, abs)
					}
				}
			}
		}
		h.warm(ctx, warm)
	})
}

// pruneLoop removes old versions once a day, at a random time so that
// machines rarely try at the same moment; a lock on the drive makes sure
// only one of them does the work.
func (h *helper) pruneLoop(ctx context.Context) {
	first := envDuration("LOREFS_PRUNE_FIRST", time.Duration(5+rand.Intn(55))*time.Minute)
	select {
	case <-ctx.Done():
		return
	case <-time.After(first):
	}
	h.prune(ctx)
	every(ctx, envDuration("LOREFS_PRUNE_EVERY", 24*time.Hour), func() { h.prune(ctx) })
}

func (h *helper) prune(ctx context.Context) {
	res, ran, err := PruneLocked(ctx, h.store)
	if err != nil {
		if ctx.Err() == nil {
			h.log.Printf("removing old versions: %v", err)
		}
		return
	}
	if !ran {
		h.log.Printf("another machine is removing old versions; skipping this run")
		return
	}
	h.mu.Lock()
	h.lastPrune = time.Now()
	h.mu.Unlock()
	h.log.Printf("removed %d old version(s), %d old journal file(s); noticed %d vanished file(s)", res.Versions, res.Journals, res.Adopted)
}

// PruneLocked runs Prune while holding the drive-wide lock. ran is false
// when another machine holds the lock.
func PruneLocked(ctx context.Context, store *history.Store) (res history.PruneResult, ran bool, err error) {
	lockPath := filepath.Join(store.Root, history.MetaDir, "prune.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return res, false, err
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return res, false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return res, false, nil
		}
		return res, false, err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	res, err = store.PruneContext(ctx)
	return res, true, err
}

// healthLoop checks the drive once a minute and notifies the user once
// when it stops working and once when it works again. It returns an
// error when the drive is no longer mounted, so that the service ends.
func (h *helper) healthLoop(ctx context.Context) error {
	var failingSince time.Time
	notified := false
	var noticeID uint32 // the "unavailable" notification, so recovery can replace it
	unmounted := 0
	t := time.NewTicker(h.t.Health)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if !jfs.IsMounted(h.cfg.Folder) {
			unmounted++
			if unmounted >= 3 {
				return errors.New("the drive is no longer mounted")
			}
			continue
		}
		unmounted = 0
		ok := h.indexReachable()
		h.mu.Lock()
		h.healthy = ok
		h.mu.Unlock()
		switch {
		case !ok && failingSince.IsZero():
			failingSince = time.Now()
		case !ok && !notified && time.Since(failingSince) >= h.t.DownAfter:
			notified = true
			h.log.Printf("the drive is unavailable")
			noticeID = Notify("LoreFS is unavailable", "Files on the shared drive cannot be opened or saved right now. Check your internet connection, then run: lorefs status", true, 0)
		case ok && notified:
			notified = false
			failingSince = time.Time{}
			h.log.Printf("the drive is available again")
			// Replace the warning rather than leave it on screen beside the
			// good news: the warning does not expire by itself.
			Notify("LoreFS is available again", "The shared drive is working.", false, noticeID)
			noticeID = 0
		case ok:
			failingSince = time.Time{}
		}
	}
}

func (h *helper) indexReachable() bool {
	if h.cfg.IsLocal() {
		_, err := os.Stat(h.cfg.IndexPath)
		return err == nil
	}
	target, err := mount.Target(h.cfg)
	if err != nil {
		return false
	}
	conn, err := index.Dial(target, 8*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_, err = conn.Ping()
	return err == nil
}

// Notify sends a desktop notification and returns its id (0 if unknown).
// An urgent notification stays on screen until dismissed or replaced; an
// ordinary one disappears after ten seconds. A non-zero replace puts the
// new notification in place of an earlier one. It does nothing if the
// desktop cannot be reached.
func Notify(title, body string, urgent bool, replace uint32) uint32 {
	if os.Getenv("LOREFS_NO_NOTIFY") != "" {
		if f := os.Getenv("LOREFS_NOTIFY_LOG"); f != "" {
			if fh, err := os.OpenFile(f, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
				fmt.Fprintf(fh, "%s\t%s\n", title, body)
				fh.Close()
			}
		}
		return 0
	}
	args := []string{"--app-name=LoreFS", "--icon=folder-remote", "--print-id"}
	if urgent {
		args = append(args, "--urgency=critical")
	} else {
		args = append(args, "--expire-time=10000")
	}
	if replace > 0 {
		args = append(args, fmt.Sprintf("--replace-id=%d", replace))
	}
	out, err := exec.Command("notify-send", append(args, title, body)...).Output()
	if err != nil {
		return 0
	}
	id, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 32)
	return uint32(id)
}
