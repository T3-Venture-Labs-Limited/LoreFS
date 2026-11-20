// Package restore rebuilds a drive's index from one of the copies JuiceFS
// keeps in the drive's bucket (or in the backup's bucket), and rehearses
// doing so without touching the real index or either bucket.
package restore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/create"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/index"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/s3check"
)

// Source says where the copies of the index, and the file contents, are.
type Source struct {
	Bucket    string // address of the bucket, as for `lorefs create`
	AccessKey string
	SecretKey string
	Name      string // the drive's name: its files are under this prefix
}

// ErrIndexInUse is returned when the target index already holds a drive.
var ErrIndexInUse = errors.New("the index already holds a drive")

// Copies lists the copies of the index in the bucket, oldest first.
func Copies(ctx context.Context, s Source) ([]string, error) {
	cl, err := s3check.Parse(s.Bucket)
	if err != nil {
		return nil, err
	}
	cl.AccessKey, cl.SecretKey = s.AccessKey, s.SecretKey
	prefix := s.Name + "/meta/"
	keys, err := cl.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, k := range keys {
		if name := strings.TrimPrefix(k, prefix); strings.HasPrefix(name, "dump-") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("no copies of the index were found in the bucket under %s", prefix)
	}
	return out, nil
}

// Options describe a restore or a drill.
type Options struct {
	Source Source
	Copy   string // a name from Copies; empty: the newest
	// Target index, for a restore.
	IndexURL  string
	IndexPass string
	IndexCA   []byte
	// Drill: compare restored files with this folder (the live drive) when set.
	LiveFolder string
	Runner     create.Runner
	Progress   func(string)
}

func (o *Options) say(s string) {
	if o.Progress != nil {
		o.Progress(s)
	}
}

func (o *Options) runner() create.Runner {
	if o.Runner != nil {
		return o.Runner
	}
	return create.ExecRunner{Bin: jfs.Binary()}
}

// fetch downloads the chosen copy into dir and returns its path and name.
func (o *Options) fetch(ctx context.Context, dir string) (string, string, error) {
	names, err := Copies(ctx, o.Source)
	if err != nil {
		return "", "", err
	}
	name := o.Copy
	if name == "" {
		name = names[len(names)-1]
	}
	found := false
	for _, n := range names {
		found = found || n == name
	}
	if !found {
		return "", "", fmt.Errorf("there is no copy called %s; `lorefs restore-index --list` shows them", name)
	}
	cl, _ := s3check.Parse(o.Source.Bucket)
	cl.AccessKey, cl.SecretKey = o.Source.AccessKey, o.Source.SecretKey
	o.say("Using the index copy " + name)
	data, err := cl.Get(ctx, o.Source.Name+"/meta/"+name)
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(dir, name)
	return path, name, os.WriteFile(path, data, 0o600)
}

// load puts a copy into an index and gives the drive its storage key back
// (copies never hold it), pointing the drive at Source.Bucket.
func (o *Options) load(ctx context.Context, meta, dump string, env []string) error {
	r := o.runner()
	if out, err := r.Run(ctx, env, "load", meta, dump); err != nil {
		return fmt.Errorf("loading the copy failed: %s", lastLine(out))
	}
	if out, err := r.Run(ctx, env, "config", meta, "--storage", "s3", "--bucket", o.Source.Bucket,
		"--access-key", o.Source.AccessKey, "--secret-key", o.Source.SecretKey, "--yes"); err != nil {
		return fmt.Errorf("giving the drive its storage key back failed: %s", lastLine(out))
	}
	return nil
}

// Restore rebuilds the index at o.IndexURL. It refuses an index that holds
// a drive.
func Restore(ctx context.Context, o Options) (copyName string, err error) {
	var cfg config.Config
	if err := cfg.SetIndex(o.IndexURL); err != nil {
		return "", err
	}
	if cfg.IsLocal() {
		return "", errors.New("restore into a shared index (rediss://...)")
	}
	conn, derr := index.Dial(index.Target{Host: cfg.IndexHost, Port: cfg.IndexPort, DB: cfg.IndexDB, TLS: cfg.IndexTLS,
		Password: o.IndexPass, CAPEM: o.IndexCA}, 20*time.Second)
	if derr != nil {
		return "", fmt.Errorf("the target index cannot be used: %w", derr)
	}
	name, formatted := conn.IsFormatted()
	conn.Close()
	if formatted {
		return "", fmt.Errorf("%w (named %q). Restoring over it is refused. If it is damaged, empty it first (see docs/restore.md)", ErrIndexInUse, name)
	}
	dir, err := os.MkdirTemp("", "lorefs-restore-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	dump, copyName, err := o.fetch(ctx, dir)
	if err != nil {
		return "", err
	}
	meta := cfg.IndexURL()
	if len(o.IndexCA) > 0 {
		ca := filepath.Join(dir, "ca.pem")
		if err := os.WriteFile(ca, o.IndexCA, 0o600); err != nil {
			return "", err
		}
		meta += "?tls-ca-cert-file=" + ca
	}
	env := []string{"META_PASSWORD=" + o.IndexPass}
	o.say("Restoring into the index at " + cfg.IndexHost + "...")
	if err := o.load(ctx, meta, dump, env); err != nil {
		return copyName, err
	}
	o.say("Checking that every file's blocks are present in the bucket...")
	if out, err := o.runner().Run(ctx, env, "fsck", meta); err != nil {
		return copyName, fmt.Errorf("the check reported problems (files written after the copy may be affected): %s", lastLine(out))
	}
	return copyName, nil
}

// DrillResult is what a rehearsal found.
type DrillResult struct {
	Copy                               string
	Files                              int
	Compared, Same, Different, Missing int
}

// Drill restores into a throwaway index on this computer, mounts it
// read-only, compares files with the live drive when o.LiveFolder is set,
// and removes everything it made. It changes neither the real index nor
// either bucket.
func Drill(ctx context.Context, o Options) (res DrillResult, err error) {
	dir, err := os.MkdirTemp("", "lorefs-drill-*")
	if err != nil {
		return res, err
	}
	mnt := filepath.Join(dir, "mnt")
	defer func() {
		exec.Command("fusermount3", "-uz", mnt).Run()
		time.Sleep(300 * time.Millisecond)
		os.Chmod(dir, 0o700)
		os.RemoveAll(dir)
	}()
	dump, name, err := o.fetch(ctx, dir)
	if err != nil {
		return res, err
	}
	res.Copy = name
	var c config.Config
	c.IndexScheme, c.IndexPath = config.SchemeSQLite, filepath.Join(dir, "meta.db")
	meta := c.IndexURL()
	o.say("Restoring into a throwaway index on this computer...")
	if err := o.load(ctx, meta, dump, nil); err != nil {
		return res, err
	}
	if err := os.MkdirAll(mnt, 0o700); err != nil {
		return res, err
	}
	// Read-only and with background jobs off, so the rehearsal cannot change
	// or delete anything in the real bucket.
	out, merr := o.runner().Run(ctx, nil, "mount", "-d", "--read-only", "--no-bgjob", "--backup-meta", "0",
		"--no-usage-report", "--hide-internal", "--cache-dir", filepath.Join(dir, "cache"), meta, mnt)
	if merr != nil {
		return res, fmt.Errorf("mounting the rehearsal copy failed: %s", lastLine(out))
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		if _, e := os.Stat(filepath.Join(mnt, ".config")); e == nil {
			break
		}
	}
	var files []string
	filepath.WalkDir(mnt, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return nil
		}
		rel, _ := filepath.Rel(mnt, p)
		if d.IsDir() {
			if rel == ".trash" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(rel, ".lorefs/journal") || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	sort.Strings(files)
	res.Files = len(files)
	if o.LiveFolder != "" {
		limit := files
		if len(limit) > 500 {
			limit = limit[:500]
		}
		for _, f := range limit {
			live := filepath.Join(o.LiveFolder, f)
			if _, e := os.Stat(live); e != nil {
				res.Missing++
				continue
			}
			a, e1 := os.ReadFile(filepath.Join(mnt, f))
			b, e2 := os.ReadFile(live)
			switch {
			case e1 != nil || e2 != nil:
				res.Different++
			case bytes.Equal(a, b):
				res.Same++
			default:
				res.Different++
			}
			res.Compared++
		}
	}
	return res, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, "<FATAL>: "); i >= 0 {
		s = s[i+len("<FATAL>: "):]
	}
	if i := strings.LastIndex(s, " ["); i > 0 && strings.HasSuffix(s, "]") {
		s = s[:i]
	}
	return s
}
