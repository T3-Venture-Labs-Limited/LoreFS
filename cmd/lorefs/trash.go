package main

import (
	"fmt"
	"os"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/status"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/trash"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/ui"
)

// cmdTrash lists files deleted or replaced in the last 7 days and brings
// one back. It covers what version history skips on purpose, such as
// video files.
func cmdTrash(args []string) error {
	fs := flags("trash")
	asJSON := fs.Bool("json", false, "output for programs")
	to := fs.String("to", "", "folder to put the restored file in (default: where you are, or the top of the drive)")
	limit := fs.Int("n", 40, "how many lines at most")
	plain, err := parse(fs, args)
	if err != nil {
		return err
	}
	_, store, err := openStore()
	if err != nil {
		return err
	}

	if len(plain) >= 1 && plain[0] == "restore" {
		if len(plain) != 2 {
			return errUsage("use: lorefs trash restore <file name>   (see `lorefs trash` for the names)")
		}
		e, ok, err := trash.Find(store.Root, plain[1])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no deleted file called %q was found. Deleted files are kept for 7 days; run `lorefs trash` to see them", plain[1])
		}
		dest := *to
		if dest == "" {
			cwd, _ := os.Getwd()
			dest = trash.Dir(store, cwd)
		} else if rel, err := relInDrive(store, dest); err == nil {
			dest = store.Root + string(os.PathSeparator) + rel
		} else {
			return err
		}
		path, err := trash.Restore(e, dest)
		if err != nil {
			return err
		}
		fmt.Printf("Brought back %s (deleted %s) as:\n  %s\n", e.Name, ui.When(e.Deleted, time.Now()), path)
		return nil
	}

	filter := ""
	if len(plain) == 1 {
		filter = plain[0]
	} else if len(plain) > 1 {
		return errUsage("use: lorefs trash [part of a file name]   or   lorefs trash restore <file name>")
	}
	entries, err := trash.List(store.Root, filter)
	if err != nil {
		return err
	}
	total := len(entries)
	if total > *limit {
		entries = entries[:*limit]
	}
	if *asJSON {
		if entries == nil {
			entries = []trash.Entry{}
		}
		return printJSON(entries)
	}
	if total == 0 {
		if filter != "" {
			fmt.Printf("No deleted file with %q in its name was found. Deleted files are kept for 7 days.\n", filter)
		} else {
			fmt.Println("No deleted files. Files deleted or replaced on the drive are kept here for 7 days.")
		}
		return nil
	}
	now := time.Now()
	fmt.Println("Deleted or replaced in the last 7 days (newest first):")
	fmt.Println()
	fmt.Printf("%-34s %10s  %s\n", "deleted", "size", "name")
	for _, e := range entries {
		fmt.Printf("%-34s %10s  %s\n", ui.When(e.Deleted, now), status.Size(e.Size), e.Name)
	}
	if total > len(entries) {
		fmt.Printf("\n(%d more; narrow it down with: lorefs trash <part of the name>)\n", total-len(entries))
	}
	fmt.Println("\nTo bring one back: lorefs trash restore <name>")
	return nil
}
