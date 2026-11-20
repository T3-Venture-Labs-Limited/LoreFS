package jfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// ReadDir lists a folder on the drive with each entry's details, sorted
// by name.
//
// It asks for the details of each entry while it is still reading the
// folder, the way `ls -l` does. That matters on the drive: the kernel then
// fetches names and details together, a few hundred entries per request
// to the index. Reading all the names first and asking afterwards, which
// is what os.ReadDir followed by Info does, costs one request per entry
// (measured on the real drive: 607 entries in 0.4 s against 43 ms each).
func ReadDir(dir string) ([]os.FileInfo, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	infos, err := f.Readdir(-1)
	sort.Slice(infos, func(a, b int) bool { return infos[a].Name() < infos[b].Name() })
	if len(infos) > 0 {
		err = nil // entries that vanished while reading are not a failure
	}
	return infos, err
}

// walkWorkers is how many folders are read at the same time. Reading one
// folder on the drive takes about 0.16 s whatever its size (four requests
// to the index, one after another), so a walk that reads folders one at a
// time needs a minute for 350 folders. Reading them side by side brings
// that to a few seconds, which is why `rg` and `fd` are fast on the drive
// and `find` is not.
const walkWorkers = 64

// Walk calls fn for every file and folder below root, reading several
// folders at once. fn is never called from two places at the same time,
// and a folder is always reported before anything inside it, but
// otherwise the order is not defined.
//
// Returning filepath.SkipDir for a folder leaves its content out; any
// other error stops the walk and is returned. Folders that cannot be read
// are passed over, except root itself.
func Walk(root string, fn func(path string, info os.FileInfo) error) error {
	return WalkWith(root, WalkOptions{}, fn)
}

// WalkOptions are the extras of WalkWith.
type WalkOptions struct {
	// Ctx stops the walk when it ends. Folders being read at that moment
	// are finished (a few requests to the index each); no more are begun.
	Ctx context.Context
	// Enter is called for each folder below root (not for root itself)
	// just before the folder is read. Unlike fn it is called from several
	// places at the same time: it is for work that costs a request to the
	// index for every folder, which must not be done one folder after
	// another on a slow connection.
	Enter func(dir string)
	// Strict stops the walk at the first folder that cannot be read and
	// returns that error. Without it such folders are passed over, which
	// is right for a search and wrong for anything that concludes from
	// the walk that a file is gone.
	Strict bool
}

// WalkWith is Walk with options.
func WalkWith(root string, opt WalkOptions, fn func(path string, info os.FileInfo) error) error {
	if opt.Ctx == nil {
		opt.Ctx = context.Background()
	}
	infos, err := ReadDir(root)
	if err != nil {
		return err
	}
	w := &walker{fn: fn, opt: opt, sem: make(chan struct{}, walkWorkers)}
	w.wg.Add(1)
	w.visit(root, infos)
	w.wg.Wait()
	if w.err == nil {
		w.err = opt.Ctx.Err()
	}
	return w.err
}

type walker struct {
	fn  func(string, os.FileInfo) error
	opt WalkOptions
	sem chan struct{}
	wg  sync.WaitGroup
	mu  sync.Mutex // serialises fn and guards err
	err error
}

func (w *walker) visit(dir string, infos []os.FileInfo) {
	defer w.wg.Done()
	var subdirs []string
	w.mu.Lock()
	for _, info := range infos {
		if w.err == nil {
			w.err = w.opt.Ctx.Err()
		}
		if w.err != nil {
			break
		}
		p := filepath.Join(dir, info.Name())
		err := w.fn(p, info)
		switch {
		case err == filepath.SkipDir:
		case err != nil:
			w.err = err
		case info.IsDir():
			subdirs = append(subdirs, p)
		}
	}
	stopped := w.err != nil
	w.mu.Unlock()
	if stopped {
		return
	}
	for _, p := range subdirs {
		w.wg.Add(1)
		go func(p string) {
			w.sem <- struct{}{}
			// Many folders may have been waiting here; once the walk has
			// been stopped none of them is read any more.
			w.mu.Lock()
			stopped := w.err != nil || w.opt.Ctx.Err() != nil
			w.mu.Unlock()
			if stopped {
				<-w.sem
				w.wg.Done()
				return
			}
			if w.opt.Enter != nil {
				w.opt.Enter(p)
			}
			infos, err := ReadDir(p)
			<-w.sem
			if err != nil {
				if w.opt.Strict && len(infos) == 0 && !os.IsNotExist(err) {
					w.mu.Lock()
					if w.err == nil {
						w.err = fmt.Errorf("reading %s: %w", p, err)
					}
					w.mu.Unlock()
				}
				w.wg.Done()
				return
			}
			w.visit(p, infos)
		}(p)
	}
}

// RemoveTree deletes a folder and everything in it.
func (Copy) RemoveTree(path string) error { return os.RemoveAll(path) }

// RemoveTree deletes a folder and everything in it with `juicefs rmr`,
// which works through the folder with many requests at once. Deleting
// file by file took 104 s for 282 files on the real drive. If the folder
// is not on the drive, or the command fails, it falls back to deleting
// file by file.
func (e Exec) RemoveTree(path string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	}
	if _, err := e.run("rmr", path); err == nil {
		return nil
	}
	return os.RemoveAll(path)
}
