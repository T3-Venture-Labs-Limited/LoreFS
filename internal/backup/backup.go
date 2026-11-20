// Package backup keeps a second copy of everything a drive stores, at
// another S3-compatible provider, and records how the last copy went where
// every computer's status page can read it.
//
// The copy is made by `juicefs sync` from the drive's bucket to a second
// bucket. What the second bucket does with objects that are removed is up
// to that bucket (versioning, or a lifecycle rule): setup says what it
// finds and how to turn it on.
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/create"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/index"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/mount"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/s3check"
)

// Settings describe the destination. The keys are kept apart, in a file
// only the user can read.
type Settings struct {
	Dest         string    `json:"dest_bucket"`
	SourceBucket string    `json:"source_bucket"` // the drive's own bucket
	Region       string    `json:"region,omitempty"`
	Added        time.Time `json:"added"`
}

// Keys are the storage keys the backup needs. JuiceFS keeps the drive's own
// secret key encrypted in the index, so it is asked for once, here, and kept
// with the destination's.
type Keys struct {
	DestAccess, DestSecret     string
	SourceAccess, SourceSecret string
}

// Paths of the files inside the settings folder.
func SettingsFile() string { return filepath.Join(config.Dir(), "backup.json") }
func SecretsFile() string  { return filepath.Join(config.Dir(), "backup.env") }

// ErrNotSetUp is returned when no backup was set up on this computer.
var ErrNotSetUp = errors.New("no backup is set up on this computer; run `lorefs backup setup`")

// Load reads the settings and the keys.
func Load() (s Settings, k Keys, err error) {
	data, err := os.ReadFile(SettingsFile())
	if err != nil {
		return s, k, ErrNotSetUp
	}
	if err = json.Unmarshal(data, &s); err != nil || s.Dest == "" {
		return s, k, fmt.Errorf("%s is unreadable", SettingsFile())
	}
	env, err := os.ReadFile(SecretsFile())
	if err != nil {
		return s, k, fmt.Errorf("the backup keys are missing (%s): run `lorefs backup setup` again", SecretsFile())
	}
	for _, line := range strings.Split(string(env), "\n") {
		line = strings.TrimSpace(line)
		for key, dst := range map[string]*string{"BACKUP_ACCESS_KEY=": &k.DestAccess, "BACKUP_SECRET_KEY=": &k.DestSecret,
			"DRIVE_ACCESS_KEY=": &k.SourceAccess, "DRIVE_SECRET_KEY=": &k.SourceSecret} {
			if v, ok := strings.CutPrefix(line, key); ok {
				*dst = v
			}
		}
	}
	if k.DestAccess == "" || k.DestSecret == "" || k.SourceAccess == "" || k.SourceSecret == "" {
		return s, k, fmt.Errorf("%s holds no keys: run `lorefs backup setup` again", SecretsFile())
	}
	return s, k, nil
}

// SetupOptions describe a destination to check and save.
type SetupOptions struct {
	Dest      string // bucket address
	AccessKey string
	SecretKey string
	// The drive's own storage key, which JuiceFS keeps encrypted in the index.
	DriveAccessKey string
	DriveSecretKey string
	Region         string // optional; read from the address when it names one
	Progress       func(string)
}

// SetupResult tells what setup found out.
type SetupResult struct {
	Bucket          string
	KeepsRemoved    bool // the destination keeps removed objects as earlier versions
	KeepsKnown      bool // false: the storage did not say
	DriveBucket     string
	DestIsDrivesOwn bool
}

// Setup checks a destination and, if it is fit, saves it.
func Setup(ctx context.Context, cfg config.Config, o SetupOptions) (SetupResult, error) {
	var res SetupResult
	say := o.Progress
	if say == nil {
		say = func(string) {}
	}
	cl, err := s3check.Parse(o.Dest)
	if err != nil {
		return res, err
	}
	if o.Region != "" {
		cl.Region = o.Region
	}
	cl.AccessKey, cl.SecretKey = o.AccessKey, o.SecretKey
	res.Bucket = cl.Bucket

	st, err := driveSetting(cfg)
	if err != nil {
		return res, err
	}
	res.DriveBucket = st.Bucket
	if s3check.Same(st.Bucket, o.Dest) {
		res.DestIsDrivesOwn = true
		return res, errors.New("that is the bucket the drive itself uses. A copy in the same bucket protects nothing: " +
			"give a bucket at another provider, or at least another bucket")
	}
	// The drive's own key must be able to read the drive's bucket.
	src, err := s3check.Parse(st.Bucket)
	if err != nil {
		return res, fmt.Errorf("the drive's bucket address is not usable: %w", err)
	}
	src.AccessKey, src.SecretKey = o.DriveAccessKey, o.DriveSecretKey
	say("Checking that the drive's storage key can read the drive's bucket...")
	if _, err := src.List(ctx, st.Name+"/"); err != nil {
		return res, fmt.Errorf("the drive's storage key was not accepted by the drive's bucket: %w", err)
	}
	say("Checking that the backup bucket can be reached and written with its key...")
	if err := cl.CheckWrite(ctx); err != nil {
		return res, err
	}
	say("Checking whether it keeps what is removed from it...")
	res.KeepsRemoved, res.KeepsKnown, _ = cl.Versioning(ctx)

	data, _ := json.MarshalIndent(Settings{Dest: o.Dest, SourceBucket: st.Bucket, Region: cl.Region, Added: time.Now().UTC()}, "", "  ")
	if err := config.WritePrivate(SettingsFile(), append(data, '\n')); err != nil {
		return res, err
	}
	if err := config.WritePrivate(SecretsFile(), []byte("BACKUP_ACCESS_KEY="+o.AccessKey+"\nBACKUP_SECRET_KEY="+o.SecretKey+
		"\nDRIVE_ACCESS_KEY="+o.DriveAccessKey+"\nDRIVE_SECRET_KEY="+o.DriveSecretKey+"\n")); err != nil {
		return res, err
	}
	return res, nil
}

// KeepNote explains what to do about removed objects.
func (r SetupResult) KeepNote() string {
	switch {
	case r.KeepsKnown && r.KeepsRemoved:
		return "The bucket keeps removed and replaced objects as earlier versions, so wiping the drive's bucket does not wipe this copy at once."
	case r.KeepsKnown:
		return "The bucket removes objects at once when asked. If the drive's bucket were wiped, the next run would wipe this copy too. " +
			"Turn on bucket versioning (or a rule that keeps removed objects for 30 days) at the provider, then run setup again."
	default:
		return "This could not be checked: the storage did not say whether it keeps removed objects. Make sure the bucket has versioning, " +
			"or a rule that keeps removed objects for about 30 days (Backblaze B2 does this by default), or a wipe of the drive's bucket would reach the copy at the next run."
	}
}

func driveSetting(cfg config.Config) (index.DriveSetting, error) {
	var s index.DriveSetting
	if cfg.IsLocal() {
		return s, errors.New("a trial drive keeps its files on this computer; there is nothing to back up to another provider")
	}
	target, err := mount.Target(cfg)
	if err != nil {
		return s, err
	}
	conn, err := index.Dial(target, 15*time.Second)
	if err != nil {
		return s, err
	}
	defer conn.Close()
	if s, err = conn.Setting(); err != nil {
		return s, err
	}
	if s.Storage != "s3" {
		return s, fmt.Errorf("the drive's storage is of kind %q; backup works with drives made by `lorefs create` (S3-compatible storage)", s.Storage)
	}
	return s, nil
}

// SyncURI turns a bucket address into the form `juicefs sync` reads, with
// a prefix inside the bucket. Keys go in only when given.
func SyncURI(bucket, prefix, access, secret string) (uri string, plain bool, err error) {
	cl, err := s3check.Parse(bucket)
	if err != nil {
		return "", false, err
	}
	e, _ := url.Parse(cl.Endpoint)
	u := url.URL{Scheme: "s3"}
	if access != "" {
		u.User = url.UserPassword(access, secret)
	}
	if cl.PathStyle {
		u.Host = e.Host
		u.Path = "/" + cl.Bucket + "/" + strings.Trim(prefix, "/") + "/"
	} else {
		u.Host = cl.Bucket + "." + e.Host
		u.Path = "/" + strings.Trim(prefix, "/") + "/"
	}
	return u.String(), e.Scheme == "http", nil
}

// ParseSyncSummary reads the line `juicefs sync` prints at the end.
type Summary struct {
	Found, Skipped, Copied, Extra, Deleted, Failed int64
	CopiedBytes, SkippedBytes                      int64
}

var summaryRe = regexp.MustCompile(`Found: (\d+), excluded: \d+ \([^)]*\), skipped: (\d+) \(([^)]*)\), copied: (\d+) \(([^)]*)\), extra: (\d+) \([^)]*\)(?:, deleted: (\d+), failed: (\d+))?`)

// ParseSummary finds the last summary line in the program's output.
func ParseSummary(out string) (Summary, bool) {
	all := summaryRe.FindAllStringSubmatch(out, -1)
	if len(all) == 0 {
		return Summary{}, false
	}
	m := all[len(all)-1]
	n := func(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }
	return Summary{Found: n(m[1]), Skipped: n(m[2]), SkippedBytes: ParseSize(m[3]), Copied: n(m[4]), CopiedBytes: ParseSize(m[5]),
		Extra: n(m[6]), Deleted: n(m[7]), Failed: n(m[8])}, true
}

var sizeRe = regexp.MustCompile(`^([\d.]+)\s*([KMGTP]?i?B)$`)

// ParseSize reads sizes such as "2.86 MiB" as bytes.
func ParseSize(s string) int64 {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	mult := map[string]float64{"B": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40, "PiB": 1 << 50}[m[2]]
	return int64(v * mult)
}

// RunOptions describe one run.
type RunOptions struct {
	Runner   create.Runner // nil: the real program
	Progress func(string)
	Now      func() time.Time
}

// Run copies the drive's bucket to the destination and records the outcome
// in the drive's index. It returns the recorded result; a failed copy is a
// result with OK false, not an error, unless the result could not be
// recorded either.
func Run(ctx context.Context, cfg config.Config, o RunOptions) (index.FileBackup, error) {
	say := o.Progress
	if say == nil {
		say = func(string) {}
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	runner := o.Runner
	if runner == nil {
		runner = create.ExecRunner{Bin: jfs.Binary()}
	}
	set, keys, err := Load()
	if err != nil {
		return index.FileBackup{}, err
	}
	st, err := driveSetting(cfg)
	if err != nil {
		return index.FileBackup{}, err
	}
	result := index.FileBackup{Destination: strings.TrimSuffix(set.Dest, "/")}
	record := func(r index.FileBackup) (index.FileBackup, error) {
		r.Time = now().UTC()
		r.Destination = result.Destination
		target, terr := mount.Target(cfg)
		if terr != nil {
			return r, terr
		}
		conn, derr := index.Dial(target, 15*time.Second)
		if derr != nil {
			return r, fmt.Errorf("the result could not be recorded: %w", derr)
		}
		defer conn.Close()
		return r, conn.SaveFileBackup(r)
	}
	fail := func(msg string) (index.FileBackup, error) {
		return record(index.FileBackup{OK: false, Error: msg})
	}

	src, srcPlain, err := SyncURI(set.SourceBucket, st.Name, keys.SourceAccess, keys.SourceSecret)
	if err != nil {
		return fail("the drive's bucket address is not usable: " + err.Error())
	}
	dst, dstPlain, err := SyncURI(set.Dest, st.Name, "", "")
	if err != nil {
		return fail("the backup bucket address is not usable: " + err.Error())
	}
	env := []string{"AWS_ACCESS_KEY_ID=" + keys.DestAccess, "AWS_SECRET_ACCESS_KEY=" + keys.DestSecret}
	if set.Region != "" {
		env = append(env, "AWS_REGION="+set.Region)
	}
	args := []string{"sync", src, dst, "--threads", "16", "--no-color"}
	if srcPlain || dstPlain {
		args = append(args, "--no-https")
	}

	// First look without copying or removing anything, to see what would
	// happen. Without --delete-dst the program counts what is in the drive's
	// bucket ("found") and what is only in the copy ("extra").
	say("Comparing the drive's bucket with the copy...")
	out, err := runner.Run(ctx, env, append(args, "--dry")...)
	if err != nil {
		return fail(explain(out, "the drive's bucket and the copy could not be compared"))
	}
	sum, ok := ParseSummary(out)
	if !ok {
		return fail("the comparison gave an answer that was not understood")
	}
	have := sum.Found - sum.Copied + sum.Extra // what the copy holds now
	if have > 1000 && sum.Found*2 < have {
		return fail(fmt.Sprintf("the drive's bucket holds %d objects, the backup %d; not copied, check the drive's bucket", sum.Found, have))
	}

	say(fmt.Sprintf("Copying %d changed object(s)...", sum.Copied))
	out, err = runner.Run(ctx, env, append(args, "--delete-dst")...)
	if err != nil {
		return fail(explain(out, "the copy failed"))
	}
	final, ok := ParseSummary(out)
	if !ok {
		return fail("the copy gave an answer that was not understood")
	}
	if final.Failed > 0 {
		return fail(fmt.Sprintf("%d object(s) could not be copied: %s", final.Failed, explain(out, "see the program's output")))
	}
	// What the copy now holds is what the drive's bucket held when it was compared.
	return record(index.FileBackup{OK: true, Objects: sum.Found, Bytes: sum.SkippedBytes + sum.CopiedBytes})
}

func explain(out, fallback string) string {
	low := strings.ToLower(out)
	line := lastError(out)
	switch {
	case strings.Contains(low, "cap") && (strings.Contains(low, "exceed") || strings.Contains(low, "limit") || strings.Contains(low, "reached")):
		return "the destination refused more data: its spending limit was reached (" + line + ")"
	case strings.Contains(low, "accessdenied") || strings.Contains(low, "invalidaccesskey") || strings.Contains(low, "signaturedoesnotmatch") || strings.Contains(low, "403"):
		return "the storage refused the key (" + line + ")"
	}
	if line == "" {
		return fallback
	}
	return fallback + ": " + line
}

var errLine = regexp.MustCompile(`<(?:ERROR|FATAL)>: (.*?)(?: \[[^\]]*\])?$`)

func lastError(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if m := errLine.FindStringSubmatch(strings.TrimSpace(lines[i])); m != nil {
			s := m[1]
			if len(s) > 240 {
				s = s[:240]
			}
			return s
		}
	}
	return ""
}

// Unit files for the nightly run.
const serviceUnit = `[Unit]
Description=LoreFS nightly backup of the drive's bucket
Documentation=https://github.com/T3-Venture-Labs-Limited/LoreFS

[Service]
Type=oneshot
ExecStart=%s backup run
%s`

const timerUnit = `[Unit]
Description=LoreFS nightly backup

[Timer]
OnCalendar=%s
RandomizedDelaySec=%s
# If the computer was off at 03:30, run once it is back.
Persistent=true

[Install]
WantedBy=timers.target
`

// ScheduleOptions say where the timer goes.
type ScheduleOptions struct {
	Exe    string // the lorefs program
	System bool   // /etc/systemd/system instead of the user's folder
	Off    bool
	// Systemctl runs systemctl; nil runs the real one.
	Systemctl func(args ...string) error
	UnitDir   string // override, for tests
}

// ScheduleFiles returns the unit directory and the contents of both units.
func ScheduleFiles(o ScheduleOptions) (dir, service, timer string) {
	dir = o.UnitDir
	if dir == "" {
		if o.System {
			dir = "/etc/systemd/system"
		} else {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".config", "systemd", "user")
		}
	}
	env := ""
	if d := os.Getenv("LOREFS_CONFIG_DIR"); d != "" {
		env = "Environment=LOREFS_CONFIG_DIR=" + d + "\n"
	}
	calendar, spread := "*-*-* 03:30:00", "20min"
	if c := os.Getenv("LOREFS_BACKUP_CALENDAR"); c != "" { // for tests
		calendar, spread = c, "0"
	}
	return dir, fmt.Sprintf(serviceUnit, o.Exe, env), fmt.Sprintf(timerUnit, calendar, spread)
}

// Schedule installs (or with Off removes) the nightly timer.
func Schedule(o ScheduleOptions) error {
	ctl := o.Systemctl
	if ctl == nil {
		ctl = func(args ...string) error {
			if !o.System {
				args = append([]string{"--user"}, args...)
			}
			if out, err := execCombined("systemctl", args...); err != nil {
				return fmt.Errorf("systemctl %s: %s", strings.Join(args, " "), strings.TrimSpace(out))
			}
			return nil
		}
	}
	dir, service, timer := ScheduleFiles(o)
	svc, tmr := filepath.Join(dir, "lorefs-backup.service"), filepath.Join(dir, "lorefs-backup.timer")
	if o.Off {
		ctl("disable", "--now", "lorefs-backup.timer")
		os.Remove(svc)
		os.Remove(tmr)
		return ctl("daemon-reload")
	}
	if _, _, err := Load(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(svc, []byte(service), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(tmr, []byte(timer), 0o644); err != nil {
		return err
	}
	if err := ctl("daemon-reload"); err != nil {
		return err
	}
	return ctl("enable", "--now", "lorefs-backup.timer")
}

func execCombined(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var b bytes.Buffer
	cmd.Stdout, cmd.Stderr = &b, &b
	err := cmd.Run()
	return b.String(), err
}
