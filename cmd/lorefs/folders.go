package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/sandbox"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/status"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/ui"
)

// folderSize adds up the files in a folder of the drive.
func folderSize(dir string) (int64, int) {
	var total int64
	files := 0
	jfs.Walk(dir, func(_ string, info os.FileInfo) error {
		if info.IsDir() {
			if info.Name() == history.MetaDir || info.Name() == ".trash" {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
			files++
		}
		return nil
	})
	return total, files
}

func pinArg(args []string, verb string) (config.Config, *history.Store, string, error) {
	cfg, store, err := openStore()
	if err != nil {
		return cfg, nil, "", err
	}
	if len(args) != 1 {
		return cfg, nil, "", errUsage("say which folder, for example: lorefs " + verb + " notes")
	}
	rel, err := relInDrive(store, args[0])
	if err != nil {
		return cfg, nil, "", err
	}
	return cfg, store, filepath.ToSlash(rel), nil
}

func cmdPin(args []string) error {
	cfg, store, rel, err := pinArg(args, "pin")
	if err != nil {
		return err
	}
	abs := filepath.Join(store.Root, rel)
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a folder on the drive", rel)
	}
	pins, err := config.Pins()
	if err != nil {
		return err
	}
	for _, p := range pins {
		if p == rel {
			fmt.Printf("%s is already pinned.\n", rel)
			return nil
		}
	}
	// Everything pinned must fit in the cache, or pinned content would
	// keep pushing itself out.
	size, files := folderSize(abs)
	var already int64
	for _, p := range pins {
		s, _ := folderSize(filepath.Join(store.Root, p))
		already += s
	}
	limit := int64(cfg.CacheLimitMB) << 20
	if already+size > limit {
		fmt.Printf("Cannot pin %s: it holds %s", rel, status.Size(size))
		if already > 0 {
			fmt.Printf(", and %s is already pinned", status.Size(already))
		}
		fmt.Printf(", but the local cache is limited to %s.\n", status.Size(limit))
		fmt.Println("To allow more, raise cache_limit_mb in ~/.config/lorefs/config.toml and run:")
		fmt.Println("  systemctl --user restart lorefs-mount")
		return exitCode(1)
	}
	if err := config.SavePins(append(pins, rel)); err != nil {
		return err
	}
	fmt.Printf("Pinned %s (%d files, %s). Downloading in the background; `lorefs pins` shows progress.\n", rel, files, status.Size(size))
	return warmInBackground(abs)
}

// warmInBackground starts caching a folder and returns at once.
func warmInBackground(abs string) error {
	return jfs.NewExec().WarmBackground(abs)
}

func cmdUnpin(args []string) error {
	_, _, rel, err := pinArg(args, "unpin")
	if err != nil {
		return err
	}
	pins, err := config.Pins()
	if err != nil {
		return err
	}
	var keep []string
	found := false
	for _, p := range pins {
		if p == rel {
			found = true
		} else {
			keep = append(keep, p)
		}
	}
	if !found {
		fmt.Printf("%s was not pinned.\n", rel)
		return nil
	}
	if err := config.SavePins(keep); err != nil {
		return err
	}
	fmt.Printf("Unpinned %s. Its files stay cached until the space is needed for something else.\n", rel)
	return nil
}

func cmdPins(args []string) error {
	fs := flags("pins")
	asJSON := fs.Bool("json", false, "output for programs")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	_, store, err := openStore()
	if err != nil {
		return err
	}
	pins, err := config.Pins()
	if err != nil {
		return err
	}
	type row struct {
		Folder  string  `json:"folder"`
		Size    int64   `json:"size"`
		Files   int     `json:"files"`
		Cached  float64 `json:"cached_percent"`
		Missing bool    `json:"missing,omitempty"`
	}
	rows := []row{}
	j := jfs.NewExec()
	for _, p := range pins {
		abs := filepath.Join(store.Root, p)
		r := row{Folder: p}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			r.Missing = true
		} else {
			r.Size, r.Files = folderSize(abs)
			r.Cached = 100
			if r.Size > 0 {
				if pct, err := j.CachedPercent(abs); err == nil {
					r.Cached = pct
				}
			}
		}
		rows = append(rows, r)
	}
	if *asJSON {
		return printJSON(rows)
	}
	if len(rows) == 0 {
		fmt.Println("No folders are pinned on this computer.")
		fmt.Println("Small files (notes, documents) are kept ready automatically. To keep a whole")
		fmt.Println("folder ready, including large files: lorefs pin <folder>")
		return nil
	}
	fmt.Printf("%-40s %10s %7s  %s\n", "folder", "size", "files", "ready on this computer")
	for _, r := range rows {
		if r.Missing {
			fmt.Printf("%-40s %10s %7s  %s\n", r.Folder, "-", "-", "folder no longer exists (lorefs unpin to remove)")
			continue
		}
		fmt.Printf("%-40s %10s %7d  %.0f%%\n", r.Folder, status.Size(r.Size), r.Files, r.Cached)
	}
	return nil
}

func cmdSandbox(args []string) error {
	if len(args) == 0 {
		return errUsage("use: lorefs sandbox create <folder> [name] | list | diff <name> | apply <name> | remove <name>")
	}
	sub, rest := args[0], args[1:]
	fs := flags("sandbox " + sub)
	asJSON := fs.Bool("json", false, "output for programs")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	plain, err := parse(fs, rest)
	if err != nil {
		return err
	}
	_, store, err := openStore()
	if err != nil {
		return err
	}
	plainErr := func(err error) error {
		for _, known := range []error{sandbox.ErrBadName, sandbox.ErrExists, sandbox.ErrNotFound} {
			if errors.Is(err, known) {
				return errors.New(known.Error() + ". Run `lorefs sandbox list` to see the sandboxes")
			}
		}
		return err
	}

	switch sub {
	case "create":
		if len(plain) < 1 || len(plain) > 2 {
			return errUsage("use: lorefs sandbox create <folder> [name]")
		}
		rel, err := relInDrive(store, plain[0])
		if err != nil {
			return err
		}
		name := ""
		if len(plain) == 2 {
			name = plain[1]
		}
		info, err := sandbox.Create(store, rel, name)
		if err != nil {
			return plainErr(err)
		}
		if *asJSON {
			info.Files = nil
			return printJSON(info)
		}
		fmt.Printf("Sandbox %q is ready. Work in this folder:\n\n  %s\n\n", info.Name, info.Path)
		fmt.Printf("It is a private copy of %s and takes no extra storage. The original is not affected.\n", rel)
		fmt.Printf("When done:  lorefs sandbox diff %s   then   lorefs sandbox apply %s   or   lorefs sandbox remove %s\n", info.Name, info.Name, info.Name)
		return nil

	case "list":
		list, err := sandbox.List(store.Root)
		if err != nil {
			return err
		}
		if *asJSON {
			if list == nil {
				list = []sandbox.Info{}
			}
			return printJSON(list)
		}
		if len(list) == 0 {
			fmt.Println("There are no sandboxes.")
			return nil
		}
		now := time.Now()
		fmt.Printf("%-28s %-30s %s\n", "name", "copy of", "created")
		for _, s := range list {
			fmt.Printf("%-28s %-30s %s\n", s.Name, s.Source, ui.When(s.Created, now))
		}
		return nil

	case "diff":
		if len(plain) != 1 {
			return errUsage("use: lorefs sandbox diff <name>")
		}
		info, changes, err := sandbox.Diff(store.Root, plain[0])
		if err != nil {
			return plainErr(err)
		}
		if *asJSON {
			if changes == nil {
				changes = []sandbox.Change{}
			}
			return printJSON(changes)
		}
		if len(changes) == 0 {
			fmt.Printf("Nothing was added or changed in sandbox %q.\n", info.Name)
			return nil
		}
		sort.Slice(changes, func(a, b int) bool { return changes[a].Path < changes[b].Path })
		fmt.Printf("Added or changed in sandbox %q (a copy of %s):\n\n", info.Name, info.Source)
		for _, c := range changes {
			kind := "changed"
			if c.Added {
				kind = "added"
			}
			note := ""
			if c.OriginalChanged {
				note = "   (the original also changed since the sandbox was made)"
			}
			fmt.Printf("  %-8s %s%s\n", kind, c.Path, note)
		}
		fmt.Println("\nFiles deleted in the sandbox are not listed and are never deleted from the original.")
		return nil

	case "apply":
		if len(plain) != 1 {
			return errUsage("use: lorefs sandbox apply <name>")
		}
		info, changes, err := sandbox.Diff(store.Root, plain[0])
		if err != nil {
			return plainErr(err)
		}
		if len(changes) == 0 {
			fmt.Printf("Nothing was added or changed in sandbox %q, so there is nothing to copy back.\n", info.Name)
			return nil
		}
		if !*yes && ui.IsTerminal(os.Stdin) {
			q := fmt.Sprintf("Copy %d added or changed file(s) from sandbox %q into %s? Replaced files keep their previous content in history.",
				len(changes), info.Name, info.Source)
			if !ui.Confirm(q) {
				fmt.Println("Nothing was changed.")
				return nil
			}
		}
		applied, err := sandbox.Apply(store, plain[0])
		if err != nil {
			return plainErr(err)
		}
		fmt.Printf("Copied %d file(s) into %s. The sandbox still exists; remove it with: lorefs sandbox remove %s\n",
			len(applied), info.Source, info.Name)
		return nil

	case "remove":
		if len(plain) != 1 {
			return errUsage("use: lorefs sandbox remove <name>")
		}
		if err := sandbox.Remove(store.Root, plain[0], store.Cloner); err != nil {
			return plainErr(err)
		}
		fmt.Printf("Removed sandbox %q. The original folder was not touched.\n", plain[0])
		return nil
	}
	return errUsage("use: lorefs sandbox create <folder> [name] | list | diff <name> | apply <name> | remove <name>")
}
