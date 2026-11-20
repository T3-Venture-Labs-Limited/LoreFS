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
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/record"
)

// Heartbeat is what the helper writes for `lorefs status` to read.
type Heartbeat struct {
	PID         int       `json:"pid"`
	Time        time.Time `json:"time"`
	Started     time.Time `json:"started"`
	LastVersion time.Time `json:"last_version"`
	// LogVersion is the last change-log entry read, and LogEntries how many
	// were read since the helper started. ChangeLog says whether the
	// drive's change log is on; when it is off, versions come from the
	// periodic walk only.
	LogVersion int64 `json:"log_version"`
	LogEntries int64 `json:"log_entries"`
	ChangeLog  bool  `json:"change_log"`
	// Ready is false while the helper is still starting. Saves made
	// meanwhile are recorded once it is ready.
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
	LongQuiet      time.Duration // quiet time after a write that filled a slice
	JournalFlush   time.Duration
	Follow         time.Duration // read other machines' journals
	Walk           time.Duration // catch-up walk
	WalkNoLog      time.Duration // catch-up walk when the drive has no change log
	LogCheck       time.Duration // look again at the drive's change-log setting
	Health         time.Duration
	DownAfter      time.Duration // unhealthy this long before notifying
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
		// A write that fills a 64 MiB slice may be followed by more. The
		// recorder does not wait longer for it by default: a file that is
		// still being written is not recorded anyway (its size on the drive
		// is ahead of what has been committed), and one that is complete
		// is recorded at once. The option stays for slow storage.
		LongQuiet: envDuration("LOREFS_LONG_QUIET", 500*time.Millisecond),
		Walk:      envDuration("LOREFS_WALK", 6*time.Hour),
		WalkNoLog: envDuration("LOREFS_WALK_NO_LOG", 10*time.Minute),
		LogCheck:  envDuration("LOREFS_LOG_CHECK", 5*time.Minute),
		Health:    envDuration("LOREFS_HEALTH", time.Minute),
		DownAfter: envDuration("LOREFS_DOWN_AFTER", 2*time.Minute),
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
	recorder    *record.Recorder
	changeLog   bool
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

	// Writing the rules file and the bookkeeping folders is a few dozen
	// requests to the index: half a minute on a slow connection. Nothing
	// waits for it, and the helper stops at once when asked.
	go func() {
		if err := EnsureDrive(cfg); err != nil {
			logger.Printf("preparing the drive: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errc := make(chan error, 2)

	// Recording a version is a handful of requests to the index and no
	// work for this computer, so many can run side by side. With 4, a
	// 2,000-file import was still being recorded 13 minutes after the
	// copy had finished.
	h.mu.Lock()
	h.ready = true
	h.mu.Unlock()
	h.writeHeartbeat()
	wg.Add(1)
	go func() { defer wg.Done(); h.recordLoop(ctx) }()
	logger.Printf("recording versions for %s as %q", cfg.Folder, cfg.Machine)

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
}

// CatchUp is called when the change log cannot be trusted to hold
// everything: some entries were no longer there, or the reader had to
// start again without a position. A walk of the changed files covers it.
func (h *helper) CatchUp(reason string) {
	h.log.Printf("%s; scheduling a catch-up", reason)
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
		LastWalk: h.lastWalk, LastPrune: h.lastPrune, Healthy: h.healthy, Ready: h.ready, ChangeLog: h.changeLog}
	rc := h.recorder
	h.mu.Unlock()
	if rc != nil {
		hb.LogVersion, hb.LogEntries = rc.Version(), rc.Entries()
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
	for {
		every := h.t.Walk
		h.mu.Lock()
		if !h.changeLog {
			every = h.t.WalkNoLog // without the log, the walk is the only recorder
		}
		h.mu.Unlock()
		timer := time.NewTimer(every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-h.walkNow:
			timer.Stop()
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

// followLoop reads what other machines did and brings small files and
// files in pinned folders into the local cache.
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
	every(ctx, h.t.Follow, func() {
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
				for _, e := range evs {
					if e.Op != "save" && e.Op != "restore" {
						continue
					}
					rel := filepath.FromSlash(e.Path)
					abs := filepath.Join(h.cfg.Folder, rel)
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

// driveStatus asks juicefs about the drive. It works with every index.
func (h *helper) driveStatus(ctx context.Context) (jfs.DriveStatus, error) {
	var pw string
	if !h.cfg.IsLocal() {
		var err error
		if pw, err = config.Password(); err != nil {
			return jfs.DriveStatus{}, err
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return h.jfs.Status(cctx, mount.IndexURL(h.cfg), pw)
}

// recordLoop follows the drive's change log for as long as the log is on.
// When it is off, nothing is followed and the periodic walk records
// versions; the setting is looked at again from time to time.
func (h *helper) recordLoop(ctx context.Context) {
	var cancelRec context.CancelFunc
	var recDone chan struct{}
	stopRec := func() {
		if cancelRec != nil {
			cancelRec()
			<-recDone
			cancelRec, recDone = nil, nil
			h.mu.Lock()
			h.recorder = nil
			h.mu.Unlock()
		}
	}
	defer stopRec()
	asked := false
	for {
		on := true
		if st, err := h.driveStatus(ctx); err == nil {
			on = st.ChangeLog
		} else if ctx.Err() == nil {
			h.log.Printf("could not read the drive's change-log setting (%v); assuming it is on", err)
		}
		h.mu.Lock()
		changed := on != h.changeLog || !asked
		h.changeLog = on
		h.mu.Unlock()
		asked = true
		switch {
		case on && cancelRec == nil:
			h.log.Printf("following the drive's change log")
			rec := h.newRecorder()
			rctx, cancel := context.WithCancel(ctx)
			cancelRec, recDone = cancel, make(chan struct{})
			h.mu.Lock()
			h.recorder = rec
			h.mu.Unlock()
			go func(done chan struct{}) {
				defer close(done)
				if err := rec.Run(rctx, nil); err != nil {
					h.log.Printf("following the change log: %v", err)
				}
			}(recDone)
		case !on && cancelRec != nil:
			stopRec()
		}
		if changed && !on {
			h.log.Printf("the drive's change log is off: versions are recorded by a walk every %s. Turn it on with: lorefs admin changelog on", h.t.WalkNoLog)
			h.CatchUp("the change log was off")
		}
		h.writeHeartbeat()
		select {
		case <-ctx.Done():
			return
		case <-time.After(h.t.LogCheck):
		}
	}
}

func (h *helper) newRecorder() *record.Recorder {
	env := []string{}
	if !h.cfg.IsLocal() {
		if pw, err := config.Password(); err == nil {
			env = append(env, "META_PASSWORD="+pw)
		}
	}
	return record.New(record.Options{
		Folder: h.cfg.Folder,
		Source: record.ExecSource{Bin: jfs.Binary(), Meta: mount.IndexURL(h.cfg), Env: env},
		Session: func() (int64, error) {
			data, err := os.ReadFile(filepath.Join(h.cfg.Folder, ".config"))
			if err != nil {
				return 0, err
			}
			if n, ok := record.ParseSession(data); ok {
				return n, nil
			}
			return 0, errors.New("the mount's session number is not readable")
		},
		Info:      record.InfoSource(jfs.Binary(), h.cfg.Folder),
		Skip:      h.store.Skip,
		Handler:   h,
		Group:     record.GroupOptions{Quiet: h.t.Quiet, LongQuiet: h.t.LongQuiet, MaxWait: h.t.MaxWait, Workers: 12},
		StateFile: filepath.Join(config.StateDir(), "changelog.json"),
		Log:       h.log,
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
