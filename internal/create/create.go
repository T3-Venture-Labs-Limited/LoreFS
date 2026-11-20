// Package create makes a new shared drive: it checks the index and the
// storage, lets JuiceFS write the drive, turns on the change log, and
// builds the first join code. Nothing is created unless every check
// passes, and a failure after the drive was written removes it again.
package create

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/index"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/token"
)

// What a new drive keeps, and for how long (spec: safe defaults).
const (
	TrashDays        = 7
	ChangeLogAge     = "24h"
	ChangeLogEntries = 100000
)

// The reasons creating a drive is refused. Each is wrapped with a plain
// sentence that says what to do.
var (
	ErrPlainIndex       = errors.New("the index address is not encrypted")
	ErrIndexUnreachable = errors.New("the index cannot be reached")
	ErrIndexPassword    = errors.New("the index refused the password")
	ErrIndexCertificate = errors.New("the index did not prove its identity")
	ErrIndexInUse       = errors.New("the index already holds a drive")
	ErrIndexNotEmpty    = errors.New("the index database is not empty")
	ErrStorageKey       = errors.New("the storage refused the key")
	ErrStorageReach     = errors.New("the storage cannot be reached")
	ErrStorageInUse     = errors.New("the bucket already holds a drive")
	ErrIncomplete       = errors.New("something needed to create the drive is missing")
)

// Storage is an S3-compatible bucket.
type Storage struct {
	Bucket    string // address, for example https://store.example.net/my-bucket
	AccessKey string
	SecretKey string
}

// Options say what to create.
type Options struct {
	Name        string // the drive's name; default "lorefs"
	Storage     Storage
	IndexURL    string // for example rediss://index.example.net:6379/1
	IndexPass   string
	IndexCA     []byte // PEM; empty when the certificate comes from a public authority
	AllowPlain  bool   // accept an index without encryption (testing only)
	Progress    func(string)
	Juicefs     Runner // nil: the real program
	DialTimeout time.Duration
}

// Runner runs the juicefs program. env holds extra environment variables.
type Runner interface {
	Run(ctx context.Context, env []string, args ...string) (output string, err error)
}

// Result describes the drive that was created.
type Result struct {
	Config config.Config
	Code   string // the first join code
	CA     []byte
	Pass   string
}

func (o *Options) say(s string) {
	if o.Progress != nil {
		o.Progress(s)
	}
}

// Create checks everything, then creates the drive.
func Create(ctx context.Context, o Options) (Result, error) {
	var res Result
	if o.Name == "" {
		o.Name = "lorefs"
	}
	if err := CheckName(o.Name); err != nil {
		return res, err
	}
	cfg := config.Defaults()
	cfg.DriveName = o.Name
	if o.IndexURL == "" {
		return res, fmt.Errorf("%w: the index address", ErrIncomplete)
	}
	if err := cfg.SetIndex(o.IndexURL); err != nil {
		return res, err
	}
	if cfg.IsLocal() {
		return res, errors.New("a trial drive is created with --local")
	}
	if o.Storage.Bucket == "" || o.Storage.AccessKey == "" || o.Storage.SecretKey == "" {
		return res, fmt.Errorf("%w: the storage address, access key and secret key", ErrIncomplete)
	}
	if o.IndexPass == "" {
		return res, fmt.Errorf("%w: the index password", ErrIncomplete)
	}
	if cfg.IndexScheme == config.SchemePlain {
		if !o.AllowPlain {
			return res, fmt.Errorf("%w. Anyone on the network between you and the index could read file names and the password. "+
				"Use an address that starts with rediss://, or pass --allow-unencrypted-index if you accept that for testing", ErrPlainIndex)
		}
		o.say("Warning: the index is not encrypted. Anyone on the network path can read file names and the password.")
	}

	// 1. The index: reachable, password accepted, nothing in it.
	o.say("Checking the index...")
	timeout := o.DialTimeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	conn, err := index.Dial(index.Target{Host: cfg.IndexHost, Port: cfg.IndexPort, DB: cfg.IndexDB,
		TLS: cfg.IndexTLS, Password: o.IndexPass, CAPEM: o.IndexCA}, timeout)
	switch {
	case errors.Is(err, index.ErrPassword):
		return res, fmt.Errorf("%w. Check the password.", ErrIndexPassword)
	case errors.Is(err, index.ErrCertificate):
		return res, fmt.Errorf("%w (%v). If the server uses its own certificate authority, give its certificate file.", ErrIndexCertificate, err)
	case err != nil:
		return res, fmt.Errorf("%w (%v). Check the address and that this computer may connect to it.", ErrIndexUnreachable, err)
	}
	name, formatted := conn.IsFormatted()
	n, _ := conn.KeyCount()
	conn.Close()
	switch {
	case formatted:
		return res, fmt.Errorf("%w (named %q). Nothing was changed. To use that drive, run `lorefs join` with a join code for it; "+
			"to make a new one, give an empty index database", ErrIndexInUse, name)
	case n > 0:
		return res, fmt.Errorf("%w: it holds %d keys that are not a drive. Nothing was changed. Use an empty database or server", ErrIndexNotEmpty, n)
	}

	// 2. Write the drive. JuiceFS tests that the storage can be written
	// before it writes anything to the index.
	runner := o.Juicefs
	if runner == nil {
		runner = ExecRunner{Bin: jfs.Binary()}
	}
	caPath := ""
	if len(o.IndexCA) > 0 {
		f, err := os.CreateTemp("", "lorefs-ca-*.pem")
		if err != nil {
			return res, err
		}
		caPath = f.Name()
		defer os.Remove(caPath)
		f.Write(o.IndexCA)
		f.Close()
	}
	meta := metaURL(cfg, caPath)
	env := []string{"META_PASSWORD=" + o.IndexPass, "ACCESS_KEY=" + o.Storage.AccessKey, "SECRET_KEY=" + o.Storage.SecretKey}
	o.say("Creating the drive (this tests the storage as well)...")
	out, err := runner.Run(ctx, env, "format", "--storage", "s3", "--bucket", o.Storage.Bucket,
		"--trash-days", strconv.Itoa(TrashDays), meta, o.Name)
	if err != nil {
		return res, explainFormat(out, o.Storage.Bucket)
	}

	// 3. The change log, which version history is built on.
	o.say("Turning on the change log...")
	if out, err := runner.Run(ctx, env, "config", meta, "--changelog",
		"--changelog-max-age", ChangeLogAge, "--changelog-max-lines", strconv.Itoa(ChangeLogEntries)); err != nil {
		o.undo(ctx, runner, env, meta)
		return res, fmt.Errorf("the drive was written but the change log could not be turned on (%s); the drive was removed again", lastLine(out))
	}

	cfg.OwnerUID, cfg.OwnerGID = os.Getuid(), os.Getgid()
	code, err := token.Encode(token.Token{Drive: o.Name, Scheme: cfg.IndexScheme, Host: cfg.IndexHost,
		Port: cfg.IndexPort, DB: cfg.IndexDB, TLS: cfg.IndexTLS, Password: o.IndexPass, CA: string(o.IndexCA),
		UID: cfg.OwnerUID, GID: cfg.OwnerGID})
	if err != nil {
		o.undo(ctx, runner, env, meta)
		return res, err
	}
	return Result{Config: cfg, Code: code, CA: o.IndexCA, Pass: o.IndexPass}, nil
}

// undo removes a drive that was written but cannot be used: the index
// entries and the storage objects under the drive's name.
func (o *Options) undo(ctx context.Context, r Runner, env []string, meta string) {
	out, err := r.Run(ctx, env, "status", meta)
	if err != nil {
		return
	}
	if uuid := uuidOf(out); uuid != "" {
		r.Run(ctx, env, "destroy", meta, uuid, "--yes")
	}
}

func uuidOf(out string) string {
	i := strings.Index(out, `"UUID"`)
	if i < 0 {
		return ""
	}
	rest := out[i+len(`"UUID"`):]
	a := strings.IndexByte(rest, '"')
	if a < 0 {
		return ""
	}
	b := strings.IndexByte(rest[a+1:], '"')
	if b < 0 {
		return ""
	}
	return rest[a+1 : a+1+b]
}

// metaURL is the address juicefs uses for the index; caPath is a file
// holding the certificate authority, or "".
func metaURL(c config.Config, caPath string) string {
	u := c.IndexURL()
	if caPath != "" {
		u += "?tls-ca-cert-file=" + caPath
	}
	return u
}

// explainFormat turns what `juicefs format` printed into a sentence.
func explainFormat(out, bucket string) error {
	low := strings.ToLower(out)
	line := lastLine(out)
	switch {
	case containsAny(low, "accessdenied", "access denied", "invalidaccesskey", "signaturedoesnotmatch", "forbidden", "invalid access key", "403", "unauthorized"):
		return fmt.Errorf("%w. Check the access key and secret key, and that the key may write to the bucket. Nothing was created (%s)", ErrStorageKey, line)
	case containsAny(low, "not empty", "already exist", "already formatted", "volume uuid"):
		return fmt.Errorf("%w. Nothing was changed. Use an empty bucket, or `lorefs join` if that drive is the one you want (%s)", ErrStorageInUse, line)
	case containsAny(low, "no such host", "connection refused", "request send failed", "timeout", "timed out", "dial tcp", "no route", "i/o timeout", "eof"):
		return fmt.Errorf("%w (%s). Check the address %s and this computer's connection. Nothing was created", ErrStorageReach, line, bucket)
	}
	return fmt.Errorf("the drive could not be created: %s. Nothing was created", line)
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	// Drop the "2026/11/20 17:02:57.647128 juicefs[1234] <FATAL>: " prefix.
	if i := strings.Index(s, "<FATAL>: "); i >= 0 {
		s = s[i+len("<FATAL>: "):]
	}
	if i := strings.LastIndex(s, " ["); i > 0 && strings.HasSuffix(s, "]") {
		s = s[:i]
	}
	return s
}

// ExecRunner runs the real juicefs program. Secrets travel in the
// environment, never in the arguments.
type ExecRunner struct{ Bin string }

// Run implements Runner.
func (e ExecRunner) Run(ctx context.Context, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, e.Bin, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = append(os.Environ(), env...)
	err := cmd.Run()
	return out.String(), err
}

// SetChangeLog turns the drive's change log on (with the retention a new
// drive gets) or off. Off means version history falls back to a periodic
// walk of the changed files.
func SetChangeLog(ctx context.Context, r Runner, meta string, env []string, on bool) error {
	if r == nil {
		r = ExecRunner{Bin: jfs.Binary()}
	}
	args := []string{"config", meta}
	if on {
		args = append(args, "--changelog", "--changelog-max-age", ChangeLogAge, "--changelog-max-lines", strconv.Itoa(ChangeLogEntries))
	} else {
		args = append(args, "--changelog=false")
	}
	if out, err := r.Run(ctx, env, args...); err != nil {
		return fmt.Errorf("the change log setting could not be changed: %s", lastLine(out))
	}
	return nil
}
