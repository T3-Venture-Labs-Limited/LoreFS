package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadNotJoined(t *testing.T) {
	t.Setenv("LOREFS_CONFIG_DIR", t.TempDir())
	if _, err := Load(); !errors.Is(err, ErrNotJoined) {
		t.Fatalf("want ErrNotJoined, got %v", err)
	}
	if Joined() {
		t.Fatal("Joined() should be false")
	}
}

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.CacheLimitMB != 51200 || c.ClientCache || !c.IndexTLS || c.Containers {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.SmallFileKB != 1024 || c.RetentionDays != 30 || c.IndexDB != 1 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestRoundTripAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	t.Setenv("LOREFS_CONFIG_DIR", dir)
	c := Defaults()
	c.Machine = "alice"
	c.Folder = `/home/alice/My "Lore"`
	c.IndexHost = "index.example.net"
	c.IndexPort = 12345
	c.ClientCache = true
	c.Containers = true
	c.CacheLimitMB = 1000
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != c {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, c)
	}
	st, err := os.Stat(File())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config.toml mode = %v, want 0600", st.Mode().Perm())
	}
	dst, _ := os.Stat(dir)
	if dst.Mode().Perm() != 0o700 {
		t.Fatalf("config dir mode = %v, want 0700", dst.Mode().Perm())
	}
}

func TestMissingKeysKeepDefaults(t *testing.T) {
	t.Setenv("LOREFS_CONFIG_DIR", t.TempDir())
	if err := os.WriteFile(File(), []byte("machine = \"a\"\nfuture_key = 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Machine != "a" || c.CacheLimitMB != 51200 {
		t.Fatalf("unexpected: %+v", c)
	}
}

func TestBadValueIsReported(t *testing.T) {
	t.Setenv("LOREFS_CONFIG_DIR", t.TempDir())
	if err := os.WriteFile(File(), []byte("index_port = many\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a non-numeric port")
	}
}

func TestPasswordRoundTrip(t *testing.T) {
	t.Setenv("LOREFS_CONFIG_DIR", t.TempDir())
	if err := SavePassword("s3cret-value"); err != nil {
		t.Fatal(err)
	}
	pw, err := Password()
	if err != nil || pw != "s3cret-value" {
		t.Fatalf("got %q, %v", pw, err)
	}
	st, _ := os.Stat(SecretFile())
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("secret.env mode = %v", st.Mode().Perm())
	}
}

func TestPins(t *testing.T) {
	t.Setenv("LOREFS_CONFIG_DIR", t.TempDir())
	if p, err := Pins(); err != nil || len(p) != 0 {
		t.Fatalf("empty pins: %v %v", p, err)
	}
	if err := SavePins([]string{"notes", "assets/brand"}); err != nil {
		t.Fatal(err)
	}
	p, err := Pins()
	if err != nil || len(p) != 2 || p[0] != "assets/brand" {
		t.Fatalf("pins = %v, %v", p, err)
	}
}

func TestIndexDescription(t *testing.T) {
	cases := []struct {
		raw      string
		scheme   string
		host     string
		port, db int
		tls      bool
		local    bool
		wantAddr string
	}{
		{"rediss://index.example.net:6380/2", "rediss", "index.example.net", 6380, 2, true, false, "rediss://index.example.net:6380/2"},
		{"rediss://203.0.113.7/1", "rediss", "203.0.113.7", 6379, 1, true, false, "rediss://203.0.113.7:6379/1"},
		{"redis://127.0.0.1:16380/1", "redis", "127.0.0.1", 16380, 1, false, false, "redis://127.0.0.1:16380/1"},
		{"sqlite3:///home/alice/.local/share/lorefs/local/trial/meta.db", "sqlite3", "", 0, 0, false, true,
			"sqlite3:///home/alice/.local/share/lorefs/local/trial/meta.db"},
	}
	for _, tc := range cases {
		var c Config
		if err := c.setIndex(tc.raw); err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if c.IndexScheme != tc.scheme || c.IndexHost != tc.host || c.IndexPort != tc.port || c.IndexDB != tc.db || c.IndexTLS != tc.tls || c.IsLocal() != tc.local {
			t.Errorf("%s: got %+v", tc.raw, c)
		}
		if got := c.IndexURL(); got != tc.wantAddr {
			t.Errorf("%s: IndexURL() = %q, want %q", tc.raw, got, tc.wantAddr)
		}
	}
	for _, bad := range []string{"http://x:1/1", "rediss://:6379/1", "rediss://h:99999/1", "rediss://h:6379/x", "sqlite3://", "not an address"} {
		var c Config
		if err := c.setIndex(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// A settings file written by the program that came before: no "index"
// key, no owner_gid; it must still load, with the scheme from index_tls.
func TestReadsTheEarlierSettingsFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOREFS_CONFIG_DIR", dir)
	old := `# settings
drive_name = "lorefs"
machine = "alice"
folder = "/home/alice/Lore"
index_host = "index.example.net"
index_port = 6379
index_db = 1
index_tls = true
owner_uid = 1000
cache_dir = "/home/alice/.cache/lorefs"
cache_limit_mb = 51200
`
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(old), 0o600)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.IndexScheme != SchemeEncrypted || c.IndexURL() != "rediss://index.example.net:6379/1" || c.OwnerUID != 1000 {
		t.Fatalf("%+v", c)
	}
	// index_tls = false gives the unencrypted scheme.
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(strings.Replace(old, "index_tls = true", "index_tls = false", 1)), 0o600)
	if c, _ = Load(); c.IndexScheme != SchemePlain || c.IndexTLS {
		t.Fatalf("%+v", c)
	}
}

// Saving keeps the old keys so that the earlier program can still read
// the file (the way back).
func TestSavedFileStaysReadableByTheEarlierProgram(t *testing.T) {
	t.Setenv("LOREFS_CONFIG_DIR", t.TempDir())
	c := Defaults()
	c.IndexHost, c.IndexPort = "index.example.net", 6379
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(File())
	for _, want := range []string{`index = "rediss://index.example.net:6379/1"`, `index_host = "index.example.net"`, "index_port = 6379", "index_db = 1", "index_tls = true", "owner_uid = "} {
		if !strings.Contains(string(data), want) {
			t.Errorf("settings file lacks %q:\n%s", want, data)
		}
	}
}
