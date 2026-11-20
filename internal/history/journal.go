package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event is one line of a machine's journal.
type Event struct {
	Time    time.Time `json:"t"`
	Machine string    `json:"m"`
	Op      string    `json:"op"` // save, delete, rename, restore, or a folder note (below)
	Path    string    `json:"path"`
	To      string    `json:"to,omitempty"`      // rename target
	Size    int64     `json:"size,omitempty"`    // save, restore
	Version string    `json:"version,omitempty"` // file name of the version
}

// Folder notes. A machine only sees changes made through its own mount,
// so each machine writes down the folders it creates and renames, and the
// others read that to know which folders to watch. They are not shown in
// the activity log.
const (
	OpNewFolder  = "mkdir"
	OpMoveFolder = "mvdir"
)

// FolderNote reports whether an event is a note about a folder rather
// than something that happened to a file.
func (e Event) FolderNote() bool { return e.Op == OpNewFolder || e.Op == OpMoveFolder }

// Journal collects this machine's events and writes them in batches to
// .lorefs/journal/<machine>/<date>.jsonl. Each machine writes only its
// own files, so machines never write to the same journal file.
type Journal struct {
	root    string
	machine string
	mu      sync.Mutex
	buf     []Event
}

// NewJournal returns the journal of one machine on the drive at root.
func NewJournal(root, machine string) *Journal {
	return &Journal{root: root, machine: machine}
}

func journalDir(root string) string { return filepath.Join(root, MetaDir, "journal") }

// Append queues an event. Call Flush to write it.
func (j *Journal) Append(e Event) {
	e.Machine = j.machine
	j.mu.Lock()
	j.buf = append(j.buf, e)
	j.mu.Unlock()
}

// Flush writes queued events. Events stay queued if writing fails.
func (j *Journal) Flush() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.buf) == 0 {
		return nil
	}
	byDay := map[string][]Event{}
	var days []string
	for _, e := range j.buf {
		d := e.Time.UTC().Format("2006-01-02")
		if _, ok := byDay[d]; !ok {
			days = append(days, d)
		}
		byDay[d] = append(byDay[d], e)
	}
	dir := filepath.Join(journalDir(j.root), j.machine)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, d := range days {
		var sb strings.Builder
		for _, e := range byDay[d] {
			line, err := json.Marshal(e)
			if err != nil {
				return err
			}
			sb.Write(line)
			sb.WriteByte('\n')
		}
		f, err := os.OpenFile(filepath.Join(dir, d+".jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		if _, err := f.WriteString(sb.String()); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	j.buf = j.buf[:0]
	return nil
}

// ReadJournals returns the events of every machine since the given time,
// newest first.
func ReadJournals(root string, since time.Time) ([]Event, error) {
	machines, err := os.ReadDir(journalDir(root))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	firstDay := since.UTC().Format("2006-01-02")
	var out []Event
	for _, m := range machines {
		if !m.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(journalDir(root), m.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			day, ok := strings.CutSuffix(f.Name(), ".jsonl")
			if !ok || day < firstDay {
				continue
			}
			evs, _, err := ReadJournalFile(filepath.Join(journalDir(root), m.Name(), f.Name()), 0)
			if err != nil {
				continue
			}
			for _, e := range evs {
				if !e.Time.Before(since) {
					out = append(out, e)
				}
			}
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Time.After(out[b].Time) })
	return out, nil
}

// ReadJournalFile reads complete lines of one journal file starting at a
// byte offset and returns the events and the offset after the last
// complete line, so a caller can follow a file as it grows.
func ReadJournalFile(path string, offset int64) ([]Event, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, 0); err != nil {
		return nil, offset, err
	}
	var out []Event
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if err != nil { // an incomplete last line is left for next time
			break
		}
		offset += int64(len(line))
		var e Event
		if json.Unmarshal([]byte(line), &e) == nil && e.Op != "" {
			out = append(out, e)
		}
	}
	return out, offset, nil
}

// OtherMachines lists the machines that have a journal, except self.
func OtherMachines(root, self string) []string {
	entries, err := os.ReadDir(journalDir(root))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != self {
			out = append(out, e.Name())
		}
	}
	return out
}

// JournalFile is the path of one machine's journal for one day.
func JournalFile(root, machine string, day time.Time) string {
	return filepath.Join(journalDir(root), machine, day.UTC().Format("2006-01-02")+".jsonl")
}
