// Package sandbox gives an agent a private copy of a folder to work in.
// The copy is a clone, so it stores no duplicate data. Nothing done
// inside it reaches the original until `apply` copies the results back.
package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
)

// entry is the state of one file when the sandbox was made.
type entry struct {
	Size     int64 `json:"size"`
	MTime    int64 `json:"mtime"`     // of the copy in the sandbox
	SrcMTime int64 `json:"src_mtime"` // of the original
}

// Info describes a sandbox.
type Info struct {
	Name    string           `json:"name"`
	Source  string           `json:"source"` // folder it was made from, relative to the drive
	Created time.Time        `json:"created"`
	Path    string           `json:"path"`
	Files   map[string]entry `json:"files,omitempty"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// Errors with plain-language text.
var (
	ErrBadName  = errors.New("a sandbox name may use lower-case letters, digits, - and _")
	ErrExists   = errors.New("a sandbox with that name already exists")
	ErrNotFound = errors.New("there is no sandbox with that name")
)

func dir(root string) string { return filepath.Join(root, history.MetaDir, "sandboxes") }

func paths(root, name string) (folder, meta string) {
	return filepath.Join(dir(root), name), filepath.Join(dir(root), name+".json")
}

// Create clones the folder srcRel into a new sandbox and returns it.
func Create(s *history.Store, srcRel, name string) (Info, error) {
	var info Info
	srcAbs := filepath.Join(s.Root, srcRel)
	st, err := os.Stat(srcAbs)
	if err != nil || !st.IsDir() {
		return info, fmt.Errorf("%s is not a folder on the drive", srcRel)
	}
	if name == "" {
		name = history.CleanMachine(filepath.Base(srcRel)) + "-" + s.Now().UTC().Format("20060102-150405")
	}
	if !nameRe.MatchString(name) {
		return info, ErrBadName
	}
	folder, meta := paths(s.Root, name)
	if _, err := os.Lstat(folder); err == nil {
		return info, ErrExists
	}
	if err := os.MkdirAll(dir(s.Root), 0o755); err != nil {
		return info, err
	}
	if err := s.Cloner.Clone(srcAbs, folder); err != nil {
		return info, fmt.Errorf("creating the sandbox: %w", err)
	}
	info = Info{Name: name, Source: filepath.ToSlash(srcRel), Created: s.Now().UTC(), Path: folder, Files: map[string]entry{}}
	err = walk(folder, func(rel string, fi os.FileInfo) {
		e := entry{Size: fi.Size(), MTime: fi.ModTime().UnixNano()}
		if src, err := os.Lstat(filepath.Join(srcAbs, rel)); err == nil {
			e.SrcMTime = src.ModTime().UnixNano()
		}
		info.Files[filepath.ToSlash(rel)] = e
	})
	if err != nil {
		return info, err
	}
	data, _ := json.Marshal(info)
	if err := os.WriteFile(meta, data, 0o644); err != nil {
		return info, err
	}
	return info, nil
}

func walk(folder string, fn func(rel string, fi os.FileInfo)) error {
	return jfs.Walk(folder, func(p string, fi os.FileInfo) error {
		if !fi.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(folder, p)
		fn(rel, fi)
		return nil
	})
}

// Load reads a sandbox's record.
func Load(root, name string) (Info, error) {
	var info Info
	if !nameRe.MatchString(name) {
		return info, ErrBadName
	}
	folder, meta := paths(root, name)
	data, err := os.ReadFile(meta)
	if err != nil {
		return info, ErrNotFound
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, err
	}
	// The record lives on the shared drive, where anyone can edit it. Its
	// source must be a folder of the drive, or apply could write outside.
	src := filepath.Clean(filepath.FromSlash(info.Source))
	first, _, _ := strings.Cut(filepath.ToSlash(src), "/")
	if info.Source == "" || filepath.IsAbs(src) || src == "." || first == ".." || first == history.MetaDir || first == ".trash" {
		return info, fmt.Errorf("the record of sandbox %q names %q as its source, which is not a folder on the drive", name, info.Source)
	}
	info.Source = filepath.ToSlash(src)
	info.Path = folder
	return info, nil
}

// List returns every sandbox on the drive, newest first, without the
// per-file records.
func List(root string) ([]Info, error) {
	entries, err := os.ReadDir(dir(root))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		if info, err := Load(root, name); err == nil {
			info.Files = nil
			out = append(out, info)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Created.After(out[b].Created) })
	return out, nil
}

// Change is one file that differs from the state at creation.
type Change struct {
	Path  string `json:"path"` // relative to the sandbox and to its source folder
	Added bool   `json:"added"`
	// OriginalChanged: the original file was also changed after the
	// sandbox was made, so applying replaces someone else's newer work.
	OriginalChanged bool `json:"original_changed,omitempty"`
}

// Diff lists the files added or changed inside the sandbox.
func Diff(root, name string) (Info, []Change, error) {
	info, err := Load(root, name)
	if err != nil {
		return info, nil, err
	}
	srcAbs := filepath.Join(root, filepath.FromSlash(info.Source))
	var out []Change
	err = walk(info.Path, func(rel string, fi os.FileInfo) {
		key := filepath.ToSlash(rel)
		was, known := info.Files[key]
		if known && was.Size == fi.Size() && was.MTime == fi.ModTime().UnixNano() {
			return
		}
		c := Change{Path: key, Added: !known}
		if src, err := os.Lstat(filepath.Join(srcAbs, rel)); err == nil {
			if known {
				c.OriginalChanged = src.ModTime().UnixNano() != was.SrcMTime || src.Size() != was.Size
			} else {
				c.OriginalChanged = true // a file of that name appeared in the original meanwhile
			}
		}
		out = append(out, c)
	})
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return info, out, err
}

// Apply copies the added and changed files back into the original
// folder. It deletes nothing. Each file it replaces keeps its previous
// content in version history.
func Apply(s *history.Store, name string) ([]Change, error) {
	info, changes, err := Diff(s.Root, name)
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
		rel := filepath.Join(filepath.FromSlash(info.Source), filepath.FromSlash(c.Path))
		if err := s.ReplaceWith(rel, filepath.Join(info.Path, filepath.FromSlash(c.Path))); err != nil {
			return changes, fmt.Errorf("copying %s back: %w", c.Path, err)
		}
	}
	return changes, s.Journal.Flush()
}

// Remove deletes a sandbox. The original folder is not touched.
func Remove(root, name string, rm jfs.Cloner) error {
	if !nameRe.MatchString(name) {
		return ErrBadName
	}
	folder, meta := paths(root, name)
	if _, err := os.Stat(meta); err != nil {
		return ErrNotFound
	}
	// Folders in a sandbox may be read-only; make sure they can be emptied.
	os.Chmod(folder, 0o755)
	jfs.Walk(folder, func(p string, info os.FileInfo) error {
		if info.IsDir() && info.Mode().Perm()&0o300 != 0o300 {
			os.Chmod(p, 0o755)
		}
		return nil
	})
	if err := rm.RemoveTree(folder); err != nil {
		return err
	}
	return os.Remove(meta)
}
