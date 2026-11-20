package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultImage is the index server the bundle runs: Valkey (BSD licence).
const DefaultImage = "valkey/valkey:8-alpine"

// InitOptions say what to put in the bundle.
type InitOptions struct {
	Address  string // an IP address or a name that computers will use to reach the index
	Dir      string // the folder to create; it must not exist
	Port     int    // default 6379
	MemoryMB int    // limit before the index refuses new files; default 400
	Image    string // default DefaultImage
	Name     string // the drive's name, used to label the authority
	UID, GID int    // the user the container runs as (owns the files)
	Now      time.Time
}

// Connection is what `lorefs create --server-dir` reads back.
type Connection struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Password string `json:"-"`
	CA       []byte `json:"-"`
}

// ErrExists is returned when the folder is already there.
var ErrExists = errors.New("the folder already exists")

// Init writes the bundle. It refuses an existing folder, so a bundle that
// holds a running index's data is never overwritten.
func Init(o InitOptions) error {
	if o.Address == "" {
		return errors.New("--address is required: the IP address or name other computers will use to reach the index")
	}
	if strings.ContainsAny(o.Address, " /\\:\"'\n") && net.ParseIP(o.Address) == nil {
		return fmt.Errorf("%q is not an address: give an IP address or a host name without a port", o.Address)
	}
	if o.Dir == "" {
		o.Dir = "lorefs-index"
	}
	if o.Port == 0 {
		o.Port = 6379
	}
	if o.MemoryMB == 0 {
		o.MemoryMB = 400
	}
	if o.MemoryMB < 1 {
		return errors.New("the memory limit must be at least 1 MB")
	}
	if o.Image == "" {
		o.Image = DefaultImage
	}
	if o.Name == "" {
		o.Name = "lorefs"
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if _, err := os.Stat(o.Dir); err == nil {
		return fmt.Errorf("%w: %s. Nothing was changed. Choose another folder, or remove that one if you are sure it holds no data you need", ErrExists, o.Dir)
	}

	ca, err := NewAuthority(o.Name, o.Now)
	if err != nil {
		return err
	}
	sc, err := ca.Issue(o.Address, o.Now)
	if err != nil {
		return err
	}
	pw, err := Password()
	if err != nil {
		return err
	}

	// Write into a temporary sibling and rename at the end, so that a
	// failure leaves no half-made bundle.
	tmp := o.Dir + ".partial"
	os.RemoveAll(tmp)
	for _, d := range []string{"tls", "secrets", "data"} {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o700); err != nil {
			return err
		}
	}
	write := func(name string, data []byte, mode os.FileMode) error {
		return os.WriteFile(filepath.Join(tmp, name), data, mode)
	}
	conn, _ := json.MarshalIndent(Connection{Address: o.Address, Port: o.Port}, "", "  ")
	steps := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"tls/ca.crt", ca.CertPEM, 0o644},
		{"tls/server.crt", sc.CertPEM, 0o600},
		{"tls/server.key", sc.KeyPEM, 0o600},
		{"secrets/ca.key", ca.KeyPEM, 0o600},
		{"secrets/index-password", []byte(pw + "\n"), 0o600},
		{"valkey.conf", []byte(config(pw, o)), 0o600},
		{"compose.yaml", []byte(compose(o)), 0o644},
		{"server.json", append(conn, '\n'), 0o644},
		{"README.md", []byte(readme(o)), 0o644},
	}
	for _, s := range steps {
		if err := write(s.name, s.data, s.mode); err != nil {
			os.RemoveAll(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, o.Dir); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	return nil
}

// Read reads a bundle's connection details.
func Read(dir string) (Connection, error) {
	var c Connection
	data, err := os.ReadFile(filepath.Join(dir, "server.json"))
	if err != nil {
		return c, fmt.Errorf("%s is not an index bundle (no server.json): %w", dir, err)
	}
	if err := json.Unmarshal(data, &c); err != nil || c.Address == "" || c.Port == 0 {
		return c, fmt.Errorf("%s/server.json is not readable", dir)
	}
	pw, err := os.ReadFile(filepath.Join(dir, "secrets", "index-password"))
	if err != nil {
		return c, fmt.Errorf("reading the index password: %w", err)
	}
	c.Password = strings.TrimSpace(string(pw))
	if c.CA, err = os.ReadFile(filepath.Join(dir, "tls", "ca.crt")); err != nil {
		return c, fmt.Errorf("reading the certificate authority: %w", err)
	}
	return c, nil
}

// IndexURL is the address a drive on this bundle uses.
func (c Connection) IndexURL(db int) string {
	return "rediss://" + net.JoinHostPort(c.Address, strconv.Itoa(c.Port)) + "/" + strconv.Itoa(db)
}

func config(pw string, o InitOptions) string {
	return fmt.Sprintf(`# LoreFS index (Valkey). Written by `+"`lorefs server init`"+`.
# Encrypted connections only: the plain port is switched off.
port 0
tls-port 6379
tls-cert-file /tls/server.crt
tls-key-file /tls/server.key
tls-auth-clients no
tls-protocols "TLSv1.2 TLSv1.3"
requirepass %s

# Keep data on disk: every change is logged and flushed once a second,
# with periodic snapshots as well.
dir /data
appendonly yes
appendfsync everysec
save 900 1
save 300 100

# When memory is full, refuse new writes instead of discarding entries.
maxmemory %dmb
maxmemory-policy noeviction

tcp-keepalive 60
`, pw, o.MemoryMB)
}

func compose(o InitOptions) string {
	return fmt.Sprintf(`# LoreFS index. Start it with: docker compose up -d
services:
  index:
    image: %s
    command: ["valkey-server", "/etc/valkey/valkey.conf"]
    restart: unless-stopped
    # Run as the user who made this folder, so that the keys and data stay theirs.
    user: "%d:%d"
    ports:
      - "%d:6379"
    volumes:
      - ./valkey.conf:/etc/valkey/valkey.conf:ro
      - ./tls:/tls:ro
      - ./data:/data
`, o.Image, o.UID, o.GID, o.Port)
}

func readme(o InitOptions) string {
	return fmt.Sprintf(`# LoreFS index

This folder runs the index of a LoreFS drive: a Valkey server that accepts
encrypted connections only, protected by a password.

Address computers use: %s (port %d)
Memory limit: %d MB. When it is full the index refuses new files and
keeps the ones it has; raise "maxmemory" in valkey.conf and restart.

## Start it

    docker compose up -d

Open port %d to the computers that will use the drive, and to nobody else.
The password is what protects the index; the connection is encrypted with
the certificate in tls/.

## Create the drive on it

    lorefs create --server-dir %s --bucket https://store.example.net/my-bucket --access-key KEY

(the storage secret key is asked for, or read from LOREFS_STORAGE_SECRET).

## Files

- compose.yaml        how Docker runs the index
- valkey.conf         its configuration (holds the password; keep it private)
- tls/ca.crt          the certificate authority; computers need this, it is public
- tls/server.crt, tls/server.key   the server's certificate and key
- secrets/            the password and the authority's key. Keep a copy somewhere
                      safe and never share this folder.
- data/               the index's data
- server.json         the address, read by lorefs

The certificate is valid for ten years and only for %s. If the address
changes, make a new bundle with lorefs server init and move the data.

## Backups

The index is small and holds only the drive's file names and structure; the
file contents live in the storage bucket. JuiceFS writes a copy of the
index into the bucket every hour while a computer is connected.
`, o.Address, o.Port, o.MemoryMB, o.Port, o.Dir, o.Address)
}
