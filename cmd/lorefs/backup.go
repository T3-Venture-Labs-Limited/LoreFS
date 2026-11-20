package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/backup"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/index"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/mount"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/restore"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/server"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/status"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/ui"
)

const backupHelp = `lorefs backup keeps a second copy of the drive's files at another provider.

  lorefs backup setup --bucket <address> --access-key <key>   check and save the destination
  lorefs backup run                                           copy now
  lorefs backup schedule [--off] [--system]                   copy every night
  lorefs backup status                                        show the last result

The secret key is asked for, or read from LOREFS_BACKUP_SECRET; it is never
taken from the command line. The destination should keep what is removed
from it for a while (bucket versioning, or a rule such as Backblaze B2's).
`

func cmdBackup(args []string) error {
	if len(args) == 0 {
		return errUsage("use: lorefs backup setup|run|schedule|status\n\n" + backupHelp)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "setup":
		return backupSetup(rest)
	case "run":
		return backupRun(rest)
	case "schedule":
		return backupSchedule(rest)
	case "status":
		return backupStatus(rest)
	}
	return errUsage("there is no `lorefs backup " + sub + "`\n\n" + backupHelp)
}

func backupSetup(args []string) error {
	fs := flags("backup setup")
	bucket := fs.String("bucket", "", "address of the destination bucket, e.g. https://my-backup.s3.us-west-004.backblazeb2.com")
	accessKey := fs.String("access-key", "", "the destination's access key (or LOREFS_BACKUP_KEY)")
	region := fs.String("region", "", "the destination's region, if it cannot be read from the address")
	driveKey := fs.String("drive-access-key", "", "the drive's own storage access key (or LOREFS_STORAGE_KEY); JuiceFS keeps its secret key encrypted, so it is asked for once")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ak := *accessKey
	if ak == "" {
		ak = os.Getenv("LOREFS_BACKUP_KEY")
	}
	sk := os.Getenv("LOREFS_BACKUP_SECRET")
	dak := *driveKey
	if dak == "" {
		dak = os.Getenv("LOREFS_STORAGE_KEY")
	}
	dsk := os.Getenv("LOREFS_STORAGE_SECRET")
	if ui.IsTerminal(os.Stdin) {
		if *bucket == "" {
			*bucket = ui.Ask("Destination bucket address", "")
		}
		if ak == "" {
			ak = ui.Ask("Destination access key", "")
		}
		if sk == "" {
			if sk, err = ui.AskSecret("Destination secret key (not shown)"); err != nil {
				return err
			}
			sk = strings.TrimSpace(sk)
		}
		if dak == "" {
			dak = ui.Ask("The drive's own storage access key", "")
		}
		if dsk == "" {
			if dsk, err = ui.AskSecret("The drive's own storage secret key (not shown)"); err != nil {
				return err
			}
			dsk = strings.TrimSpace(dsk)
		}
	}
	if *bucket == "" || ak == "" || sk == "" || dak == "" || dsk == "" {
		return errUsage("missing --bucket, --access-key (or LOREFS_BACKUP_KEY), LOREFS_BACKUP_SECRET, --drive-access-key (or LOREFS_STORAGE_KEY) or LOREFS_STORAGE_SECRET\n\n" + backupHelp)
	}
	res, err := backup.Setup(context.Background(), cfg, backup.SetupOptions{Dest: *bucket, AccessKey: ak, SecretKey: sk,
		DriveAccessKey: dak, DriveSecretKey: dsk, Region: *region, Progress: func(s string) { fmt.Fprintln(os.Stderr, s) }})
	if err != nil {
		return err
	}
	fmt.Printf("The backup destination is saved: bucket %q.\n\n%s\n\nRun it now with `lorefs backup run`, and every night with `lorefs backup schedule`.\n", res.Bucket, res.KeepNote())
	return nil
}

func backupRun(args []string) error {
	fs := flags("backup run")
	quiet := fs.Bool("quiet", false, "print nothing unless it fails")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	say := func(s string) {
		if !*quiet {
			fmt.Fprintln(os.Stderr, s)
		}
	}
	r, err := backup.Run(context.Background(), cfg, backup.RunOptions{Progress: say})
	if err != nil {
		return err
	}
	if !r.OK {
		fmt.Fprintf(os.Stderr, "The backup failed: %s\n", r.Error)
		return exitCode(1)
	}
	if !*quiet {
		fmt.Printf("The backup is complete: %d objects, %s, at %s.\n", r.Objects, status.Size(r.Bytes), r.Destination)
	}
	return nil
}

func backupSchedule(args []string) error {
	fs := flags("backup schedule")
	off := fs.Bool("off", false, "stop the nightly backup")
	system := fs.Bool("system", false, "install for the whole computer (needs administrator rights), not just this user")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := backup.Schedule(backup.ScheduleOptions{Exe: exe, System: *system, Off: *off}); err != nil {
		return err
	}
	if *off {
		fmt.Println("The nightly backup is switched off.")
		return nil
	}
	fmt.Println("The backup will run every night at about 03:30. If this computer is off then, it runs when the computer is next on.")
	fmt.Println("To see the timer: systemctl " + map[bool]string{true: "", false: "--user "}[*system] + "list-timers lorefs-backup.timer")
	return nil
}

func backupStatus([]string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	target, err := mount.Target(cfg)
	if err != nil {
		return err
	}
	conn, err := index.Dial(target, 15*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	b, ok, err := conn.LastFileBackup()
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("No backup has reported yet.")
		return nil
	}
	age := status.Ago(time.Since(b.Time))
	if b.OK {
		fmt.Printf("The last backup was %s: %d objects, %s, at %s.\n", age, b.Objects, status.Size(b.Bytes), b.Destination)
	} else {
		fmt.Printf("The last backup was %s and failed: %s\n", age, b.Error)
		return exitCode(1)
	}
	return nil
}

const restoreHelp = `lorefs restore-index rebuilds a drive's index from a copy kept in a bucket.

  lorefs restore-index --list                    show the copies in the drive's bucket
  lorefs restore-index --drill                   rehearse into a throwaway index on this computer
  lorefs restore-index --index rediss://...      restore into a new, empty index
  --copy <name>        use this copy instead of the newest
  --from-backup        take the copies and the file contents from the backup bucket
  --bucket, --access-key   the drive's bucket (secret key in LOREFS_STORAGE_SECRET),
                           needed unless --from-backup
  --server-dir <folder>    the new index is a bundle made by lorefs server init
  --index-password        no: the password is read from LOREFS_INDEX_PASSWORD

File contents are never touched by a restore: only the map from names to
stored blocks is rebuilt. Changes made after the copy was written (at most
about an hour) are not in it.
`

func cmdRestoreIndex(args []string) error {
	fs := flags("restore-index")
	list := fs.Bool("list", false, "show the copies available")
	drill := fs.Bool("drill", false, "rehearse the restore into a throwaway index")
	copyName := fs.String("copy", "", "use this copy instead of the newest")
	fromBackup := fs.Bool("from-backup", false, "use the backup bucket")
	bucket := fs.String("bucket", "", "the drive's bucket")
	accessKey := fs.String("access-key", "", "the drive's storage access key (or LOREFS_STORAGE_KEY)")
	name := fs.String("name", "", "the drive's name (default: the one in the settings of this computer)")
	indexURL := fs.String("index", "", "address of the new, empty index")
	caFile := fs.String("index-ca-file", "", "certificate authority file for that index")
	serverDir := fs.String("server-dir", "", "an index bundle made by `lorefs server init`")
	fs.Usage = func() { fmt.Fprint(os.Stderr, restoreHelp); fs.PrintDefaults() }
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, cfgErr := config.Load()
	driveName := *name
	if driveName == "" && cfgErr == nil {
		driveName = cfg.DriveName
	}
	if driveName == "" {
		return errUsage("give the drive's name with --name\n\n" + restoreHelp)
	}
	src := restore.Source{Name: driveName}
	if *fromBackup {
		set, k, err := backup.Load()
		if err != nil {
			return err
		}
		src.Bucket, src.AccessKey, src.SecretKey = set.Dest, k.DestAccess, k.DestSecret
	} else {
		src.Bucket, src.AccessKey, src.SecretKey = *bucket, *accessKey, os.Getenv("LOREFS_STORAGE_SECRET")
		if src.AccessKey == "" {
			src.AccessKey = os.Getenv("LOREFS_STORAGE_KEY")
		}
		// Without flags, the drive's bucket and key that `lorefs backup setup` saved.
		if set, k, err := backup.Load(); err == nil {
			if src.Bucket == "" {
				src.Bucket = set.SourceBucket
			}
			if src.AccessKey == "" && src.SecretKey == "" {
				src.AccessKey, src.SecretKey = k.SourceAccess, k.SourceSecret
			}
		}
		if src.Bucket == "" || src.AccessKey == "" || src.SecretKey == "" {
			return errUsage("give --bucket, --access-key (or LOREFS_STORAGE_KEY) and LOREFS_STORAGE_SECRET, or use --from-backup\n\n" + restoreHelp)
		}
	}
	say := func(s string) { fmt.Fprintln(os.Stderr, s) }
	ctx := context.Background()
	opt := restore.Options{Source: src, Copy: *copyName, Progress: say}

	switch {
	case *list:
		names, err := restore.Copies(ctx, src)
		if err != nil {
			return err
		}
		fmt.Println("Index copies in the bucket (UTC time in the name, newest last):")
		for _, n := range names {
			fmt.Println("  " + n)
		}
		return nil
	case *drill:
		if cfgErr == nil && jfs.IsMounted(cfg.Folder) {
			opt.LiveFolder = cfg.Folder
		}
		res, err := restore.Drill(ctx, opt)
		if err != nil {
			return err
		}
		fmt.Printf("The restored copy %s lists %d files.\n", res.Copy, res.Files)
		if opt.LiveFolder != "" {
			fmt.Printf("Compared with the live drive at %s: %d identical, %d different, %d no longer on the live drive (of %d compared).\n",
				opt.LiveFolder, res.Same, res.Different, res.Missing, res.Compared)
			if res.Same == 0 && res.Compared > 0 {
				return errors.New("nothing matched. The copy may be unusable")
			}
		} else {
			fmt.Println("(No live drive is connected on this computer to compare with.)")
		}
		fmt.Println("Drill finished. Nothing in the real index or either bucket was changed, and nothing was left on this computer.")
		return nil
	}
	if *serverDir != "" {
		conn, err := server.Read(*serverDir)
		if err != nil {
			return err
		}
		*indexURL, opt.IndexPass, opt.IndexCA = conn.IndexURL(1), conn.Password, conn.CA
	} else {
		opt.IndexPass = os.Getenv("LOREFS_INDEX_PASSWORD")
		if *caFile != "" {
			b, err := os.ReadFile(*caFile)
			if err != nil {
				return err
			}
			opt.IndexCA = b
		}
	}
	if *indexURL == "" || opt.IndexPass == "" {
		return errUsage("give --index (or --server-dir) and LOREFS_INDEX_PASSWORD for the new index\n\n" + restoreHelp)
	}
	opt.IndexURL = *indexURL
	used, err := restore.Restore(ctx, opt)
	if err != nil {
		return err
	}
	fmt.Printf("The index was restored from %s. Join code for the computers: run `lorefs invite` on one that is connected, or\n", used)
	fmt.Println("make a new one by pointing a computer at this index. Then restart the drive on each computer: systemctl --user restart lorefs-mount")
	return nil
}
