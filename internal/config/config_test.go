package config

import (
	"errors"
	"os"
	"path/filepath"
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
	c.IndexHost = "example.proxy.rlwy.net"
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
