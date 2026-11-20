package status

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/index"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/peers"
)

var now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

// healthy returns inputs where every check passes.
func healthy() Inputs {
	return Inputs{
		Now: now, Folder: "/home/alice/Lore", Mounted: true,
		IndexRTT: 42 * time.Millisecond, MemUsed: 50 << 20, MemLimit: 400 << 20,
		BackupAt: now.Add(-40 * time.Minute), HasBackup: true,
		StorageName: "lorefs.s3.example.com",
		CacheUsed:   3 << 30, CacheLimit: 50 << 30, DiskFreePct: 60,
		HelperRunning: true, HelperReady: true, HelperLogRead: 354,
		HelperSince: now.Add(-5 * time.Hour), LastVersion: now.Add(-3 * time.Minute),
	}
}

func find(t *testing.T, checks []Check, id string) Check {
	t.Helper()
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", id, checks)
	return Check{}
}

func TestHealthy(t *testing.T) {
	checks := Evaluate(healthy())
	if Overall(checks) != OK {
		t.Fatalf("expected all ok: %+v", checks)
	}
	for _, id := range []string{"drive", "index", "storage", "cache", "history", "backup", "index-memory"} {
		if find(t, checks, id).Level != OK {
			t.Errorf("%s should be ok", id)
		}
	}
	if !strings.Contains(find(t, checks, "index").Detail, "42 ms") {
		t.Error("index line should show the response time")
	}
	if !strings.Contains(find(t, checks, "history").Detail, "3 minutes ago") {
		t.Error("history line should show the last version time")
	}
}

func TestEachProblem(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*Inputs)
		id    string
		level Level
		words string
	}{
		{"not mounted", func(i *Inputs) { i.Mounted = false; i.StorageName = "" }, "drive", Fail, "not connected"},
		{"index down", func(i *Inputs) { i.IndexErr = errors.New("timeout") }, "index", Fail, "cannot be reached"},
		{"index slow", func(i *Inputs) { i.IndexRTT = 900 * time.Millisecond }, "index", Warn, "slow"},
		{"password changed", func(i *Inputs) { i.IndexErr = errors.New("rejected"); i.IndexRejected = true }, "index", Fail, "password was changed"},
		{"storage down", func(i *Inputs) { i.StorageErr = errors.New("no answer") }, "storage", Fail, "cannot be reached"},
		{"disk nearly full", func(i *Inputs) { i.DiskFreePct = 4 }, "cache", Warn, "4% free"},
		{"recorder stopped", func(i *Inputs) { i.HelperRunning = false }, "history", Warn, "not being recorded"},
		{"stale backup", func(i *Inputs) { i.BackupAt = now.Add(-5 * time.Hour) }, "backup", Warn, "is 5 hours old"},
		{"no backup after hours", func(i *Inputs) { i.HasBackup = false }, "backup", Warn, "No backup"},
		{"memory high", func(i *Inputs) { i.MemUsed = 350 << 20 }, "index-memory", Warn, "87%"},
	}
	for _, tc := range cases {
		in := healthy()
		tc.mut(&in)
		checks := Evaluate(in)
		c := find(t, checks, tc.id)
		if c.Level != tc.level {
			t.Errorf("%s: level %v, want %v (%s)", tc.name, c.Level, tc.level, c.Detail)
		}
		if !strings.Contains(c.Detail, tc.words) {
			t.Errorf("%s: detail %q should mention %q", tc.name, c.Detail, tc.words)
		}
		if c.Level != OK && c.Next == "" && tc.id != "storage" {
			t.Errorf("%s: a problem needs a next step", tc.name)
		}
		if Overall(checks) < tc.level {
			t.Errorf("%s: overall should be at least %v", tc.name, tc.level)
		}
	}
}

func TestFreshMountIsNotBlamedForOldBackup(t *testing.T) {
	in := healthy()
	in.HelperSince = now.Add(-5 * time.Minute)
	in.BackupAt = now.Add(-48 * time.Hour)
	if c := find(t, Evaluate(in), "backup"); c.Level != OK {
		t.Fatalf("a machine that just connected should not warn yet: %+v", c)
	}
	in.HasBackup = false
	if c := find(t, Evaluate(in), "backup"); c.Level != OK {
		t.Fatalf("no backup right after the first mount is normal: %+v", c)
	}
}

func TestExitCodesAndText(t *testing.T) {
	r := Report{Joined: true, Drive: "lorefs", Machine: "alice", Checks: Evaluate(healthy()),
		Programs: map[string]string{"lorefs": "test", "juicefs": "1.4.2"}}
	r.Overall = Overall(r.Checks)
	if r.ExitCode() != 0 || !strings.Contains(r.Text(), "Everything is working") {
		t.Fatalf("healthy report: exit %d\n%s", r.ExitCode(), r.Text())
	}
	in := healthy()
	in.HelperRunning = false
	r.Checks = Evaluate(in)
	r.Overall = Overall(r.Checks)
	if r.ExitCode() != 1 {
		t.Fatalf("warnings only should exit 1, got %d", r.ExitCode())
	}
	in.IndexErr = errors.New("timeout")
	r.Checks = Evaluate(in)
	r.Overall = Overall(r.Checks)
	if r.ExitCode() != 2 {
		t.Fatalf("a failure should exit 2, got %d", r.ExitCode())
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if json.Unmarshal(data, &back) != nil || back["overall"] != "failed" {
		t.Fatalf("json: %s", data)
	}
}

func TestNotJoinedText(t *testing.T) {
	r := Report{}
	if !strings.Contains(r.Text(), "has not joined") || !strings.Contains(r.Text(), "lorefs join") {
		t.Fatalf("unexpected text: %s", r.Text())
	}
}

func TestSizeAndAgo(t *testing.T) {
	if Size(512) != "512 B" || Size(1536) != "1.5 KiB" || Size(3<<30) != "3.0 GiB" {
		t.Fatalf("sizes: %s %s %s", Size(512), Size(1536), Size(3<<30))
	}
	if Ago(30*time.Second) != "moments ago" || Ago(26*time.Hour) != "26 hours ago" || Ago(72*time.Hour) != "3 days ago" {
		t.Fatal("ago strings")
	}
}

func TestProgramVersionAcrossComputers(t *testing.T) {
	const mine, mineAt = "3ac46d7", int64(1795080076)
	note := func(machine, version string, at int64, seen time.Duration) peers.Note {
		return peers.Note{Machine: machine, Version: version, CommitTime: at, Seen: now.Add(-seen)}
	}
	self := note("alice", mine, mineAt, time.Hour)
	cases := []struct {
		name  string
		peers []peers.Note
		level Level
		words []string
		next  string
	}{
		{"alone", []peers.Note{self}, OK, []string{"lorefs 3ac46d7", "no other computer"}, ""},
		{"same everywhere", []peers.Note{self, note("bob", mine, mineAt, time.Hour)}, OK,
			[]string{"the same on all 2 computers"}, ""},
		{"another is newer", []peers.Note{self, note("bob", "abc1234", mineAt+86400, time.Hour)}, Warn,
			[]string{"newer version", "bob (abc1234, from 20 Nov)", "runs lorefs 3ac46d7"}, "lorefs update"},
		{"another is older", []peers.Note{self, note("bob", "9a93131", mineAt-9000, time.Hour)}, OK,
			[]string{"Not yet updated: bob (9a93131", "lorefs update"}, ""},
		{"different, order unknown", []peers.Note{self, note("bob", "3ac46d7-dirty", mineAt, time.Hour)}, OK,
			[]string{"bob (3ac46d7-dirty"}, ""},
		{"a newer one that has been silent for months does not count",
			[]peers.Note{self, note("old-laptop", "abc1234", mineAt+86400, 60*24*time.Hour)}, OK,
			[]string{"no other computer"}, ""},
		{"this computer's own stale note is ignored", []peers.Note{note("alice", "0000000", mineAt+86400, time.Hour)}, OK,
			[]string{"lorefs 3ac46d7"}, ""},
	}
	for _, tc := range cases {
		in := healthy()
		in.Machine, in.Version, in.CommitTime, in.Peers = "alice", mine, mineAt, tc.peers
		checks := Evaluate(in)
		c := find(t, checks, "program")
		if c.Level != tc.level {
			t.Errorf("%s: level %v, want %v (%s)", tc.name, c.Level, tc.level, c.Detail)
		}
		for _, w := range tc.words {
			if !strings.Contains(c.Detail, w) {
				t.Errorf("%s: %q should mention %q", tc.name, c.Detail, w)
			}
		}
		if !strings.Contains(c.Next, tc.next) || (tc.next == "" && c.Next != "") {
			t.Errorf("%s: next step %q, want it to contain %q", tc.name, c.Next, tc.next)
		}
		if tc.level == OK && Overall(checks) != OK {
			t.Errorf("%s: a version difference that needs nothing here must not spoil a healthy page", tc.name)
		}
	}
	// Without a known version (old callers, tests) there is no line at all.
	for _, c := range Evaluate(healthy()) {
		if c.ID == "program" {
			t.Error("no program line expected when the version is unknown")
		}
	}
}

func TestHelperStillStarting(t *testing.T) {
	in := healthy()
	in.HelperReady, in.HelperLogRead, in.HelperSince = false, 120, now.Add(-40*time.Second)
	checks := Evaluate(in)
	c := find(t, checks, "history")
	if c.Level != OK || !strings.Contains(c.Detail, "starting: 120 changes") || !strings.Contains(c.Detail, "recorded once it is ready") {
		t.Fatalf("a helper that has just started is not a problem: %+v", c)
	}
	if Overall(checks) != OK {
		t.Fatal("a starting helper must not spoil a healthy page")
	}
	// On a slow connection it can take many minutes; then say so.
	in.HelperSince = now.Add(-9 * time.Minute)
	c = find(t, Evaluate(in), "history")
	if c.Level != Warn || !strings.Contains(c.Detail, "still starting after 9 minutes (120 changes") || !strings.Contains(c.Next, "Index line") {
		t.Fatalf("a helper starting for 9 minutes should be explained: %+v", c)
	}
}

func TestFileBackup(t *testing.T) {
	// A drive without a backup job shows no line for it.
	for _, c := range Evaluate(healthy()) {
		if c.ID == "file-backup" {
			t.Fatal("no file-backup line expected without a backup job")
		}
	}
	in := healthy()
	in.HasFileBackup = true
	in.FileBackup = index.FileBackup{Time: now.Add(-7 * time.Hour), OK: true, Objects: 8224, Bytes: 3319225390}
	c := find(t, Evaluate(in), "file-backup")
	if c.Level != OK || !strings.Contains(c.Detail, "7 hours ago (8224 files, 3.1 GiB)") {
		t.Fatalf("healthy backup: %+v", c)
	}
	in.FileBackup.Time = now.Add(-50 * time.Hour)
	if c := find(t, Evaluate(in), "file-backup"); c.Level != Warn || !strings.Contains(c.Detail, "is 2 days old") {
		t.Fatalf("stale backup: %+v", c)
	}
	in.FileBackup = index.FileBackup{Time: now.Add(-2 * time.Hour), OK: false, Error: "Backblaze refused more data: its spending limit was reached"}
	c = find(t, Evaluate(in), "file-backup")
	if c.Level != Warn || !strings.Contains(c.Detail, "spending limit") || !strings.Contains(c.Next, "drive itself works") {
		t.Fatalf("failed backup: %+v", c)
	}
}

func TestChangeLogLine(t *testing.T) {
	in := healthy()
	if len(Evaluate(in)) == 0 || func() bool {
		for _, c := range Evaluate(in) {
			if c.ID == "change-log" {
				return true
			}
		}
		return false
	}() {
		t.Fatal("no change-log line when the drive's settings are unknown")
	}
	in.DriveKnown, in.ChangeLogOn, in.ChangeLogMaxAge = true, true, 24*time.Hour
	c := find(t, Evaluate(in), "change-log")
	if c.Level != OK || !strings.Contains(c.Detail, "24 hours") {
		t.Fatalf("%+v", c)
	}
	in.ChangeLogOn = false
	c = find(t, Evaluate(in), "change-log")
	if c.Level != Warn || c.Next == "" {
		t.Fatalf("%+v", c)
	}
}

// A trial drive has no server: the lines that need one are not shown or
// are plainly marked, and nothing fails because they are missing.
func TestTrialDriveHasNoServerChecks(t *testing.T) {
	in := healthy()
	in.LocalIndex = true
	in.IndexRTT, in.MemUsed, in.MemLimit, in.HasBackup = 0, 0, 0, false
	in.DriveKnown, in.ChangeLogOn, in.ChangeLogMaxAge = true, true, 24*time.Hour
	checks := Evaluate(in)
	if Overall(checks) != OK {
		t.Fatalf("a healthy trial drive should be ok: %+v", checks)
	}
	if !strings.Contains(find(t, checks, "index").Detail, "trial drive") {
		t.Error("the index line should say it is a trial drive")
	}
	for _, c := range checks {
		if c.ID == "index-memory" || c.ID == "file-backup" {
			t.Errorf("%s does not apply to a trial drive", c.ID)
		}
	}
}

func TestRulesFileFromAnOlderTemplateIsMentionedNotChanged(t *testing.T) {
	in := healthy()
	in.RulesKnown, in.RulesVersion, in.RulesCurrent = true, 1, 2
	checks := Evaluate(in)
	c := find(t, checks, "rules")
	if c.Level != OK || !strings.Contains(c.Detail, "older template") || !strings.Contains(c.Next, "lorefs rules diff") {
		t.Fatalf("%+v", c)
	}
	if Overall(checks) != OK {
		t.Fatal("an older template is information, not a warning")
	}
	in.RulesVersion = 2
	for _, c := range Evaluate(in) {
		if c.ID == "rules" {
			t.Fatal("no line when the file is current")
		}
	}
}
