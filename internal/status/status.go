// Package status gathers the health of the drive on this machine and
// explains it in plain language.
package status

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/index"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/peers"
)

// Level is the outcome of one check.
type Level int

const (
	OK Level = iota
	Warn
	Fail
)

func (l Level) String() string {
	return [...]string{"ok", "warning", "failed"}[l]
}

// MarshalJSON writes the level as a word.
func (l Level) MarshalJSON() ([]byte, error) { return json.Marshal(l.String()) }

// Check is one line of the status page.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Level  Level  `json:"level"`
	Detail string `json:"detail"`
	Next   string `json:"next_step,omitempty"`
}

// Inputs are the facts the checks are decided from. Collect fills them
// from the real machine; tests fill them by hand.
type Inputs struct {
	Now    time.Time
	Folder string

	Mounted bool

	IndexErr error
	// IndexRejected: the index answered but refused this machine's password.
	IndexRejected bool
	IndexRTT      time.Duration
	MemUsed       int64
	MemLimit      int64
	BackupAt      time.Time
	HasBackup     bool
	// The copy of file contents to a second provider, if the drive has one.
	FileBackup    index.FileBackup
	HasFileBackup bool

	StorageName string // empty when unknown (not mounted)
	StorageErr  error

	CacheUsed   int64
	CacheLimit  int64
	DiskFreePct float64 // free space on the disk holding the cache; <0 unknown

	HelperRunning bool
	HelperSince   time.Time
	LastVersion   time.Time
	// HelperReady is false while the helper is still looking at every
	// folder after starting; HelperWatched is how many it has looked at.
	HelperReady   bool
	HelperWatched int

	// The program on this computer, and what the computers on the drive
	// say they run (this one included, once its helper has started).
	Machine    string
	Version    string
	CommitTime int64
	Peers      []peers.Note
}

// Report is the whole status page.
type Report struct {
	Joined   bool              `json:"joined"`
	Drive    string            `json:"drive,omitempty"`
	Machine  string            `json:"machine,omitempty"`
	Folder   string            `json:"folder,omitempty"`
	Overall  Level             `json:"overall"`
	Checks   []Check           `json:"checks"`
	Programs map[string]string `json:"programs"`
	Time     time.Time         `json:"time"`
}

// Evaluate turns facts into checks.
func Evaluate(in Inputs) []Check {
	var out []Check
	add := func(c Check) { out = append(out, c) }

	// Drive mounted.
	if in.Mounted {
		add(Check{ID: "drive", Title: "Drive", Level: OK, Detail: "mounted at " + in.Folder})
	} else {
		add(Check{ID: "drive", Title: "Drive", Level: Fail,
			Detail: "The shared drive is not connected on this computer, so its folder is empty and read-only.",
			Next:   "It reconnects on its own when it can. To try now: systemctl --user restart lorefs-mount"})
	}

	// Index.
	switch {
	case in.IndexRejected:
		add(Check{ID: "index", Title: "Index", Level: Fail,
			Detail: "The drive no longer accepts this computer's join code, because the drive's password was changed. Files cannot be opened or saved.",
			Next:   "Ask for a new join code, then run: lorefs leave --yes --keep-cache   and   lorefs join"})
	case in.IndexErr != nil:
		add(Check{ID: "index", Title: "Index", Level: Fail,
			Detail: "The drive's index cannot be reached (" + in.IndexErr.Error() + "). Files cannot be opened or saved until it can.",
			Next:   "Check your internet connection first. If other sites work, the index service may be down."})
	case in.IndexRTT > 400*time.Millisecond:
		add(Check{ID: "index", Title: "Index", Level: Warn,
			Detail: fmt.Sprintf("reachable, but slow: %d ms per request. Browsing folders will feel sluggish.", in.IndexRTT.Milliseconds()),
			Next:   "Usually a slow or busy internet connection, or a VPN that sends traffic the long way round. If you use a VPN, let connections to the index bypass it."})
	default:
		add(Check{ID: "index", Title: "Index", Level: OK, Detail: fmt.Sprintf("reachable, %d ms per request", in.IndexRTT.Milliseconds())})
	}

	// Storage.
	switch {
	case in.StorageName == "":
		add(Check{ID: "storage", Title: "Storage", Level: Warn, Detail: "not checked, because the drive is not mounted"})
	case in.StorageErr != nil:
		add(Check{ID: "storage", Title: "Storage", Level: Fail,
			Detail: "The file storage cannot be reached (" + in.StorageErr.Error() + "). Files already opened recently still work; others do not.",
			Next:   "Check your internet connection."})
	default:
		add(Check{ID: "storage", Title: "Storage", Level: OK, Detail: "reachable (" + in.StorageName + ")"})
	}

	// Local cache.
	cache := Check{ID: "cache", Title: "Local cache", Level: OK,
		Detail: fmt.Sprintf("%s of %s used", Size(in.CacheUsed), Size(in.CacheLimit))}
	if in.DiskFreePct >= 0 && in.DiskFreePct < 10 {
		cache.Level = Warn
		cache.Detail += fmt.Sprintf(". The disk holding the cache has only %.0f%% free.", in.DiskFreePct)
		cache.Next = "Free some disk space, or lower cache_limit_mb in ~/.config/lorefs/config.toml."
	}
	add(cache)

	// Version recorder.
	if in.HelperRunning && !in.HelperReady {
		// Starting: it looks at every folder first, which takes seconds
		// on a nearby index and minutes on a slow connection.
		c := Check{ID: "history", Title: "Version history", Level: OK,
			Detail: fmt.Sprintf("starting: %d folders looked at so far. Saves made meanwhile are recorded once it is ready.", in.HelperWatched)}
		if d := in.Now.Sub(in.HelperSince); d > 5*time.Minute {
			c.Level = Warn
			c.Detail = fmt.Sprintf("still starting after %d minutes (%d folders looked at so far). Saves made meanwhile are recorded once it is ready.", int(d.Minutes()), in.HelperWatched)
			c.Next = "It takes this long only when the connection to the index is slow; see the Index line above."
		}
		add(c)
	} else if in.HelperRunning {
		detail := "recording versions"
		if !in.LastVersion.IsZero() {
			detail += ", last one " + Ago(in.Now.Sub(in.LastVersion))
		} else {
			detail += ", none yet since it started"
		}
		add(Check{ID: "history", Title: "Version history", Level: OK, Detail: detail})
	} else {
		add(Check{ID: "history", Title: "Version history", Level: Warn,
			Detail: "The version recorder is not running. Changes made on this computer are not being recorded in history.",
			Next:   "Start it with: systemctl --user restart lorefs-helper"})
	}

	// Index backup. A machine that has only just connected is given time
	// before a stale backup counts against it.
	settled := in.Mounted && !in.HelperSince.IsZero() && in.Now.Sub(in.HelperSince) > 30*time.Minute
	switch {
	case in.IndexErr != nil:
		add(Check{ID: "backup", Title: "Index backup", Level: Warn, Detail: "not checked, because the index cannot be reached"})
	case !in.HasBackup && settled && in.Now.Sub(in.HelperSince) > 2*time.Hour:
		add(Check{ID: "backup", Title: "Index backup", Level: Warn,
			Detail: "No backup of the index has been written yet.",
			Next:   "Backups are written hourly while the drive is mounted. If this stays, tell whoever looks after the drive."})
	case !in.HasBackup:
		add(Check{ID: "backup", Title: "Index backup", Level: OK, Detail: "none yet; the first is written within an hour of mounting"})
	case settled && in.Now.Sub(in.BackupAt) > 3*time.Hour:
		add(Check{ID: "backup", Title: "Index backup", Level: Warn,
			Detail: "The latest backup of the index is " + Age(in.Now.Sub(in.BackupAt)) + " old. It should be less than an hour or two.",
			Next:   "If this stays for several hours, tell whoever looks after the drive."})
	default:
		add(Check{ID: "backup", Title: "Index backup", Level: OK, Detail: "latest " + Ago(in.Now.Sub(in.BackupAt))})
	}

	// Copy of the files to a second provider (nightly).
	if in.IndexErr == nil && in.HasFileBackup {
		b := in.FileBackup
		age := in.Now.Sub(b.Time)
		switch {
		case !b.OK:
			add(Check{ID: "file-backup", Title: "File backup", Level: Warn,
				Detail: "The last nightly copy of the files to the second storage (" + Ago(age) + ") failed: " + b.Error,
				Next:   "The drive itself works. Tell whoever looks after the drive; until it is fixed, files saved since the last good copy have only one copy."})
		case age > 36*time.Hour:
			add(Check{ID: "file-backup", Title: "File backup", Level: Warn,
				Detail: "The latest copy of the files to the second storage is " + Age(age) + " old. It should run every night.",
				Next:   "The drive itself works. Tell whoever looks after the drive."})
		default:
			add(Check{ID: "file-backup", Title: "File backup", Level: OK,
				Detail: fmt.Sprintf("latest copy %s (%d files, %s)", Ago(age), b.Objects, Size(b.Bytes))})
		}
	}

	// Index memory.
	if in.IndexErr == nil {
		switch {
		case in.MemLimit <= 0:
			add(Check{ID: "index-memory", Title: "Index memory", Level: OK, Detail: Size(in.MemUsed) + " used (no limit set)"})
		case float64(in.MemUsed) > 0.8*float64(in.MemLimit):
			add(Check{ID: "index-memory", Title: "Index memory", Level: Warn,
				Detail: fmt.Sprintf("%s of %s used (%d%%). When it is full, no new files can be created.",
					Size(in.MemUsed), Size(in.MemLimit), int(100*in.MemUsed/in.MemLimit)),
				Next: "Tell whoever looks after the drive, so the limit can be raised."})
		default:
			add(Check{ID: "index-memory", Title: "Index memory", Level: OK,
				Detail: fmt.Sprintf("%s of %s used (%d%%)", Size(in.MemUsed), Size(in.MemLimit), int(100*in.MemUsed/in.MemLimit))})
		}
	}
	if in.Version != "" {
		add(programCheck(in))
	}
	return out
}

// peerStale is how long a computer can be silent before its version no
// longer counts: it may have left, or been switched off for good.
const peerStale = 30 * 24 * time.Hour

// programCheck compares this computer's version of lorefs with the others'.
// Different versions work on one drive (see README, "Updating"), but a
// fix only helps on the computers that have it, so the page says when
// this computer is behind.
func programCheck(in Inputs) Check {
	c := Check{ID: "program", Title: "Program", Level: OK}
	var others, newer, older []peers.Note
	for _, p := range in.Peers {
		if p.Machine == in.Machine || in.Now.Sub(p.Seen) > peerStale {
			continue
		}
		others = append(others, p)
		switch {
		case p.Version == in.Version:
		case p.CommitTime > 0 && in.CommitTime > 0 && p.CommitTime > in.CommitTime:
			newer = append(newer, p)
		default:
			older = append(older, p)
		}
	}
	describe := func(ps []peers.Note) string {
		var parts []string
		for _, p := range ps {
			s := p.Machine + " (" + p.Version
			if p.CommitTime > 0 {
				s += ", from " + time.Unix(p.CommitTime, 0).UTC().Format("2 Jan")
			}
			parts = append(parts, s+")")
		}
		return strings.Join(parts, ", ")
	}
	switch {
	case len(newer) > 0:
		c.Level = Warn
		c.Detail = "This computer runs lorefs " + in.Version + ". A newer version is in use on: " + describe(newer) + "."
		c.Next = "Update this computer with: lorefs update"
	case len(older) > 0:
		c.Detail = "lorefs " + in.Version + ". Not yet updated: " + describe(older) + "; there, run: lorefs update"
	case len(others) == 0:
		c.Detail = "lorefs " + in.Version + " (no other computer has reported its version yet)"
	default:
		c.Detail = fmt.Sprintf("lorefs %s, the same on all %d computers", in.Version, len(others)+1)
	}
	return c
}

// Overall is the worst level among the checks.
func Overall(checks []Check) Level {
	worst := OK
	for _, c := range checks {
		if c.Level > worst {
			worst = c.Level
		}
	}
	return worst
}

// ExitCode is 0 when healthy, 1 with warnings only, 2 when something failed.
func (r Report) ExitCode() int { return int(r.Overall) }

// Text renders the report for a person.
func (r Report) Text() string {
	var sb strings.Builder
	if !r.Joined {
		sb.WriteString("This computer has not joined a LoreFS drive yet.\n\n")
		sb.WriteString("To join, ask someone who already has the drive to run `lorefs invite`\n")
		sb.WriteString("and send you the code it prints. Then run: lorefs join\n")
		return sb.String()
	}
	fmt.Fprintf(&sb, "LoreFS drive %q on %s\n\n", r.Drive, r.Machine)
	mark := map[Level]string{OK: "✓", Warn: "!", Fail: "✗"}
	for _, c := range r.Checks {
		fmt.Fprintf(&sb, "  %s %-16s %s\n", mark[c.Level], c.Title, c.Detail)
		if c.Next != "" {
			fmt.Fprintf(&sb, "    %-16s → %s\n", "", c.Next)
		}
	}
	sb.WriteString("\n")
	switch r.Overall {
	case OK:
		sb.WriteString("Everything is working.\n")
	case Warn:
		sb.WriteString("The drive works, with the warnings marked ! above.\n")
	default:
		sb.WriteString("The drive is not working properly. See the lines marked ✗ above.\n")
	}
	fmt.Fprintf(&sb, "\nlore %s · JuiceFS %s\n", r.Programs["lorefs"], r.Programs["juicefs"])
	return sb.String()
}

// Size renders a byte count for a person.
func Size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Age renders a duration as "5 minutes", for "is 5 minutes old".
func Age(d time.Duration) string {
	switch a := Ago(d); a {
	case "just now", "moments ago":
		return "a few seconds"
	default:
		return strings.TrimSuffix(a, " ago")
	}
}

// Ago renders a duration as "5 minutes ago".
func Ago(d time.Duration) string {
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "moments ago"
	case d < 2*time.Minute:
		return "1 minute ago"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 2*time.Hour:
		return "1 hour ago"
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
