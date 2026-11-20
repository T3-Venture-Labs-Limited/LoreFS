package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// The kinds of index.
const (
	SchemeEncrypted = "rediss"  // Redis or Valkey over TLS
	SchemePlain     = "redis"   // not encrypted: testing only
	SchemeSQLite    = "sqlite3" // a trial drive on one computer
)

// IsLocal reports whether this is a trial drive, whose index is a file on
// this computer rather than a server.
func (c Config) IsLocal() bool { return c.IndexScheme == SchemeSQLite }

// IndexURL returns the index's address without any password, for example
// "rediss://index.example.net:6379/1".
func (c Config) IndexURL() string {
	if c.IsLocal() {
		return SchemeSQLite + "://" + c.IndexPath
	}
	u := url.URL{
		Scheme: c.IndexScheme,
		Host:   net.JoinHostPort(c.IndexHost, strconv.Itoa(c.IndexPort)),
		Path:   "/" + strconv.Itoa(c.IndexDB),
	}
	return u.String()
}

// SetIndex sets the index from an address such as
// "rediss://index.example.net:6379/1" or "sqlite3:///path/meta.db".
func (c *Config) SetIndex(raw string) error { return c.setIndex(raw) }

// setIndex reads an address written by IndexURL.
func (c *Config) setIndex(raw string) error {
	if rest, ok := strings.CutPrefix(raw, SchemeSQLite+"://"); ok {
		if rest == "" {
			return fmt.Errorf("index: the trial drive's file is missing")
		}
		c.IndexScheme, c.IndexPath = SchemeSQLite, rest
		c.IndexHost, c.IndexPort, c.IndexDB, c.IndexTLS = "", 0, 0, false
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("index: %q is not an address", raw)
	}
	if u.Scheme != SchemeEncrypted && u.Scheme != SchemePlain {
		return fmt.Errorf("index: %q: the address should start with rediss:// (or redis:// for testing)", raw)
	}
	port := 6379
	if p := u.Port(); p != "" {
		if port, err = strconv.Atoi(p); err != nil || port <= 0 || port > 65535 {
			return fmt.Errorf("index: %q has a bad port", raw)
		}
	}
	db := 0
	if p := strings.Trim(u.Path, "/"); p != "" {
		if db, err = strconv.Atoi(p); err != nil || db < 0 {
			return fmt.Errorf("index: %q has a bad database number", raw)
		}
	}
	if u.Hostname() == "" {
		return fmt.Errorf("index: %q names no server", raw)
	}
	c.IndexScheme, c.IndexHost, c.IndexPort, c.IndexDB = u.Scheme, u.Hostname(), port, db
	c.IndexTLS = u.Scheme == SchemeEncrypted
	c.IndexPath = ""
	return nil
}

// normalize makes the scheme and the older host, port and tls settings
// agree after reading a file. A file written by the program that came
// before has no "index" key: its scheme comes from index_tls.
func (c *Config) normalize(sawIndex bool) {
	if c.IsLocal() {
		return
	}
	if !sawIndex {
		c.IndexScheme = SchemePlain
		if c.IndexTLS {
			c.IndexScheme = SchemeEncrypted
		}
	}
	c.IndexTLS = c.IndexScheme == SchemeEncrypted
}
