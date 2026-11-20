package status

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/helper"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/index"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/mount"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/peers"
)

// Collect inspects this machine and returns the full report. commitTime
// is when the running version was committed (seconds since 1970, 0 when
// unknown).
func Collect(loreVersion string, commitTime int64) Report {
	r := Report{Time: time.Now(), Programs: map[string]string{
		"lorefs": loreVersion, "juicefs": jfs.NewExec().Version(),
	}}
	cfg, err := config.Load()
	if err != nil {
		if !errors.Is(err, config.ErrNotJoined) {
			r.Joined = true
			r.Checks = []Check{{ID: "settings", Title: "Settings", Level: Fail,
				Detail: "The LoreFS settings on this computer cannot be read: " + err.Error(),
				Next:   "Fix ~/.config/lorefs/config.toml, or run `lorefs leave` and join again."}}
			r.Overall = Fail
		}
		return r
	}
	r.Joined, r.Drive, r.Machine, r.Folder = true, cfg.DriveName, cfg.Machine, cfg.Folder
	in := gather(cfg)
	in.Machine, in.Version, in.CommitTime = cfg.Machine, loreVersion, commitTime
	if in.Mounted {
		in.Peers = peers.Read(cfg.Folder)
	}
	r.Checks = Evaluate(in)
	r.Overall = Overall(r.Checks)
	return r
}

func gather(cfg config.Config) Inputs {
	in := Inputs{Now: time.Now(), Folder: cfg.Folder, DiskFreePct: -1,
		CacheLimit: int64(cfg.CacheLimitMB) << 20}

	in.Mounted = jfs.IsMounted(cfg.Folder)

	// Index: one connection for the response time, memory and backup age.
	target, err := mount.Target(cfg)
	if err != nil {
		in.IndexErr = errors.New("the saved connection details are missing")
	} else if conn, err := index.Dial(target, 8*time.Second); err != nil {
		in.IndexErr = err
		in.IndexRejected = errors.Is(err, index.ErrPassword)
	} else {
		in.IndexRTT, in.IndexErr = conn.Ping()
		if in.IndexErr == nil {
			// The first request also pays for connection setup; measure again.
			if d, err := conn.Ping(); err == nil {
				in.IndexRTT = d
			}
			in.MemUsed, in.MemLimit, _ = conn.Memory()
			in.BackupAt, in.HasBackup, _ = conn.LastBackup()
			in.FileBackup, in.HasFileBackup, _ = conn.LastFileBackup()
		}
		conn.Close()
	}

	// Storage and cache need the mounted drive to say where they are.
	cacheDir := cfg.CacheDir
	if in.Mounted {
		if mi, err := jfs.ReadMountInfo(cfg.Folder); err == nil {
			in.StorageName, in.StorageErr = probeStorage(mi)
			if mi.CacheDir != "" {
				cacheDir = mi.CacheDir
			}
		}
	}
	in.CacheUsed = dirSize(cacheDir)
	var fs syscall.Statfs_t
	if syscall.Statfs(cfg.CacheDir, &fs) == nil && fs.Blocks > 0 {
		in.DiskFreePct = 100 * float64(fs.Bavail) / float64(fs.Blocks)
	}

	hb, fresh := helper.ReadHeartbeat()
	in.HelperRunning = fresh
	if fresh {
		in.HelperSince, in.LastVersion = hb.Started, hb.LastVersion
		in.HelperReady, in.HelperWatched = hb.Ready, hb.Watched
	}
	return in
}

// probeStorage checks that the place holding file contents answers.
func probeStorage(mi jfs.MountInfo) (name string, err error) {
	name = mi.Storage
	if mi.Storage == "file" {
		_, err = os.Stat(mi.Bucket)
		return "local folder", err
	}
	u, perr := url.Parse(mi.Bucket)
	if perr != nil || u.Host == "" {
		return name, nil // unknown address form: nothing to probe
	}
	name = u.Host
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Head(u.Scheme + "://" + u.Host + "/")
	if err != nil {
		return name, fmt.Errorf("no answer from %s", u.Host)
	}
	resp.Body.Close() // any HTTP answer, even "forbidden", means it is reachable
	return name, nil
}

func dirSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
