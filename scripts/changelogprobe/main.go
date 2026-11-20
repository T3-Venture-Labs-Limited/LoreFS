// Command changelogprobe is a test tool: it follows JuiceFS's metadata
// changelog for one drive and prints, for every change made through one
// mount, what happened and to which path. It is how the changelog was
// checked before version recording was built on it
// (docs/design-notes/changelog.md).
//
//	changelogprobe -juicefs PATH -meta URL -mount DIR [-from N] [-session N]
//
// Output, one line per change, timestamps in seconds since the program
// started:   0.512 save d/report.md   |   1.2 rename d/a d/b   |   2.0 delete d/x
// A line "gap N M" is printed when entries N..M were not there to read.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/changelog"
)

const (
	rootInode = 1
	typeFile  = 1
	typeDir   = 2
)

// link is one name of an inode. A file with hard links has several.
type link struct {
	parent int64
	name   string
}

type node struct {
	links []link
	kind  int // typeFile, typeDir, 0 unknown
	gone  bool
}

type probe struct {
	jfs, meta, mount string
	session          int64
	nodes            map[int64]*node
	started          time.Time
	infoRuns         int
	abs              bool
}

var pathLine = regexp.MustCompile(`(?m)^\s*path:\s*(.+)$`)

// info asks the mounted drive for an inode's path.
func (p *probe) info(ino int64) string {
	p.infoRuns++
	cmd := exec.Command(p.jfs, "info", "-i", strconv.FormatInt(ino, 10))
	cmd.Dir = p.mount
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	if m := pathLine.FindSubmatch(out); m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}

// pathsOf builds every path of an inode from what has passed by, asking
// the drive for what has not.
func (p *probe) pathsOf(ino int64) []string {
	if ino == rootInode {
		return []string{""}
	}
	n := p.nodes[ino]
	if n == nil {
		full := p.info(ino)
		if full == "" || strings.HasPrefix(full, "/.trash/") {
			return nil
		}
		return []string{strings.TrimPrefix(full, "/")}
	}
	if n.gone {
		return nil
	}
	var out []string
	for _, l := range n.links {
		dirs := p.pathsOf(l.parent)
		if len(dirs) == 0 {
			continue
		}
		out = append(out, path.Join(dirs[0], l.name))
	}
	return out
}

func (p *probe) pathOf(ino int64) (string, bool) {
	ps := p.pathsOf(ino)
	if len(ps) == 0 {
		return "", false
	}
	return ps[0], true
}

func (p *probe) say(format string, a ...any) {
	if p.abs {
		fmt.Printf("%.3f %s\n", float64(time.Now().UnixMilli())/1000, fmt.Sprintf(format, a...))
		return
	}
	fmt.Printf("%7.3f %s\n", time.Since(p.started).Seconds(), fmt.Sprintf(format, a...))
}

// handle updates what is known about inodes, and prints changes made
// through the chosen session.
func (p *probe) handle(e changelog.Entry) {
	own := e.Session == p.session
	add := func(ino, parent int64, name string, kind int) {
		n := p.nodes[ino]
		if n == nil || n.gone {
			n = &node{kind: kind}
			p.nodes[ino] = n
		}
		if kind != 0 {
			n.kind = kind
		}
		n.links = append(n.links, link{parent, name})
	}
	save := func(ino int64) {
		if !own {
			return
		}
		if n := p.nodes[ino]; n != nil && n.kind == typeDir {
			return
		}
		for _, full := range p.pathsOf(ino) {
			p.say("save %s", full)
		}
	}
	switch e.Op {
	case "CREATE":
		parent, _ := e.Int(0)
		kind, _ := e.Int(4)
		ino, _ := e.ResultInt()
		add(ino, parent, e.Arg(1), int(kind))
	case "WRITE", "TRUNCATE", "FALLOCATE", "SETATTR":
		ino, _ := e.Int(0)
		save(ino)
	case "COPYFILERANGE":
		ino, _ := e.Int(2)
		save(ino)
	case "CLONE":
		parent, _ := e.Int(1)
		ino, _ := e.ResultInt()
		add(ino, parent, e.Arg(2), 0)
		save(ino)
	case "ATTACH", "LINK":
		ino, _ := e.Int(0)
		parent, _ := e.Int(1)
		add(ino, parent, e.Arg(2), 0)
		save(ino)
	case "MOVE":
		sp, _ := e.Int(0)
		dp, _ := e.Int(2)
		moved, _ := e.ResultInt()
		over, _ := e.Int(5)
		srcName, dstName := e.Arg(1), e.Arg(3)
		var oldPath string
		if own {
			oldPath = path.Join(p.dirOf(sp), srcName)
		}
		if over > 0 {
			if n := p.nodes[over]; n != nil {
				n.links = removeLink(n.links, link{dp, dstName})
				if len(n.links) == 0 {
					n.gone = true
				}
			}
		}
		n := p.nodes[moved]
		if n == nil {
			n = &node{}
			p.nodes[moved] = n
		}
		n.links = append(removeLink(n.links, link{sp, srcName}), link{dp, dstName})
		if own {
			newPath := path.Join(p.dirOf(dp), dstName)
			p.say("rename %s %s", oldPath, newPath)
			if n.kind != typeDir {
				p.say("save %s", newPath)
			}
		}
	case "UNLINK":
		parent, _ := e.Int(0)
		ino, _ := e.ResultInt()
		if own {
			p.say("delete %s", path.Join(p.dirOf(parent), e.Arg(1)))
		}
		if n := p.nodes[ino]; n != nil {
			n.links = removeLink(n.links, link{parent, e.Arg(1)})
			if len(n.links) == 0 {
				n.gone = true
			}
		}
	case "RMDIR":
		ino, _ := e.ResultInt()
		if n := p.nodes[ino]; n != nil {
			n.gone = true
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

func (p *probe) dirOf(ino int64) string {
	d, _ := p.pathOf(ino)
	return d
}

func main() {
	jfs := flag.String("juicefs", "juicefs", "the juicefs program")
	meta := flag.String("meta", "", "index address")
	mount := flag.String("mount", "", "a mounted folder of the drive (to ask for paths)")
	from := flag.Int64("from", 0, "first version to read (0: from now)")
	session := flag.Int64("session", 0, "report changes made through this session (default: the mount's own)")
	abs := flag.Bool("abs", false, "print the time as seconds since 1970 instead of since the start")
	stats := flag.Int("stats", 0, "print a line every N entries read, with the time it took")
	flag.Parse()
	if *meta == "" || *mount == "" {
		fmt.Fprintln(os.Stderr, "need -meta and -mount")
		os.Exit(64)
	}
	if *session == 0 {
		data, err := os.ReadFile(path.Join(*mount, ".config"))
		var cfg struct{ Meta struct{ Sid int64 } }
		if err != nil || json.Unmarshal(data, &cfg) != nil || cfg.Meta.Sid == 0 {
			fmt.Fprintln(os.Stderr, "cannot read the mount's session from .config:", err)
			os.Exit(1)
		}
		*session = cfg.Meta.Sid
	}
	p := &probe{jfs: *jfs, meta: *meta, mount: *mount, session: *session, nodes: map[int64]*node{}, started: time.Now(), abs: *abs}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	args := []string{"changelog", *meta}
	if *from > 0 {
		args = append(args, "--from", strconv.FormatInt(*from, 10))
	}
	cmd := exec.CommandContext(ctx, *jfs, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	// "from N" means the entries after N.
	expect := int64(0)
	if *from > 0 {
		expect = *from + 1
	}
	read := 0
	for sc.Scan() {
		e, err := changelog.Parse(sc.Text())
		if err != nil {
			continue
		}
		if expect > 0 && e.Version > expect {
			p.say("gap %d %d", expect, e.Version-1)
		}
		expect = e.Version + 1
		read++
		if *stats > 0 && read%*stats == 0 {
			p.say("read %d entries, at version %d", read, e.Version)
		}
		p.handle(e)
	}
	fmt.Printf("# last version %d, asked the drive for %d paths\n", expect-1, p.infoRuns)
	_ = cmd.Wait()
}
