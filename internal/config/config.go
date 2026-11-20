// Package config reads and writes the per-machine settings of LoreFS.
//
// Everything lives in one directory (normally ~/.config/lorefs) that only
// the user can read: config.toml, secret.env, ca.pem, pins and skip.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrNotJoined is returned by Load when this machine has not joined a drive.
var ErrNotJoined = errors.New("this machine has not joined a drive")

// Config is the content of config.toml.
type Config struct {
	DriveName    string // name of the drive, shown in status
	Machine      string // short name of this machine, shown in history
	Folder       string // where the drive is mounted
	IndexHost    string
	IndexPort    int
	IndexDB      int
	IndexTLS     bool // false only for local testing
	OwnerUID     int  // numeric user id that owns the drive's files
	CacheDir     string
	CacheLimitMB int
	// ClientCache turns on JuiceFS's pushed metadata cache. It is off by
	// default: with JuiceFS 1.4.2 it loses data when a file is appended to
	// repeatedly (see docs/measurements.md). Do not enable it without
	// re-running the append check in scripts/itest.sh.
	ClientCache   bool
	Containers    bool // let containers and other users see the mount
	SmallFileKB   int  // files up to this size are kept cached
	RetentionDays int  // how long replaced versions are kept
	// How long this machine trusts what it already knows before asking
	// the index again. Longer is faster; shorter shows another machine's
	// changes sooner. FileCacheSec + OpenCacheSec must stay under 5.
	FileCacheSec   int // names and attributes of files
	FolderCacheSec int // names of folders
	OpenCacheSec   int // reopening a file that was just opened
}

// Defaults returns a Config with every optional setting at its default.
func Defaults() Config {
	home, _ := os.UserHomeDir()
	return Config{
		DriveName:      "lorefs",
		Folder:         filepath.Join(home, "Lore"),
		IndexDB:        1,
		IndexTLS:       true,
		OwnerUID:       os.Getuid(),
		CacheDir:       filepath.Join(home, ".cache", "lorefs"),
		CacheLimitMB:   51200,
		ClientCache:    false,
		SmallFileKB:    1024,
		RetentionDays:  30,
		FileCacheSec:   2,
		FolderCacheSec: 10,
		OpenCacheSec:   2,
	}
}

// Dir is the directory holding this machine's settings.
func Dir() string {
	if d := os.Getenv("LOREFS_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "lorefs")
}

// StateDir is where the helper keeps its heartbeat and bookkeeping.
func StateDir() string {
	if d := os.Getenv("LOREFS_STATE_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "lorefs")
}

// JoinedAt is when this machine joined the drive: the time its saved
// secret was written. Zero when unknown.
func JoinedAt() time.Time {
	st, err := os.Stat(SecretFile())
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// Paths of the files inside Dir.
func File() string       { return filepath.Join(Dir(), "config.toml") }
func SecretFile() string { return filepath.Join(Dir(), "secret.env") }
func CAFile() string     { return filepath.Join(Dir(), "ca.pem") }
func PinsFile() string   { return filepath.Join(Dir(), "pins") }
func SkipFile() string   { return filepath.Join(Dir(), "skip") }

// Joined reports whether this machine has joined a drive.
func Joined() bool {
	_, err := os.Stat(File())
	return err == nil
}

// Load reads config.toml. Missing optional keys keep their defaults.
func Load() (Config, error) {
	c := Defaults()
	f, err := os.Open(File())
	if errors.Is(err, os.ErrNotExist) {
		return c, ErrNotJoined
	}
	if err != nil {
		return c, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, raw, ok := strings.Cut(text, "=")
		if !ok {
			return c, fmt.Errorf("%s line %d: expected key = value", File(), line)
		}
		key, raw = strings.TrimSpace(key), strings.TrimSpace(raw)
		if err := c.set(key, raw); err != nil {
			return c, fmt.Errorf("%s line %d: %w", File(), line, err)
		}
	}
	return c, sc.Err()
}

func (c *Config) set(key, raw string) error {
	str := func(dst *string) error {
		v, err := strconv.Unquote(raw)
		if err != nil {
			return fmt.Errorf("%s: expected a quoted string", key)
		}
		*dst = v
		return nil
	}
	num := func(dst *int) error {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("%s: expected a number", key)
		}
		*dst = v
		return nil
	}
	flag := func(dst *bool) error {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("%s: expected true or false", key)
		}
		*dst = v
		return nil
	}
	switch key {
	case "drive_name":
		return str(&c.DriveName)
	case "machine":
		return str(&c.Machine)
	case "folder":
		return str(&c.Folder)
	case "index_host":
		return str(&c.IndexHost)
	case "index_port":
		return num(&c.IndexPort)
	case "index_db":
		return num(&c.IndexDB)
	case "index_tls":
		return flag(&c.IndexTLS)
	case "owner_uid":
		return num(&c.OwnerUID)
	case "cache_dir":
		return str(&c.CacheDir)
	case "cache_limit_mb":
		return num(&c.CacheLimitMB)
	case "client_cache":
		return flag(&c.ClientCache)
	case "containers":
		return flag(&c.Containers)
	case "small_file_kb":
		return num(&c.SmallFileKB)
	case "retention_days":
		return num(&c.RetentionDays)
	case "file_cache_sec":
		return num(&c.FileCacheSec)
	case "folder_cache_sec":
		return num(&c.FolderCacheSec)
	case "open_cache_sec":
		return num(&c.OpenCacheSec)
	}
	return nil // unknown keys are ignored so older programs read newer files
}

// Save writes config.toml so that only the user can read it.
func (c Config) Save() error {
	q := strconv.Quote
	lines := []string{
		"# LoreFS settings for this machine. Edit with care; `lorefs status` checks them.",
		"drive_name = " + q(c.DriveName),
		"machine = " + q(c.Machine),
		"folder = " + q(c.Folder),
		"index_host = " + q(c.IndexHost),
		"index_port = " + strconv.Itoa(c.IndexPort),
		"index_db = " + strconv.Itoa(c.IndexDB),
		"index_tls = " + strconv.FormatBool(c.IndexTLS),
		"owner_uid = " + strconv.Itoa(c.OwnerUID),
		"cache_dir = " + q(c.CacheDir),
		"cache_limit_mb = " + strconv.Itoa(c.CacheLimitMB),
		"client_cache = " + strconv.FormatBool(c.ClientCache),
		"containers = " + strconv.FormatBool(c.Containers),
		"small_file_kb = " + strconv.Itoa(c.SmallFileKB),
		"retention_days = " + strconv.Itoa(c.RetentionDays),
		"file_cache_sec = " + strconv.Itoa(c.FileCacheSec),
		"folder_cache_sec = " + strconv.Itoa(c.FolderCacheSec),
		"open_cache_sec = " + strconv.Itoa(c.OpenCacheSec),
	}
	return WritePrivate(File(), []byte(strings.Join(lines, "\n")+"\n"))
}

// WritePrivate writes a file readable only by the user, replacing any
// existing file in one step.
func WritePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Password reads the index password from secret.env.
func Password() (string, error) {
	data, err := os.ReadFile(SecretFile())
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "META_PASSWORD="); ok {
			return v, nil
		}
	}
	return "", fmt.Errorf("%s holds no META_PASSWORD", SecretFile())
}

// SavePassword writes the index password to secret.env.
func SavePassword(pw string) error {
	return WritePrivate(SecretFile(), []byte("META_PASSWORD="+pw+"\n"))
}

// Pins returns the pinned folders (paths relative to the drive), sorted.
func Pins() ([]string, error) {
	data, err := os.ReadFile(PinsFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SavePins replaces the list of pinned folders.
func SavePins(pins []string) error {
	sort.Strings(pins)
	body := strings.Join(pins, "\n")
	if body != "" {
		body += "\n"
	}
	return WritePrivate(PinsFile(), []byte(body))
}
