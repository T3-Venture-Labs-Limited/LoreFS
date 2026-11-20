// Package record turns the drive's change log into version recording. It
// follows the entries made through this computer's own mount, works out
// which file each one is about, and hands the files to the recorder once
// they have been quiet for a moment.
package record

import (
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/changelog"
)

const (
	rootInode = 1
	typeFile  = 1
	typeDir   = 2
	typeLink  = 3 // symbolic link
)

// BigSlice is the size of a write after which the writer is probably still
// going: a slice is at most 64 MiB, and one this large was committed because
// it filled up, not because the program closed the file.
const BigSlice = 32 << 20

// Kind says what happened to a path.
type Kind int

const (
	Save Kind = iota + 1
	Delete
	Rename
)

// Event is something this computer did to a file or folder.
type Event struct {
	Kind Kind
	Path string // relative to the drive; for Rename, the new path
	From string // Rename: the old path
	Big  bool   // Save: a full slice was committed, so more is probably coming
	At   time.Time
}

// InfoFunc asks the mounted drive for the path of an inode ("" when it
// cannot be found or has been deleted).
type InfoFunc func(inode int64) string

// link is one name of an inode. A file with hard links has several.
type link struct {
	parent int64
	name   string
}

type node struct {
	links []link
	kind  int // 0 unknown
	gone  bool
	seq   int64
}

// Resolver keeps what has passed by about inodes and turns changelog
// entries into events about paths. It does no input or output of its own
// apart from the InfoFunc.
type Resolver struct {
	Session int64
	Info    InfoFunc
	// MaxNodes bounds the memory: beyond it the oldest inodes are
	// forgotten and looked up again if they are used (default 400,000).
	MaxNodes int

	nodes map[int64]*node
	seq   int64

	cacheIno  int64
	cachePath string
	cacheAt   time.Time
	Now       func() time.Time

	// InfoRuns counts how often the drive was asked.
	InfoRuns int
}

// NewResolver returns a resolver for the entries of one session.
func NewResolver(session int64, info InfoFunc) *Resolver {
	return &Resolver{Session: session, Info: info, nodes: map[int64]*node{}, Now: time.Now, MaxNodes: 400000}
}

var pathLine = regexp.MustCompile(`(?m)^\s*path:\s*(.+)$`)

// ParseInfo extracts the path from the output of `juicefs info -i`.
func ParseInfo(out []byte) string {
	if m := pathLine.FindSubmatch(out); m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}

// lookup asks the drive for an inode that did not pass by. Deleted files
// (in the drive's trash) have no path.
func (r *Resolver) lookup(ino int64) []string {
	if r.Info == nil {
		return nil
	}
	// Several entries for one file arrive together; ask once.
	if r.cacheIno == ino && r.Now().Sub(r.cacheAt) < 2*time.Second {
		if r.cachePath == "" {
			return nil
		}
		return []string{r.cachePath}
	}
	r.InfoRuns++
	full := r.Info(ino)
	if full == "" || strings.HasPrefix(full, "/.trash/") {
		full = ""
	}
	full = strings.TrimPrefix(full, "/")
	r.cacheIno, r.cachePath, r.cacheAt = ino, full, r.Now()
	if full == "" {
		return nil
	}
	return []string{full}
}

// pathsOf builds every path of an inode from what has passed by, asking
// the drive for what has not.
func (r *Resolver) pathsOf(ino int64) []string {
	if ino == rootInode {
		return []string{""}
	}
	n := r.nodes[ino]
	if n == nil {
		return r.lookup(ino)
	}
	if n.gone {
		return nil
	}
	var out []string
	for _, l := range n.links {
		dirs := r.pathsOf(l.parent)
		if len(dirs) == 0 {
			continue
		}
		out = append(out, path.Join(dirs[0], l.name))
	}
	return out
}

func (r *Resolver) dirOf(ino int64) (string, bool) {
	ps := r.pathsOf(ino)
	if len(ps) == 0 {
		return "", false
	}
	return ps[0], true
}

func (r *Resolver) add(ino, parent int64, name string, kind int) {
	n := r.nodes[ino]
	if n == nil || n.gone {
		r.seq++
		n = &node{kind: kind, seq: r.seq}
		r.nodes[ino] = n
		if r.MaxNodes > 0 && len(r.nodes) > r.MaxNodes {
			r.forget()
		}
	}
	if kind != 0 {
		n.kind = kind
	}
	n.links = append(removeLink(n.links, link{parent, name}), link{parent, name})
}

// forget drops deleted inodes, then the older half of the rest.
func (r *Resolver) forget() {
	for ino, n := range r.nodes {
		if n.gone {
			delete(r.nodes, ino)
		}
	}
	if len(r.nodes) <= r.MaxNodes {
		return
	}
	cut := r.seq - int64(r.MaxNodes/2)
	for ino, n := range r.nodes {
		if n.seq < cut {
			delete(r.nodes, ino)
		}
	}
}

func removeLink(ls []link, l link) []link {
	out := ls[:0:0]
	for _, x := range ls {
		if x != l {
			out = append(out, x)
		}
	}
	return out
}

// Handle updates what is known and returns the events this entry means
// for the files this computer changed.
func (r *Resolver) Handle(e changelog.Entry) []Event {
	own := e.Session == r.Session
	var out []Event
	save := func(ino int64, big bool) {
		if !own {
			return
		}
		if n := r.nodes[ino]; n != nil && (n.kind == typeDir || n.kind == typeLink) {
			return
		}
		for _, p := range r.pathsOf(ino) {
			if p != "" {
				out = append(out, Event{Kind: Save, Path: p, Big: big, At: e.Time})
			}
		}
	}
	switch e.Op {
	case "CREATE":
		parent, _ := e.Int(0)
		kind, _ := e.Int(4)
		ino, _ := e.ResultInt()
		r.add(ino, parent, e.Arg(1), int(kind))
		if own && kind == typeFile {
			save(ino, false) // an empty file has no write to follow it
		}
	case "WRITE":
		ino, _ := e.Int(0)
		length, _ := e.Int(4)
		save(ino, length >= BigSlice)
	case "TRUNCATE", "FALLOCATE":
		ino, _ := e.Int(0)
		save(ino, false)
	case "COPYFILERANGE":
		ino, _ := e.Int(2)
		save(ino, false)
	case "CLONE":
		parent, _ := e.Int(1)
		ino, _ := e.ResultInt()
		r.add(ino, parent, e.Arg(2), 0)
		save(ino, false)
	case "ATTACH", "LINK":
		ino, _ := e.Int(0)
		parent, _ := e.Int(1)
		r.add(ino, parent, e.Arg(2), 0)
		save(ino, false)
	case "MOVE":
		out = append(out, r.move(e, own)...)
	case "UNLINK":
		parent, _ := e.Int(0)
		ino, _ := e.ResultInt()
		name := e.Arg(1)
		if own {
			if dir, ok := r.dirOf(parent); ok {
				out = append(out, Event{Kind: Delete, Path: path.Join(dir, name), At: e.Time})
			}
		}
		if n := r.nodes[ino]; n != nil {
			n.links = removeLink(n.links, link{parent, name})
			if len(n.links) == 0 {
				n.gone = true
			}
		}
	case "RMDIR":
		ino, _ := e.ResultInt()
		if n := r.nodes[ino]; n != nil {
			n.gone = true
		}
	}
	return out
}

func (r *Resolver) move(e changelog.Entry, own bool) []Event {
	sp, _ := e.Int(0)
	dp, _ := e.Int(2)
	moved, _ := e.ResultInt()
	over, _ := e.Int(5)
	srcName, dstName := e.Arg(1), e.Arg(3)
	var oldPath string
	haveOld := false
	if own {
		if dir, ok := r.dirOf(sp); ok {
			oldPath, haveOld = path.Join(dir, srcName), true
		}
	}
	if over > 0 {
		if n := r.nodes[over]; n != nil {
			n.links = removeLink(n.links, link{dp, dstName})
			if len(n.links) == 0 {
				n.gone = true
			}
		}
	}
	n := r.nodes[moved]
	if n == nil {
		r.seq++
		n = &node{seq: r.seq}
		r.nodes[moved] = n
	}
	n.links = append(removeLink(n.links, link{sp, srcName}), link{dp, dstName})
	if !own {
		return nil
	}
	dir, ok := r.dirOf(dp)
	if !ok {
		// Moved to a place that is not on the drive's visible tree (the
		// trash): the file is gone as far as history is concerned.
		if haveOld {
			return []Event{{Kind: Delete, Path: oldPath, At: e.Time}}
		}
		return nil
	}
	newPath := path.Join(dir, dstName)
	if !haveOld {
		// The old place is unknown: treat it as a new file in its new place.
		return []Event{{Kind: Save, Path: newPath, At: e.Time}}
	}
	return []Event{{Kind: Rename, From: oldPath, Path: newPath, At: e.Time}}
}

// ParseSession reads the mount's own session number from the text of the
// mount's .config file.
func ParseSession(config []byte) (int64, bool) {
	// The file is JSON with "Meta":{... "Sid":N ...}. A small scan avoids
	// depending on the rest of its shape.
	s := string(config)
	i := strings.Index(s, `"Sid"`)
	if i < 0 {
		return 0, false
	}
	rest := strings.TrimLeft(s[i+len(`"Sid"`):], " \t\r\n:")
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	n, err := strconv.ParseInt(rest[:j], 10, 64)
	return n, err == nil && n > 0
}
