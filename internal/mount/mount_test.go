package mount

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
)

func base() config.Config {
	c := config.Defaults()
	c.IndexHost, c.IndexPort = "index.example.net", 6379
	c.OwnerUID, c.OwnerGID = 1000, 1001
	return c
}

func TestEveryMountActsAsTheDrivesOwner(t *testing.T) {
	args := Args(base())
	i := slices.Index(args, "--all-squash")
	if i < 0 || args[i+1] != "1000:1001" {
		t.Fatalf("want --all-squash 1000:1001, got %v", args)
	}
}

func TestMountArgumentsKeepTheSafetyOptions(t *testing.T) {
	args := strings.Join(Args(base()), " ")
	if strings.Contains(args, "--writeback") {
		t.Fatal("a save must only return once the data is stored")
	}
	for _, want := range []string{"--io-retries 8", "--hide-internal", "--backup-meta 1h"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %s in %s", want, args)
		}
	}
}

func TestIndexURLByScheme(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOREFS_CONFIG_DIR", dir)
	os.WriteFile(filepath.Join(dir, "ca.pem"), []byte("x"), 0o600) // an index with its own certificate authority
	c := base()
	u := IndexURL(c)
	if !strings.HasPrefix(u, "rediss://index.example.net:6379/1?") || !strings.Contains(u, "tls-ca-cert-file=") {
		t.Fatalf("encrypted: %s", u)
	}
	if strings.Contains(u, "password") {
		t.Fatalf("the password must never be in the address: %s", u)
	}
	c.IndexScheme, c.IndexTLS = config.SchemePlain, false
	if u = IndexURL(c); !strings.HasPrefix(u, "redis://") || strings.Contains(u, "tls-ca") {
		t.Fatalf("plain: %s", u)
	}
	c.IndexScheme, c.IndexPath = config.SchemeSQLite, "/home/alice/.local/share/lorefs/local/trial/meta.db"
	if u = IndexURL(c); u != "sqlite3:///home/alice/.local/share/lorefs/local/trial/meta.db" {
		t.Fatalf("trial: %s", u)
	}
}

func TestIndexURLWithoutOwnCertificateAuthority(t *testing.T) {
	t.Setenv("LOREFS_CONFIG_DIR", t.TempDir())
	if u := IndexURL(base()); strings.Contains(u, "tls-ca-cert-file") {
		t.Fatalf("a certificate from a public authority needs no file: %s", u)
	}
}
