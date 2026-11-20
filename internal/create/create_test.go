package create

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
)

// fakeRunner records what would be run, so that tests can check that no
// secret ever reaches a command line.
type fakeRunner struct {
	calls  [][]string
	envs   [][]string
	output map[string]string
	fail   map[string]bool
}

func (f *fakeRunner) Run(_ context.Context, env []string, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	f.envs = append(f.envs, env)
	if f.fail[args[0]] {
		return f.output[args[0]], errors.New("exit status 1")
	}
	return f.output[args[0]], nil
}

func TestCheckName(t *testing.T) {
	for _, ok := range []string{"lorefs", "team-drive", "abc", "A1-b2"} {
		if err := CheckName(ok); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ab", "my drive", "under_score", "dot.name", strings.Repeat("x", 64)} {
		if CheckName(bad) == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestIncompleteInput(t *testing.T) {
	_, err := Create(context.Background(), Options{Juicefs: &fakeRunner{}})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("got %v", err)
	}
	_, err = Create(context.Background(), Options{IndexURL: "rediss://h.example.net:6379/1", Juicefs: &fakeRunner{}})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("got %v", err)
	}
}

func TestPlainIndexIsRefusedWithoutTheOption(t *testing.T) {
	f := &fakeRunner{}
	_, err := Create(context.Background(), Options{
		Storage:  Storage{Bucket: "https://s.example.net/bucket", AccessKey: "k", SecretKey: "s"},
		IndexURL: "redis://127.0.0.1:6379/1", IndexPass: "p", Juicefs: f,
	})
	if !errors.Is(err, ErrPlainIndex) || !strings.Contains(err.Error(), "--allow-unencrypted-index") {
		t.Fatalf("got %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("nothing may run: %v", f.calls)
	}
}

func TestBadDriveNameIsRefusedFirst(t *testing.T) {
	f := &fakeRunner{}
	_, err := Create(context.Background(), Options{Name: "x", Juicefs: f})
	if err == nil || !strings.Contains(err.Error(), "not usable") || len(f.calls) != 0 {
		t.Fatalf("got %v, calls %v", err, f.calls)
	}
}

func TestExplainFormat(t *testing.T) {
	cases := []struct {
		out  string
		want error
	}{
		{"<FATAL>: Storage s3://x/b/d/ is not configured correctly: ... api error SignatureDoesNotMatch: The request signature", ErrStorageKey},
		{"<FATAL>: ... StatusCode: 403 ... InvalidAccessKeyId", ErrStorageKey},
		{"<FATAL>: Storage s3://127.0.0.1:19000/bucket1/drive1/ is not empty; please clean it up or pick another volume name [format@format.go:585]", ErrStorageInUse},
		{"<FATAL>: Failed to create bucket ... request send failed, Put \"http://127.0.0.1:19999\": dial tcp 127.0.0.1:19999: connect: connection refused", ErrStorageReach},
	}
	for _, c := range cases {
		if err := explainFormat(c.out, "https://s.example.net/b"); !errors.Is(err, c.want) {
			t.Errorf("%q: got %v, want %v", c.out, err, c.want)
		}
	}
	err := explainFormat("2026/11/20 17:02:57.647128 juicefs[1] <FATAL>: something unexpected [format@format.go:1]", "x")
	if err == nil || !strings.Contains(err.Error(), "something unexpected") || strings.Contains(err.Error(), "format.go") {
		t.Fatalf("got %v", err)
	}
}

func TestLastLineDropsLogDecoration(t *testing.T) {
	got := lastLine("a\n2026/11/20 17:02:57.647128 juicefs[1] <FATAL>: the message [format@format.go:430]\n")
	if got != "the message" {
		t.Fatalf("got %q", got)
	}
}

func TestMetaURLCarriesTheCertificatePath(t *testing.T) {
	var c config.Config
	if err := c.SetIndex("rediss://h.example.net:6380/2"); err != nil {
		t.Fatal(err)
	}
	if got := metaURL(c, ""); got != "rediss://h.example.net:6380/2" {
		t.Fatal(got)
	}
	if got := metaURL(c, "/tmp/ca.pem"); got != "rediss://h.example.net:6380/2?tls-ca-cert-file=/tmp/ca.pem" {
		t.Fatal(got)
	}
}

func TestCreateLocalNeedsNothingElseAndLeavesNothingOnFailure(t *testing.T) {
	t.Setenv("LOREFS_DATA_DIR", t.TempDir())
	f := &fakeRunner{output: map[string]string{"format": "boom"}, fail: map[string]bool{"format": true}}
	if _, err := CreateLocal(context.Background(), LocalOptions{Name: "trial", Juicefs: f}); err == nil {
		t.Fatal("expected a failure")
	}
	if _, err := os.Stat(filepath.Join(LocalRoot(), "trial")); !os.IsNotExist(err) {
		t.Fatal("a failed trial drive must leave nothing behind")
	}
	f = &fakeRunner{}
	cfg, err := CreateLocal(context.Background(), LocalOptions{Name: "trial", Juicefs: f})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsLocal() || cfg.IndexPath != filepath.Join(LocalRoot(), "trial", "meta.db") {
		t.Fatalf("%+v", cfg)
	}
	// The same name twice is refused and the first is untouched.
	if _, err := CreateLocal(context.Background(), LocalOptions{Name: "trial", Juicefs: f}); err == nil {
		t.Fatal("a second drive with the same name must be refused")
	}
	for _, call := range f.calls {
		for _, a := range call {
			if strings.Contains(a, "password") || strings.Contains(a, "SECRET") {
				t.Errorf("unexpected secret-looking argument %q", a)
			}
		}
	}
}

func TestRemoveLocalOnlyInsideTheTrialFolder(t *testing.T) {
	t.Setenv("LOREFS_DATA_DIR", t.TempDir())
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "meta.db"), []byte("x"), 0o600)
	c := config.Config{IndexScheme: config.SchemeSQLite, IndexPath: filepath.Join(outside, "meta.db")}
	if err := RemoveLocal(c); err == nil {
		t.Fatal("must refuse a folder outside the trial root")
	}
	if _, err := os.Stat(filepath.Join(outside, "meta.db")); err != nil {
		t.Fatal("the file outside was deleted")
	}
	if err := RemoveLocal(config.Config{IndexScheme: config.SchemeEncrypted}); !errors.Is(err, ErrNotTrial) {
		t.Fatalf("got %v", err)
	}
	dir := filepath.Join(LocalRoot(), "trial")
	os.MkdirAll(dir, 0o700)
	if err := RemoveLocal(config.Config{IndexScheme: config.SchemeSQLite, IndexPath: filepath.Join(dir, "meta.db")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("trial folder should be gone")
	}
}
