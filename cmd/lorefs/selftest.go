package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/index"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/mount"
)

// indexRequest times one request to the index (the second, so that setting up
// the connection is not counted). It returns 0 when that cannot be done.
func indexRequest(cfg config.Config) time.Duration {
	if cfg.IsLocal() {
		return 0
	}
	target, err := mount.Target(cfg)
	if err != nil {
		return 0
	}
	conn, err := index.Dial(target, 10*time.Second)
	if err != nil {
		return 0
	}
	defer conn.Close()
	conn.Ping()
	d, err := conn.Ping()
	if err != nil {
		return 0
	}
	return d
}

// cmdSelftest times the three things a person notices: listing a folder,
// opening a file for the first time, and opening it again. It works in a
// scratch folder inside the drive's bookkeeping and removes it afterwards.
func cmdSelftest(args []string) error {
	fs := flags("selftest")
	asJSON := fs.Bool("json", false, "output for programs")
	wait := fs.Duration("settle", 65*time.Second, "how long to wait so the listing is not answered from memory")
	small := fs.Int("small", 100, "how many small files to write (fewer makes the test quicker, and its results less comparable)")
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

	say("Preparing %d small files and one 5 MB file on the drive...\n", *small)
	listDir := filepath.Join(dir, "listing")
	if err := os.MkdirAll(listDir, 0o755); err != nil {
		return err
	}
	startWrite := time.Now()
	for i := 0; i < *small; i++ {
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
		// IndexMS is the time of one request to the index; 0 for a trial
		// drive, which has no server.
		IndexMS float64 `json:"index_request_ms"`
	}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	res := result{Machine: cfg.Machine, Time: time.Now().UTC().Format(time.RFC3339),
		ListingMS: ms(listing), FirstOpenMS: ms(first), RepeatOpenMS: ms(again),
		Write100MS: ms(writeSmall), Write5MBMS: ms(writeBig),
		ListingOK: listing <= 2*time.Second, FirstOpenOK: first <= 3*time.Second, RepeatOpenOK: again <= 500*time.Millisecond,
		EntriesListed: len(entries)}
	res.IndexMS = ms(indexRequest(cfg))
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
		if res.IndexMS > 0 {
			fmt.Printf("    One request to the index        %7.0f ms\n", res.IndexMS)
		}
		if res.IndexMS > 150 {
			fmt.Println()
			missed := !res.ListingOK || !res.FirstOpenOK || !res.RepeatOpenOK
			if missed {
				fmt.Printf("The distance to the index is the cause: every request to it takes %.0f ms here, and listing a folder\n", res.IndexMS)
				fmt.Println("or opening a file takes several.")
			} else {
				fmt.Printf("The distance to the index is long: every request to it takes %.0f ms here. The targets were met, but folders\n", res.IndexMS)
				fmt.Println("with many files will feel slow, and saving files takes several requests each.")
			}
			fmt.Println("A VPN that sends traffic the long way round, or an index on another continent, does this.")
			fmt.Println("Let connections to the index bypass the VPN, or run the index nearer.")
		}
	}
	if !res.ListingOK || !res.FirstOpenOK || !res.RepeatOpenOK {
		return exitCode(1)
	}
	return nil
}
