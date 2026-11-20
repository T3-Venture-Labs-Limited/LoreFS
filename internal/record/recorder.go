package record

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/changelog"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
)

// Source delivers the change log's lines. The real one runs
// `juicefs changelog`; tests feed their own.
type Source interface {
	// Follow calls emit for every line after version from (0: only what
	// is made from now on) until ctx ends or the source fails.
	Follow(ctx context.Context, from int64, emit func(line string)) error
}

// Options say how to follow the log.
type Options struct {
	Folder    string // where the drive is mounted
	Source    Source
	Session   func() (int64, error) // this mount's own session number
	Info      InfoFunc
	Skip      *history.Skip
	Handler   Handler
	Group     GroupOptions
	StateFile string // where the position is kept; "" keeps none
	Log       *log.Logger

	PositionEvery time.Duration // how often the position is written (default 2s)
	SessionEvery  time.Duration // how often the session is checked (default 20s)
	RetryMin      time.Duration // wait before restarting the reader (default 1s)
	RetryMax      time.Duration // ... growing to this (default 30s)
}

// Recorder follows the change log and hands settled changes to a Handler.
type Recorder struct {
	o Options
	g *group

	read    atomic.Int64 // the last version read
	started atomic.Bool
	entries atomic.Int64
	restart atomic.Int64
}

// New prepares a recorder. Call Run to start it.
func New(o Options) *Recorder {
	if o.PositionEvery <= 0 {
		o.PositionEvery = 2 * time.Second
	}
	if o.SessionEvery <= 0 {
		o.SessionEvery = 20 * time.Second
	}
	if o.RetryMin <= 0 {
		o.RetryMin = time.Second
	}
	if o.RetryMax <= 0 {
		o.RetryMax = 30 * time.Second
	}
	if o.Log == nil {
		o.Log = log.New(io.Discard, "", 0)
	}
	return &Recorder{o: o, g: newGroup(o.Folder, o.Skip, o.Handler, o.Group)}
}

// Version returns the last log version that was read.
func (r *Recorder) Version() int64 { return r.read.Load() }

// Position returns the log version up to which every change has been
// handled; a restart continues from it.
func (r *Recorder) Position() int64 { return r.g.safe(r.read.Load()) }

// Entries returns how many log entries have been read since the start.
func (r *Recorder) Entries() int64 { return r.entries.Load() }

// Restarts returns how often the reader had to be started again.
func (r *Recorder) Restarts() int64 { return r.restart.Load() }

type state struct {
	Folder  string    `json:"folder"`
	Version int64     `json:"version"`
	Saved   time.Time `json:"saved"`
}

func (r *Recorder) loadPosition() (int64, time.Time) {
	if r.o.StateFile == "" {
		return 0, time.Time{}
	}
	data, err := os.ReadFile(r.o.StateFile)
	if err != nil {
		return 0, time.Time{}
	}
	var st state
	if json.Unmarshal(data, &st) != nil || st.Folder != r.o.Folder || st.Version < 0 {
		return 0, time.Time{}
	}
	return st.Version, st.Saved
}

func (r *Recorder) savePosition(v int64) {
	if r.o.StateFile == "" {
		return
	}
	data, _ := json.Marshal(state{Folder: r.o.Folder, Version: v, Saved: time.Now().UTC()})
	tmp := r.o.StateFile + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, r.o.StateFile)
	}
}

// Run follows the log until the context ends. ready, if not nil, is closed
// once the reader has been started.
func (r *Recorder) Run(ctx context.Context, ready chan<- struct{}) error {
	o := r.o
	pos, _ := r.loadPosition()
	r.read.Store(pos)

	var wg sync.WaitGroup
	for i := 0; i < r.g.opt.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range r.g.jobs {
				r.g.work(rel)
			}
		}()
	}
	var tickWG sync.WaitGroup
	stopTick := make(chan struct{})
	tickWG.Add(1)
	go func() {
		defer tickWG.Done()
		t := time.NewTicker(tickEvery(r.g.opt.Quiet))
		p := time.NewTicker(o.PositionEvery)
		defer t.Stop()
		defer p.Stop()
		last := int64(-1)
		for {
			select {
			case <-stopTick:
				return
			case now := <-t.C:
				r.g.due(now)
			case <-p.C:
				if v := r.Position(); v != last {
					r.savePosition(v)
					last = v
				}
			}
		}
	}()

	backoff := o.RetryMin
	readyClosed := false
	for ctx.Err() == nil {
		session, serr := o.Session()
		if serr != nil || session == 0 {
			if !sleepCtx(ctx, 2*time.Second) {
				break
			}
			continue
		}
		fctx, cancel := context.WithCancel(ctx)
		// Watch for the mount having been restarted: a new session.
		go func() {
			t := time.NewTicker(o.SessionEvery)
			defer t.Stop()
			for {
				select {
				case <-fctx.Done():
					return
				case <-t.C:
					if s, e := o.Session(); e == nil && s != 0 && s != session {
						o.Log.Printf("the drive was mounted again (session %d, was %d); following it", s, session)
						cancel()
						return
					}
				}
			}
		}()
		res := NewResolver(session, o.Info)
		from := r.read.Load()
		expect := from + 1
		if from == 0 {
			expect = 0
		}
		began := time.Now()
		if !readyClosed && ready != nil {
			close(ready)
			readyClosed = true
		}
		r.started.Store(true)
		ferr := o.Source.Follow(fctx, from, func(line string) {
			e, perr := changelog.Parse(line)
			if perr != nil {
				return
			}
			r.entries.Add(1)
			if expect > 0 && e.Version != expect {
				if e.Version > expect {
					o.Log.Printf("log entries %d to %d are gone; asking for a catch-up", expect, e.Version-1)
					o.Handler.CatchUp(fmt.Sprintf("log entries %d to %d were no longer there", expect, e.Version-1))
				} else {
					o.Log.Printf("the log starts again at %d (was at %d); asking for a catch-up", e.Version, expect-1)
					o.Handler.CatchUp("the log was reset")
				}
			}
			expect = e.Version + 1
			r.read.Store(e.Version)
			for _, ev := range res.Handle(e) {
				r.apply(ev, e.Version)
			}
		})
		cancel()
		if ctx.Err() != nil {
			break
		}
		r.restart.Add(1)
		if time.Since(began) > time.Minute {
			backoff = o.RetryMin
		}
		if ferr != nil {
			o.Log.Printf("reading the change log stopped: %v; trying again in %s", ferr, backoff)
		}
		if r.read.Load() == 0 {
			// A reader that started "from now" and died before reading
			// anything has no position to continue from: what came in
			// meanwhile may have been missed.
			o.Handler.CatchUp("the change log reader restarted without a position")
		}
		if !sleepCtx(ctx, backoff) {
			break
		}
		if backoff *= 2; backoff > o.RetryMax {
			backoff = o.RetryMax
		}
	}
	close(stopTick)
	tickWG.Wait()
	close(r.g.jobs)
	wg.Wait()
	r.savePosition(r.Position())
	return nil
}

func (r *Recorder) apply(ev Event, version int64) {
	switch ev.Kind {
	case Save:
		r.g.touch(ev.Path, ev.Big, version, time.Now())
	case Delete:
		r.g.touch(ev.Path, false, version, time.Now())
	case Rename:
		r.g.renamed(ev.From, ev.Path, version, time.Now())
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// ExecSource runs the juicefs program's `changelog` command.
type ExecSource struct {
	Bin  string
	Meta string
	Env  []string // extra environment, such as META_PASSWORD
	Log  *log.Logger
}

// Follow implements Source.
func (s ExecSource) Follow(ctx context.Context, from int64, emit func(line string)) error {
	args := []string{"changelog", s.Meta}
	if from > 0 {
		args = append(args, "--from", strconv.FormatInt(from, 10))
	}
	cmd := exec.CommandContext(ctx, s.Bin, args...)
	cmd.Env = append(os.Environ(), s.Env...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go io.Copy(io.Discard, stderr) // the program's own log lines
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		emit(sc.Text())
	}
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	if werr != nil {
		return werr
	}
	return errors.New("the program ended")
}

// InfoSource asks the mounted drive for the path of an inode by running
// `juicefs info -i` inside the mount.
func InfoSource(bin, mountDir string) InfoFunc {
	return func(ino int64) string {
		cmd := exec.Command(bin, "info", "-i", strconv.FormatInt(ino, 10))
		cmd.Dir = mountDir
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return ParseInfo(out)
	}
}
