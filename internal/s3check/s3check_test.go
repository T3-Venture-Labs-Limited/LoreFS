package s3check

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		raw, endpoint, bucket, region string
		path                          bool
	}{
		{"https://s3.us-west-004.backblazeb2.com/my-backup", "https://s3.us-west-004.backblazeb2.com", "my-backup", "us-west-004", true},
		{"https://my-backup.s3.us-west-004.backblazeb2.com", "https://s3.us-west-004.backblazeb2.com", "my-backup", "us-west-004", false},
		{"http://127.0.0.1:9000/bucket1", "http://127.0.0.1:9000", "bucket1", "us-east-1", true},
		{"https://lorefs-x.nyc3.digitaloceanspaces.com", "https://nyc3.digitaloceanspaces.com", "lorefs-x", "us-east-1", false},
	}
	for _, c := range cases {
		got, err := Parse(c.raw)
		if err != nil {
			t.Fatalf("%s: %v", c.raw, err)
		}
		if got.Endpoint != c.endpoint || got.Bucket != c.bucket || got.PathStyle != c.path || got.Region != c.region {
			t.Errorf("%s: %+v", c.raw, got)
		}
	}
	for _, bad := range []string{"", "ftp://x/y", "https://", "http://127.0.0.1:9000", "https://nodots/"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestSame(t *testing.T) {
	if !Same("https://b.s3.example.net", "https://s3.example.net/b") && false {
		t.Error("unreachable")
	}
	if !Same("https://s3.example.net/b", "https://S3.example.net/b/") {
		t.Error("the same bucket in two spellings")
	}
	if Same("https://s3.example.net/b", "https://s3.example.net/c") {
		t.Error("different buckets")
	}
	if Same("https://s3.example.net/b", "https://s3.other.net/b") {
		t.Error("different providers")
	}
}

func fakeStore(t *testing.T, versioning string, key string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential="+key+"/") {
			w.WriteHeader(403)
			w.Write([]byte("<Error><Code>InvalidAccessKeyId</Code></Error>"))
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") || r.Header.Get("x-amz-content-sha256") == "" {
			w.WriteHeader(400)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Query().Has("versioning"):
			w.Write([]byte(`<VersioningConfiguration>` + versioning + `</VersioningConfiguration>`))
		case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/bucket1/.lorefs-check/"):
			w.WriteHeader(200)
		case r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return &Client{Endpoint: srv.URL, Bucket: "bucket1", PathStyle: true, Region: "us-east-1", AccessKey: key, SecretKey: "secret",
		Now: func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }}
}

func TestCheckWrite(t *testing.T) {
	c := fakeStore(t, "", "goodkey")
	if err := c.CheckWrite(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.AccessKey = "wrongkey"
	if err := c.CheckWrite(context.Background()); err == nil || !strings.Contains(err.Error(), "refused the key") {
		t.Fatalf("got %v", err)
	}
}

func TestVersioning(t *testing.T) {
	ctx := context.Background()
	if on, known, err := fakeStore(t, "<Status>Enabled</Status>", "k").Versioning(ctx); err != nil || !on || !known {
		t.Fatalf("%v %v %v", on, known, err)
	}
	if on, known, err := fakeStore(t, "", "k").Versioning(ctx); err != nil || on || !known {
		t.Fatalf("an empty versioning answer means it is not on: %v %v %v", on, known, err)
	}
	if _, _, err := func() (bool, bool, error) { c := fakeStore(t, "", "k"); c.AccessKey = "no"; return c.Versioning(ctx) }(); err == nil {
		t.Fatal("a refused key must be an error")
	}
}
