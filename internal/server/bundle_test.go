package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitWritesTheBundle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "idx")
	if err := Init(InitOptions{Address: "203.0.113.7", Dir: dir, UID: 1000, GID: 1000}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"compose.yaml", "valkey.conf", "tls/ca.crt", "tls/server.crt", "tls/server.key",
		"secrets/ca.key", "secrets/index-password", "server.json", "README.md", "data"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	if _, err := os.Stat(dir + ".partial"); err == nil {
		t.Error("the temporary folder was left behind")
	}
	// Keys and the password are readable by the user only.
	for _, f := range []string{"tls/server.key", "secrets/ca.key", "secrets/index-password", "valkey.conf"} {
		st, _ := os.Stat(filepath.Join(dir, f))
		if st.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is readable by others: %v", f, st.Mode().Perm())
		}
	}
	conf, _ := os.ReadFile(filepath.Join(dir, "valkey.conf"))
	for _, want := range []string{"port 0", "tls-port 6379", "appendonly yes", "appendfsync everysec", "maxmemory 400mb", "maxmemory-policy noeviction", "requirepass "} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("valkey.conf lacks %q", want)
		}
	}
	comp, _ := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if !strings.Contains(string(comp), DefaultImage) || !strings.Contains(string(comp), `"1000:1000"`) || !strings.Contains(string(comp), "6379:6379") {
		t.Errorf("compose.yaml: %s", comp)
	}
	c, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Address != "203.0.113.7" || c.Port != 6379 || len(c.Password) != 43 || !strings.Contains(string(c.CA), "BEGIN CERTIFICATE") {
		t.Fatalf("%+v", c)
	}
	if !strings.Contains(string(conf), "requirepass "+c.Password) {
		t.Error("the password in valkey.conf is not the one in secrets/")
	}
	if got := c.IndexURL(1); got != "rediss://203.0.113.7:6379/1" {
		t.Fatal(got)
	}
	readme, _ := os.ReadFile(filepath.Join(dir, "README.md"))
	if strings.Contains(string(readme), c.Password) {
		t.Error("the README must not hold the password")
	}
}

func TestInitRefusesAnExistingFolderAndChangesNothing(t *testing.T) {
	dir := t.TempDir() // exists
	os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o600)
	err := Init(InitOptions{Address: "203.0.113.7", Dir: dir})
	if !errors.Is(err, ErrExists) {
		t.Fatalf("got %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("the folder was changed: %v", entries)
	}
}

func TestInitInputChecks(t *testing.T) {
	d := t.TempDir()
	for _, tc := range []InitOptions{
		{Address: "", Dir: filepath.Join(d, "a")},
		{Address: "host:6379", Dir: filepath.Join(d, "b")},
		{Address: "ho st", Dir: filepath.Join(d, "c")},
		{Address: "203.0.113.7", Dir: filepath.Join(d, "d"), MemoryMB: -5},
	} {
		if err := Init(tc); err == nil {
			t.Errorf("%+v should be refused", tc)
		}
		if _, err := os.Stat(tc.Dir); err == nil {
			t.Errorf("%s was created despite the error", tc.Dir)
		}
	}
	if err := Init(InitOptions{Address: "index.example.net", Dir: filepath.Join(d, "ok"), MemoryMB: 64, Port: 6400}); err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(filepath.Join(d, "ok", "valkey.conf"))
	if !strings.Contains(string(conf), "maxmemory 64mb") {
		t.Error("memory limit not applied")
	}
	comp, _ := os.ReadFile(filepath.Join(d, "ok", "compose.yaml"))
	if !strings.Contains(string(comp), "6400:6379") {
		t.Error("port not applied")
	}
}

func TestReadRefusesAFolderThatIsNotABundle(t *testing.T) {
	if _, err := Read(t.TempDir()); err == nil {
		t.Fatal("expected an error")
	}
}
