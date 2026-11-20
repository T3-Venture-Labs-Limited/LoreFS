package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
)

// cmdSelftest times the three things a person notices: listing a folder,
// opening a file for the first time, and opening it again. It works in a
// scratch folder inside the drive's bookkeeping and removes it afterwards.
func cmdSelftest(args []string) error {
	fs := flags("selftest")
	asJSON := fs.Bool("json", false, "output for programs")
	wait := fs.Duration("settle", 65*time.Second, "how long to wait so the listing is not answered from memory")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, store, err := openStore()
	if err != nil {
		return err
	}
	dir := filepath.Join(store.Root, history.MetaDir, "selftest-"+cfg.Machine)
	store.Cloner.RemoveTree(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	defer store.Cloner.RemoveTree(dir)
	say := func(format string, a ...any) {
		if !*asJSON {
			fmt.Printf(format, a...)
		}
	}

	say("Preparing 100 small files and one 5 MB file on the drive...\n")
	listDir := filepath.Join(dir, "listing")
	if err := os.MkdirAll(listDir, 0o755); err != nil {
		return err
	}
	startWrite := time.Now()
	for i := 0; i < 100; i++ {
		if err := os.WriteFile(filepath.Join(listDir, fmt.Sprintf(".lorefs-selftest-%03d.md", i)), []byte("selftest\n"), 0o644); err != nil {
			return err
		}
	}
	writeSmall := time.Since(startWrite)
	big := filepath.Join(dir, ".lorefs-selftest-5mb.bin")
	buf := make([]byte, 5<<20)
	rand.Read(buf)
	startBig := time.Now()
	if err := os.WriteFile(big, buf, 0o644); err != nil {
		return err
	}
	writeBig := time.Since(startBig)

	j := jfs.NewExec()
	if err := j.Evict(big); err != nil {
		say("(could not clear the cache for the test file: %v)\n", err)
	}
	say("Waiting %s so the folder listing is fetched fresh...\n", wait.Round(time.Second))
	time.Sleep(*wait)

	// List the way a person or a file manager does: names with sizes and
	// times, through the system's own `ls`.
	startList := time.Now()
	if out, err := exec.Command("ls", "-la", listDir).CombinedOutput(); err != nil {
		return fmt.Errorf("listing the test folder: %v: %s", err, out)
	}
	listing := time.Since(startList)
	entries, err := os.ReadDir(listDir)
	if err != nil {
		return err
	}

	read := func() (time.Duration, error) {
		start := time.Now()
		f, err := os.Open(big)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		if _, err := io.Copy(io.Discard, f); err != nil {
			return 0, err
		}
		return time.Since(start), nil
	}
	first, err := read()
	if err != nil {
		return err
	}
	again, err := read()
	if err != nil {
		return err
	}

	type result struct {
		Machine       string  `json:"machine"`
		Time          string  `json:"time"`
		ListingMS     float64 `json:"listing_100_entries_ms"`
		FirstOpenMS   float64 `json:"first_open_5mb_ms"`
		RepeatOpenMS  float64 `json:"repeat_open_5mb_ms"`
		Write100MS    float64 `json:"write_100_small_files_ms"`
		Write5MBMS    float64 `json:"write_5mb_ms"`
		ListingOK     bool    `json:"listing_within_2s"`
		FirstOpenOK   bool    `json:"first_open_within_3s"`
		RepeatOpenOK  bool    `json:"repeat_open_within_500ms"`
		EntriesListed int     `json:"entries_listed"`
	}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	res := result{Machine: cfg.Machine, Time: time.Now().UTC().Format(time.RFC3339),
		ListingMS: ms(listing), FirstOpenMS: ms(first), RepeatOpenMS: ms(again),
		Write100MS: ms(writeSmall), Write5MBMS: ms(writeBig),
		ListingOK: listing <= 2*time.Second, FirstOpenOK: first <= 3*time.Second, RepeatOpenOK: again <= 500*time.Millisecond,
		EntriesListed: len(entries)}
	if *asJSON {
		if err := printJSON(res); err != nil {
			return err
		}
	} else {
		mark := func(ok bool) string {
			if ok {
				return "✓"
			}
			return "✗"
		}
		fmt.Println()
		fmt.Printf("  %s List a folder of 100 files      %7.0f ms   (target: under 2000)\n", mark(res.ListingOK), res.ListingMS)
		fmt.Printf("  %s Open a 5 MB file, first time    %7.0f ms   (target: under 3000)\n", mark(res.FirstOpenOK), res.FirstOpenMS)
		fmt.Printf("  %s Open the same file again        %7.0f ms   (target: under 500)\n", mark(res.RepeatOpenOK), res.RepeatOpenMS)
		fmt.Printf("    Save 100 small files            %7.0f ms\n", res.Write100MS)
		fmt.Printf("    Save a 5 MB file                %7.0f ms\n", res.Write5MBMS)
	}
	if !res.ListingOK || !res.FirstOpenOK || !res.RepeatOpenOK {
		return exitCode(1)
	}
	return nil
}
