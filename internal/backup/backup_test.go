package backup

import (
	"strings"
	"testing"
)

func TestParseSummary(t *testing.T) {
	out := `2026/11/20 18:40:37 <INFO>: Syncing from "a" to "b"
2026/11/20 18:40:37 <INFO>: Found: 7, excluded: 0 (0 B), skipped: 6 (2.86 MiB), copied: 1 (5 B), extra: 3 (0 B), deleted: 2, failed: 0 [Sync@sync.go:2238]`
	s, ok := ParseSummary(out)
	if !ok || s.Found != 7 || s.Skipped != 6 || s.Copied != 1 || s.Extra != 3 || s.Deleted != 2 || s.Failed != 0 {
		t.Fatalf("%+v %v", s, ok)
	}
	if s.SkippedBytes != 2998927 || s.CopiedBytes != 5 {
		t.Fatalf("%+v", s)
	}
	dry := `<INFO>: Found: 7, excluded: 0 (0 B), skipped: 7 (2.86 MiB), copied: 0 (0 B), extra: 3 (0 B) [Sync@sync.go:2238]`
	if s, ok = ParseSummary(dry); !ok || s.Found != 7 || s.Deleted != 0 {
		t.Fatalf("%+v %v", s, ok)
	}
	if _, ok := ParseSummary("nothing"); ok {
		t.Fatal("no summary")
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"0 B": 0, "5 B": 5, "1 KiB": 1024, "2.5 MiB": 2621440, "3 GiB": 3 << 30, "nonsense": 0} {
		if got := ParseSize(in); got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}

func TestSyncURI(t *testing.T) {
	cases := []struct {
		bucket, prefix, ak, sk, want string
		plain                        bool
	}{
		{"https://lorefs-x.s3.us-west-004.backblazeb2.com", "team", "AK", "S/K+1", "s3://AK:S%2FK+1@lorefs-x.s3.us-west-004.backblazeb2.com/team/", false},
		{"http://127.0.0.1:19000/bucket1", "drive1", "", "", "s3://127.0.0.1:19000/bucket1/drive1/", true},
		{"https://s3.eu-central-1.amazonaws.com/my-bucket", "d", "A", "B", "s3://A:B@s3.eu-central-1.amazonaws.com/my-bucket/d/", false},
	}
	for _, c := range cases {
		got, plain, err := SyncURI(c.bucket, c.prefix, c.ak, c.sk)
		if err != nil || got != c.want || plain != c.plain {
			t.Errorf("%s: %q %v %v, want %q", c.bucket, got, plain, err, c.want)
		}
	}
	if _, _, err := SyncURI("not a url", "x", "", ""); err == nil {
		t.Error("bad address")
	}
}

func TestExplain(t *testing.T) {
	cap := "2026/11/20 <ERROR>: copy x: Cap exceeded: storage cap reached [sync.go:1]"
	if got := explain(cap, "f"); !strings.Contains(got, "spending limit") {
		t.Errorf("%q", got)
	}
	if got := explain("<ERROR>: api error SignatureDoesNotMatch", "f"); !strings.Contains(got, "refused the key") {
		t.Errorf("%q", got)
	}
	if got := explain("<ERROR>: something odd [a.go:1]", "the copy failed"); got != "the copy failed: something odd" {
		t.Errorf("%q", got)
	}
	if got := explain("", "fallback"); got != "fallback" {
		t.Errorf("%q", got)
	}
}

func TestKeepNote(t *testing.T) {
	if n := (SetupResult{KeepsKnown: true}).KeepNote(); !strings.Contains(n, "removes objects at once") || !strings.Contains(n, "versioning") {
		t.Error(n)
	}
	if n := (SetupResult{KeepsKnown: true, KeepsRemoved: true}).KeepNote(); !strings.Contains(n, "keeps removed") {
		t.Error(n)
	}
	if n := (SetupResult{}).KeepNote(); !strings.Contains(n, "could not be checked") {
		t.Error(n)
	}
}
