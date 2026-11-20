package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/status"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/ui"
)

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

type versionJSON struct {
	Number  int       `json:"number"`
	Name    string    `json:"name"`
	Saved   time.Time `json:"saved"`
	Machine string    `json:"machine"`
	Size    int64     `json:"size"`
	Path    string    `json:"path"`
}

func cmdHistory(args []string) error {
	fs := flags("history")
	asJSON := fs.Bool("json", false, "output for programs")
	list := fs.Bool("list", false, "print the list instead of the interactive view")
	plain, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(plain) != 1 {
		return errUsage("say which file, for example: lorefs history notes/plan.md")
	}
	_, store, err := openStore()
	if err != nil {
		return err
	}
	rel, err := relInDrive(store, plain[0])
	if err != nil {
		return err
	}
	versions, err := store.List(rel)
	if err != nil {
		return err
	}
	deletedAt, deleted := store.DeletedAt(rel)
	now := time.Now()

	if *asJSON {
		out := struct {
			File      string        `json:"file"`
			Deleted   bool          `json:"deleted"`
			DeletedAt *time.Time    `json:"deleted_at,omitempty"`
			Versions  []versionJSON `json:"versions"`
		}{File: filepath.ToSlash(rel), Deleted: deleted, Versions: []versionJSON{}}
		if deleted {
			out.DeletedAt = &deletedAt
		}
		for i, v := range versions {
			out.Versions = append(out.Versions, versionJSON{i + 1, v.Name, v.Time, v.Machine, v.Size, v.Path})
		}
		return printJSON(out)
	}

	if len(versions) == 0 {
		fmt.Printf("%s has no earlier versions.\n", rel)
		switch {
		case store.Skip.Media(filepath.Base(rel)):
			fmt.Println("Video files are not versioned, because they are large and get replaced rather than edited.")
			fmt.Println("If one was deleted or replaced in the last 7 days, find it with: lorefs trash")
		case store.Skip.Path(rel):
			fmt.Println("Files with this kind of name are not recorded (see docs/history.md).")
		}
		return nil
	}

	interactive := !*list && ui.IsTerminal(os.Stdin) && ui.IsTerminal(os.Stdout)
	if !interactive {
		if deleted {
			fmt.Printf("%s was deleted %s. Its versions:\n\n", rel, status.Ago(now.Sub(deletedAt)))
		} else {
			fmt.Printf("Versions of %s (newest first)\n\n", rel)
		}
		fmt.Printf("%3s  %-34s %-14s %10s\n", "#", "saved", "by", "size")
		for i, v := range versions {
			fmt.Println(ui.VersionLine(i+1, v, now))
		}
		fmt.Printf("\nTo go back to one: lorefs restore %q <number>\n", plain[0])
		return nil
	}

	self, _ := os.Executable()
	n := ui.PickVersion(self, filepath.Join(store.Root, rel), versions, now)
	if n == 0 {
		fmt.Println("Nothing was changed.")
		return nil
	}
	v := versions[n-1]
	question := fmt.Sprintf("Restore %s to the version saved %s by %s? The current content is kept in history.",
		filepath.Base(rel), ui.When(v.Time, now), v.Machine)
	if deleted {
		question = fmt.Sprintf("Bring back %s as it was saved %s by %s?", filepath.Base(rel), ui.When(v.Time, now), v.Machine)
	}
	if !ui.Confirm(question) {
		fmt.Println("Nothing was changed.")
		return nil
	}
	if err := store.Restore(rel, v); err != nil {
		return err
	}
	fmt.Printf("Restored %s. To undo, open the history again and pick the version from just before.\n", rel)
	return nil
}

// cmdHistoryPreview is what the interactive view shows beside a version.
func cmdHistoryPreview(args []string) error {
	if len(args) != 2 {
		return errUsage("internal command")
	}
	_, store, err := openStore()
	if err != nil {
		return err
	}
	rel, err := relInDrive(store, args[0])
	if err != nil {
		return err
	}
	v, err := store.Find(rel, args[1])
	if err != nil {
		return err
	}
	now := time.Now()
	fmt.Printf("Saved %s by %s · %s\n\n", ui.When(v.Time, now), v.Machine, status.Size(v.Size))
	current := filepath.Join(store.Root, rel)
	if _, err := os.Stat(current); err != nil {
		fmt.Println("The file was deleted. Restoring brings it back with this content.")
		if ui.IsText(v.Path) {
			fmt.Println()
			data, _ := os.ReadFile(v.Path)
			if len(data) > 64*1024 {
				data = data[:64*1024]
			}
			os.Stdout.Write(data)
		}
		return nil
	}
	fmt.Println("Restoring would change the current file like this:")
	fmt.Println()
	return ui.Diff(os.Stdout, current, "current", v.Path, "this version", true)
}

func cmdDiff(args []string) error {
	fs := flags("diff")
	plain, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(plain) < 1 || len(plain) > 3 {
		return errUsage("use: lorefs diff <file> [older version number] [newer version number]")
	}
	_, store, err := openStore()
	if err != nil {
		return err
	}
	rel, err := relInDrive(store, plain[0])
	if err != nil {
		return err
	}
	versions, err := store.List(rel)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		fmt.Printf("%s has no earlier versions to compare with.\n", rel)
		return nil
	}
	current := filepath.Join(store.Root, rel)
	color := ui.IsTerminal(os.Stdout)
	label := func(n int) string {
		return fmt.Sprintf("version %d (%s, %s)", n, versions[n-1].Time.Local().Format("2 Jan 15:04"), versions[n-1].Machine)
	}
	switch len(plain) {
	case 1:
		// What did the latest change do? The newest version normally holds
		// the current content, so compare the one before it with now.
		_, statErr := os.Stat(current)
		newest := versions[0]
		st, _ := os.Stat(current)
		sameAsNewest := statErr == nil && st.Size() == newest.Size && st.ModTime().Sub(newest.ModTime).Abs() < time.Millisecond
		if sameAsNewest && len(versions) == 1 {
			fmt.Printf("%s has only one version, which is its current content.\n", rel)
			return nil
		}
		from := 1
		if sameAsNewest {
			from = 2
		}
		return ui.Diff(os.Stdout, versions[from-1].Path, label(from), current, "current", color)
	case 2:
		v, err := store.Find(rel, plain[1])
		if err != nil {
			return err
		}
		return ui.Diff(os.Stdout, v.Path, "version "+plain[1], current, "current", color)
	default:
		a, err := store.Find(rel, plain[1])
		if err != nil {
			return err
		}
		b, err := store.Find(rel, plain[2])
		if err != nil {
			return err
		}
		return ui.Diff(os.Stdout, a.Path, "version "+plain[1], b.Path, "version "+plain[2], color)
	}
}

func cmdRestore(args []string) error {
	fs := flags("restore")
	plain, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(plain) != 2 {
		return errUsage("use: lorefs restore <file> <version number>   (see `lorefs history <file>` for the numbers)")
	}
	_, store, err := openStore()
	if err != nil {
		return err
	}
	rel, err := relInDrive(store, plain[0])
	if err != nil {
		return err
	}
	v, err := store.Find(rel, plain[1])
	if err != nil {
		return err
	}
	if err := store.Restore(rel, v); err != nil {
		return err
	}
	fmt.Printf("Restored %s to the version saved %s by %s. The content it replaced is kept in history.\n",
		rel, ui.When(v.Time, time.Now()), v.Machine)
	return nil
}

func cmdOpen(args []string) error {
	fs := flags("open")
	printPath := fs.Bool("path", false, "print the version's location instead of opening it")
	plain, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(plain) != 2 {
		return errUsage("use: lorefs open <file> <version number>")
	}
	_, store, err := openStore()
	if err != nil {
		return err
	}
	rel, err := relInDrive(store, plain[0])
	if err != nil {
		return err
	}
	v, err := store.Find(rel, plain[1])
	if err != nil {
		return err
	}
	if *printPath {
		fmt.Println(v.Path)
		return nil
	}
	fmt.Printf("Opening the version saved %s (read-only). The current file is not changed.\n", ui.When(v.Time, time.Now()))
	return exec.Command("xdg-open", v.Path).Start()
}

func cmdLog(args []string) error {
	fs := flags("log")
	asJSON := fs.Bool("json", false, "output for programs")
	days := fs.Int("days", 7, "how many days back")
	limit := fs.Int("n", 50, "how many lines at most")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	_, store, err := openStore()
	if err != nil {
		return err
	}
	events, err := history.ReadJournals(store.Root, time.Now().Add(-time.Duration(*days)*24*time.Hour))
	if err != nil {
		return err
	}
	// Leave out the notes machines write for each other about folders.
	kept := events[:0]
	for _, e := range events {
		if !e.FolderNote() {
			kept = append(kept, e)
		}
	}
	events = kept
	if len(events) > *limit {
		events = events[:*limit]
	}
	if *asJSON {
		if events == nil {
			events = []history.Event{}
		}
		return printJSON(events)
	}
	if len(events) == 0 {
		fmt.Printf("Nothing was saved, deleted or restored in the last %d days.\n", *days)
		return nil
	}
	words := map[string]string{"save": "saved", "delete": "deleted", "rename": "moved", "restore": "restored"}
	now := time.Now()
	for _, e := range events {
		what := e.Path
		if e.Op == "rename" {
			what = e.Path + " → " + e.To
		}
		fmt.Printf("%-34s %-14s %-9s %s\n", ui.When(e.Time, now), e.Machine, words[e.Op], what)
	}
	return nil
}
