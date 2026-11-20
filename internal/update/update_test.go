package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		installed, candidate string
		want                 bool
	}{
		{"v0.1.0", "0.2.0", true},
		{"0.2.0", "0.2.0", false},
		{"v0.2.0", "0.1.9", false},
		{"v0.9.0", "0.10.0", true},
		{"v1.0.0-rc.1", "1.0.0", true},
		{"v1.0.0", "1.0.0-rc.2", false},
		{"v1.0.0-rc.2", "1.0.0-rc.10", true},
		{"dev", "0.1.0", true},
		{"68b8873-dirty", "0.1.0", true},
		{"v0.2.0-3-gabc1234", "0.2.0", false}, // built after the tag: not offered that tag
		{"v0.2.0-3-gabc1234-dirty", "0.3.0", true},
		{"v0.2.0", "not-a-version", false},
	}
	for _, c := range cases {
		if got := Newer(c.installed, c.candidate); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.installed, c.candidate, got, c.want)
		}
	}
}

func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestParseSums(t *testing.T) {
	sums := strings.Join([]string{
		hash("a") + "  lorefs-0.2.0-linux-amd64.tar.gz",
		hash("b") + "  lorefs-0.2.0-linux-arm64.tar.gz",
		hash("c") + "  lorefs-0.10.0-linux-amd64.tar.gz",
		hash("d") + "  install.sh",
		"garbage line",
	}, "\n")
	r, err := ParseSums(sums, "amd64")
	if err != nil || r.Version != "0.10.0" || r.SHA256 != hash("c") {
		t.Fatalf("%+v %v", r, err)
	}
	if r, err = ParseSums(sums, "arm64"); err != nil || r.Version != "0.2.0" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err = ParseSums("", "amd64"); err == nil {
		t.Fatal("an empty list has no archive")
	}
}

// release builds a .tar.gz whose installer writes a marker file.
func release(t *testing.T, version, marker string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	top := "lorefs-" + version + "-linux-amd64"
	tw.WriteHeader(&tar.Header{Name: top + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	script := "#!/bin/sh\necho installed " + version + " >> '" + marker + "'\n"
	tw.WriteHeader(&tar.Header{Name: top + "/install.sh", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(script))})
	tw.Write([]byte(script))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func serve(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdateInstallsANewerRelease(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	arch := release(t, "0.2.0", marker)
	srv := serve(t, map[string][]byte{
		"SHA256SUMS":                      []byte(hash(string(arch)) + "  lorefs-0.2.0-linux-amd64.tar.gz\n"),
		"lorefs-0.2.0-linux-amd64.tar.gz": arch,
	})
	var out bytes.Buffer
	res, err := Run(context.Background(), Options{Installed: "v0.1.0", Base: srv.URL, Out: &out, Arch: "amd64"})
	if err != nil || !res.Done || res.Latest != "0.2.0" {
		t.Fatalf("%+v %v\n%s", res, err, out.String())
	}
	if data, _ := os.ReadFile(marker); !strings.Contains(string(data), "installed 0.2.0") {
		t.Fatalf("the installer did not run: %q", data)
	}
}

func TestUpdateCheckOnlyChangesNothing(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	arch := release(t, "0.2.0", marker)
	srv := serve(t, map[string][]byte{"SHA256SUMS": []byte(hash(string(arch)) + "  lorefs-0.2.0-linux-amd64.tar.gz\n"), "lorefs-0.2.0-linux-amd64.tar.gz": arch})
	res, err := Run(context.Background(), Options{Installed: "v0.1.0", CheckOnly: true, Base: srv.URL, Arch: "amd64"})
	if err != nil || !res.Newer || res.Done {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("--check must not install")
	}
}

func TestAlreadyNewest(t *testing.T) {
	srv := serve(t, map[string][]byte{"SHA256SUMS": []byte(hash("x") + "  lorefs-0.2.0-linux-amd64.tar.gz\n")})
	res, err := Run(context.Background(), Options{Installed: "v0.2.0", Base: srv.URL, Arch: "amd64"})
	if err != nil || res.Newer || res.Done {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestUnreachableServerChangesNothing(t *testing.T) {
	srv := serve(t, nil)
	srv.Close()
	_, err := Run(context.Background(), Options{Installed: "v0.1.0", Base: srv.URL, Arch: "amd64"})
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("got %v", err)
	}
}

func TestCorruptedArchiveIsNotInstalled(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	arch := release(t, "0.2.0", marker)
	corrupt := append([]byte(nil), arch...)
	corrupt[len(corrupt)/2] ^= 0xff
	srv := serve(t, map[string][]byte{"SHA256SUMS": []byte(hash(string(arch)) + "  lorefs-0.2.0-linux-amd64.tar.gz\n"), "lorefs-0.2.0-linux-amd64.tar.gz": corrupt})
	res, err := Run(context.Background(), Options{Installed: "v0.1.0", Base: srv.URL, Arch: "amd64"})
	if err == nil || !strings.Contains(err.Error(), "does not match") || res.Done {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a corrupted archive must not run its installer")
	}
}

func TestUnsafeArchiveIsRefused(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	if _, err := extract(buf.Bytes(), t.TempDir()); err == nil {
		t.Fatal("a path outside the folder must be refused")
	}
}
