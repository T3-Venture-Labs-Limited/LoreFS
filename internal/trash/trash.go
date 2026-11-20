// Package trash finds and brings back files that were deleted or replaced
// on the drive in the last few days. JuiceFS moves such files into a
// hidden ".trash" folder at the top of the drive and keeps them for the
// number of days chosen when the drive was created (7 for LoreFS).
//
// It complements version history: files that history skips on purpose,
// such as videos, can still be recovered from here.
package trash

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
)

// Entry is one deleted or replaced file.
type Entry struct {
	Name    string    `json:"name"`    // the name it had
	Deleted time.Time `json:"deleted"` // when it was deleted or replaced
	Size    int64     `json:"size"`
	Path    string    `json:"path"` // where it is kept now
}

// JuiceFS names a trashed file "<folder number>-<file number>-<name>" and
// groups files by the hour (UTC) in which they were deleted.
var (
	entryRe   = regexp.MustCompile(`^\d+-\d+-(.+)$`)
	versionRe = regexp.MustCompile(`^\d{8}T\d{6}\.\d{3}Z~[^~]+~.+$`)
)

const hourLayout = "2006-01-02-15"

// bookkeeping reports whether a name belongs to LoreFS itself (old
// versions removed by retention, journals, temporary files) rather than
// to something a person deleted.
func bookkeeping(name string) bool {
	switch {
	case versionRe.MatchString(name), name == ".deleted", name == "prune.lock", name == "meta.json":
		return true
	case strings.HasPrefix(name, ".lorefs-"), strings.HasSuffix(name, ".jsonl"):
		return true
	}
	return false
}

// List returns trashed files, newest first. A non-empty filter keeps only
// names containing it, ignoring upper and lower case.
func List(root, filter string) ([]Entry, error) {
	dir := filepath.Join(root, ".trash")
	hours, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	filter = strings.ToLower(filter)
	var out []Entry
	for _, h := range hours {
		if !h.IsDir() {
			continue
		}
		hourTime, _ := time.Parse(hourLayout, h.Name())
		// jfs.ReadDir, not os.ReadDir: the trash can hold thousands of
		// entries, and asking about each one separately took 80 seconds
		// for 800 entries on the real drive.
		files, err := jfs.ReadDir(filepath.Join(dir, h.Name()))
		if err != nil {
			continue
		}
		for _, info := range files {
			m := entryRe.FindStringSubmatch(info.Name())
			if m == nil || !info.Mode().IsRegular() || bookkeeping(m[1]) {
				continue
			}
			if filter != "" && !strings.Contains(strings.ToLower(m[1]), filter) {
				continue
			}
			e := Entry{Name: m[1], Size: info.Size(), Path: filepath.Join(dir, h.Name(), info.Name()), Deleted: hourTime}
			// The change time is set when the file enters the trash. Use it
			// when it falls inside the hour the folder is named for.
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				ct := time.Unix(st.Ctim.Sec, st.Ctim.Nsec)
				if hourTime.IsZero() || (!ct.Before(hourTime) && ct.Before(hourTime.Add(time.Hour))) {
					e.Deleted = ct
				}
			}
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Deleted.After(out[b].Deleted) })
	return out, nil
}

// Find returns the most recently trashed file with exactly this name, or,
// failing that, with this name ignoring upper and lower case.
func Find(root, name string) (Entry, bool, error) {
	entries, err := List(root, "")
	if err != nil {
		return Entry{}, false, err
	}
	for _, e := range entries {
		if e.Name == name {
			return e, true, nil
		}
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, name) {
			return e, true, nil
		}
	}
	return Entry{}, false, nil
}

// Restore puts a trashed file into destDir under its old name. If a file
// of that name is already there it is not touched: the restored file gets
// " (restored)" added to its name instead. It returns the path written.
//
// The file is copied. The drive refuses to clone a file out of its trash
// ("operation not permitted"), so a large video takes as long to restore
// as it would to copy.
func Restore(e Entry, destDir string) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(destDir, e.Name)
	ext := filepath.Ext(e.Name)
	stem := strings.TrimSuffix(e.Name, ext)
	for i := 1; ; i++ {
		if _, err := os.Lstat(dest); os.IsNotExist(err) {
			break
		}
		suffix := " (restored)"
		if i > 1 {
			suffix = fmt.Sprintf(" (restored %d)", i)
		}
		dest = filepath.Join(destDir, stem+suffix+ext)
	}
	if err := copyFile(e.Path, dest); err != nil {
		return "", fmt.Errorf("bringing back %s: %w", e.Name, err)
	}
	os.Chmod(dest, 0o644)
	now := time.Now()
	os.Chtimes(dest, now, now)
	return dest, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
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

// Dir returns the folder a restored file should go to: where the user is
// standing if that is inside the drive, otherwise the top of the drive.
func Dir(store *history.Store, cwd string) string {
	if rel, err := store.Rel(filepath.Join(cwd, "x")); err == nil {
		return filepath.Join(store.Root, filepath.Dir(rel))
	}
	return store.Root
}
