// Package jfs wraps the few JuiceFS commands LoreFS relies on, behind
// small interfaces so the rest of the code can be tested without JuiceFS.
package jfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Cloner copies a file or folder inside the drive without duplicating
// stored data when it can.
type Cloner interface {
	Clone(src, dst string) error
	// RemoveTree deletes a folder and everything in it.
	RemoveTree(path string) error
}

// Warmer brings file content into the local cache and reports how much
// of it is there.
type Warmer interface {
	Warm(paths []string) error
	CachedPercent(path string) (float64, error)
}

// Binary returns the path of the juicefs program: beside the running
// program first, then on PATH.
func Binary() string {
	if b := os.Getenv("LOREFS_JUICEFS"); b != "" {
		return b
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "juicefs")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand
		}
	}
	if p, err := exec.LookPath("juicefs"); err == nil {
		return p
	}
	return "juicefs"
}

// Exec runs the real juicefs program.
type Exec struct{ Bin string }

// NewExec returns an Exec using Binary().
func NewExec() Exec { return Exec{Bin: Binary()} }

func (e Exec) run(args ...string) (string, error) {
	return e.runContext(context.Background(), args...)
}

func (e Exec) runContext(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, e.Bin, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("juicefs %s: %w: %s", args[0], err, lastLine(out.String()))
	}
	return out.String(), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// Clone runs `juicefs clone`, which copies index entries only.
func (e Exec) Clone(src, dst string) error {
	_, err := e.run("clone", src, dst)
	return err
}

// Warm runs `juicefs warmup` on the given paths.
func (e Exec) Warm(paths []string) error { return e.WarmContext(context.Background(), paths) }

// WarmContext is Warm that gives up when ctx ends. On a slow connection a
// warmup can take minutes, and the helper must still stop when asked.
func (e Exec) WarmContext(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	list, err := os.CreateTemp("", "lorefs-warm-*")
	if err != nil {
		return err
	}
	defer os.Remove(list.Name())
	if _, err := io.WriteString(list, strings.Join(paths, "\n")+"\n"); err != nil {
		list.Close()
		return err
	}
	list.Close()
	_, err = e.runContext(ctx, "warmup", "--threads", "20", "--file", list.Name())
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// WarmBackground starts caching a file or folder and returns at once.
func (e Exec) WarmBackground(path string) error {
	_, err := e.run("warmup", "--threads", "20", "--background", path)
	return err
}

var cachedRe = regexp.MustCompile(`\(([0-9.]+)%\) cached`)

// CachedPercent reports how much of a file or folder is in the local cache.
func (e Exec) CachedPercent(path string) (float64, error) {
	out, err := e.run("warmup", "--check", path)
	if err != nil {
		return 0, err
	}
	m := cachedRe.FindStringSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("could not read the cache report for %s", path)
	}
	return strconv.ParseFloat(m[1], 64)
}

// Evict drops a file or folder from the local cache.
func (e Exec) Evict(path string) error {
	_, err := e.run("warmup", "--evict", path)
	return err
}

// Version returns the JuiceFS version string, for status.
func (e Exec) Version() string {
	out, err := e.run("version")
	if err != nil {
		return "not found"
	}
	return strings.TrimPrefix(strings.TrimSpace(out), "juicefs version ")
}

// Copy is a stand-in for tests and for folders that are not on JuiceFS.
// It copies bytes, so it duplicates data.
type Copy struct{}

// Clone copies a file, or a folder and everything in it.
func (Copy) Clone(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return copyFile(src, dst, st.Mode().Perm())
	}
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := copyFile(p, target, info.Mode().Perm()); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// Warm does nothing: local files are always "cached".
func (Copy) Warm([]string) error { return nil }

// CachedPercent always reports fully cached.
func (Copy) CachedPercent(string) (float64, error) { return 100, nil }

// MountInfo is what the mounted drive reports about itself.
type MountInfo struct {
	Name      string
	Storage   string
	Bucket    string
	CacheDir  string
	CacheSize int64
	Writeback bool
}

// ReadMountInfo reads the `.config` file JuiceFS shows at the top of a
// mounted drive. It fails if the folder is not a mounted JuiceFS drive.
func ReadMountInfo(folder string) (MountInfo, error) {
	var mi MountInfo
	data, err := os.ReadFile(filepath.Join(folder, ".config"))
	if err != nil {
		return mi, err
	}
	var raw struct {
		Format struct {
			Name, Storage, Bucket string
		}
		Chunk struct {
			CacheDir  string
			CacheSize int64
			Writeback bool
		}
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return mi, err
	}
	mi.Name, mi.Storage, mi.Bucket = raw.Format.Name, raw.Format.Storage, raw.Format.Bucket
	mi.CacheDir, mi.CacheSize, mi.Writeback = raw.Chunk.CacheDir, raw.Chunk.CacheSize, raw.Chunk.Writeback
	return mi, nil
}

// IsMounted reports whether a JuiceFS drive is mounted on folder. It
// reads the kernel's mount table and never touches the drive itself, so
// it answers at once and correctly even when the index is unreachable.
func IsMounted(folder string) bool {
	return mountedIn("/proc/self/mountinfo", folder)
}

func mountedIn(mountinfo, folder string) bool {
	abs, err := filepath.Abs(folder)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(mountinfo)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		// "36 35 98:0 /root /mount/point opts ... - fstype source superopts"
		left, right, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		fields := strings.Fields(left)
		if len(fields) < 5 || unescapeMount(fields[4]) != abs {
			continue
		}
		if fstype := strings.Fields(right); len(fstype) > 0 && strings.HasPrefix(fstype[0], "fuse.juicefs") {
			return true
		}
	}
	return false
}

// unescapeMount decodes the octal escapes (\040 for a space, and so on)
// the kernel uses in mount paths.
func unescapeMount(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				sb.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// IsDead reports whether folder is a mount whose JuiceFS process has
// gone, which leaves an entry that answers every request with "not
// connected". A live mount with an unreachable index is not dead.
func IsDead(folder string) bool {
	if !IsMounted(folder) {
		return false
	}
	result := make(chan error, 1)
	go func() {
		var st syscall.Stat_t
		result <- syscall.Stat(folder, &st)
	}()
	select {
	case err := <-result:
		return errors.Is(err, syscall.ENOTCONN)
	case <-time.After(2 * time.Second):
		return false // something is answering, only slowly
	}
}

// ErrNotMounted is returned when an operation needs the mounted drive.
var ErrNotMounted = errors.New("the drive is not mounted")
