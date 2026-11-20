// Package s3check talks to an S3-compatible bucket just enough to check a
// backup destination: that it can be reached and written with a key, and
// whether it keeps what is removed from it. It signs requests itself
// (AWS signature version 4), so no cloud library is needed.
package s3check

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Client is one bucket.
type Client struct {
	Endpoint  string // scheme://host[:port], without the bucket
	Bucket    string
	PathStyle bool // https://host/bucket instead of https://bucket.host
	Region    string
	AccessKey string
	SecretKey string
	HTTP      *http.Client
	Now       func() time.Time
}

var regionInHost = regexp.MustCompile(`(?:^|[.-])((?:us|eu|ap|ca|sa|af|me|il)-[a-z]+-\d+|[a-z]{2}-[a-z]+-\d{3})(?:\.|$)`)

// Parse reads a bucket address in either form: https://host/bucket (the
// bucket is in the path) or https://bucket.host (the bucket is the first
// part of the name). A host that is an IP address or "localhost" always
// has the bucket in the path.
func Parse(raw string) (Client, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return Client{}, fmt.Errorf("%q is not a bucket address such as https://store.example.net/my-bucket", raw)
	}
	c := Client{Region: "us-east-1"}
	host := u.Host
	hostname := u.Hostname()
	if m := regionInHost.FindStringSubmatch(hostname); m != nil {
		c.Region = m[1]
	}
	path := strings.Trim(u.Path, "/")
	switch {
	case path != "":
		c.Endpoint, c.Bucket, c.PathStyle = u.Scheme+"://"+host, strings.SplitN(path, "/", 2)[0], true
	case net.ParseIP(hostname) != nil || hostname == "localhost":
		return Client{}, fmt.Errorf("%q has no bucket name: add it after the address, e.g. %s/my-bucket", raw, raw)
	default:
		first, rest, ok := strings.Cut(host, ".")
		if !ok || rest == "" {
			return Client{}, fmt.Errorf("%q has no bucket name", raw)
		}
		c.Endpoint, c.Bucket = u.Scheme+"://"+rest, first
	}
	return c, nil
}

// Same reports whether two bucket addresses name the same bucket.
func Same(a, b string) bool {
	x, err1 := Parse(a)
	y, err2 := Parse(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return strings.EqualFold(x.host(), y.host()) && x.Bucket == y.Bucket
}

func (c Client) host() string {
	u, _ := url.Parse(c.Endpoint)
	if u == nil {
		return ""
	}
	return u.Host
}

// ErrDenied is returned when the storage refuses the key.
var ErrDenied = errors.New("the storage refused the key")

func (c Client) url(key, query string) (*url.URL, string) {
	e, _ := url.Parse(c.Endpoint)
	host := e.Host
	path := "/" + key
	if c.PathStyle {
		path = "/" + c.Bucket + "/" + key
	} else {
		host = c.Bucket + "." + e.Host
	}
	u := &url.URL{Scheme: e.Scheme, Host: host, Path: path, RawQuery: query}
	return u, host
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func mac(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func (c Client) do(ctx context.Context, method, key, query string, body []byte) (int, []byte, error) {
	return c.doLimit(ctx, method, key, query, body, 1<<20)
}

// doBig is do for downloads: it reads up to 256 MiB.
func (c Client) doBig(ctx context.Context, method, key string) (int, []byte, error) {
	return c.doLimit(ctx, method, key, "", nil, 256<<20)
}

func (c Client) doLimit(ctx context.Context, method, key, query string, body []byte, limit int64) (int, []byte, error) {
	u, host := c.url(key, query)
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	now = now.UTC()
	amz := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	payload := sha(body)
	canonURI := (&url.URL{Path: u.Path}).EscapedPath()
	canonQuery := ""
	if query != "" {
		q, _ := url.ParseQuery(query)
		canonQuery = q.Encode()
	}
	headers := "host:" + host + "\nx-amz-content-sha256:" + payload + "\nx-amz-date:" + amz + "\n"
	signed := "host;x-amz-content-sha256;x-amz-date"
	canon := strings.Join([]string{method, canonURI, canonQuery, headers, signed, payload}, "\n")
	scope := day + "/" + c.Region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amz + "\n" + scope + "\n" + sha([]byte(canon))
	k := mac(mac(mac(mac([]byte("AWS4"+c.SecretKey), day), c.Region), "s3"), "aws4_request")
	sig := hex.EncodeToString(mac(k, toSign))
	req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("x-amz-date", amz)
	req.Header.Set("x-amz-content-sha256", payload)
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", c.AccessKey, scope, signed, sig))
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("no answer from %s: %w", host, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	return resp.StatusCode, data, nil
}

func denied(status int, body []byte) bool {
	b := string(body)
	return status == 401 || status == 403 || strings.Contains(b, "SignatureDoesNotMatch") ||
		strings.Contains(b, "InvalidAccessKeyId") || strings.Contains(b, "AccessDenied")
}

// CheckWrite writes a small object under a prefix of its own and removes
// it again, to show that the key can write to the bucket.
func (c Client) CheckWrite(ctx context.Context) error {
	key := ".lorefs-check/" + fmt.Sprint(time.Now().UnixNano())
	st, body, err := c.do(ctx, "PUT", key, "", []byte("lorefs backup check\n"))
	if err != nil {
		return err
	}
	if denied(st, body) {
		return fmt.Errorf("%w. Check the key, and that it may write to the bucket %q", ErrDenied, c.Bucket)
	}
	if st == 404 {
		return fmt.Errorf("the bucket %q does not exist there", c.Bucket)
	}
	if st/100 != 2 {
		return fmt.Errorf("the storage answered %d when writing a test object", st)
	}
	c.do(ctx, "DELETE", key, "", nil)
	return nil
}

// Versioning reports whether the bucket keeps removed and replaced
// objects as earlier versions (S3 bucket versioning, which Backblaze B2
// has always on). known is false when the storage does not say.
func (c Client) Versioning(ctx context.Context) (enabled, known bool, err error) {
	st, body, err := c.do(ctx, "GET", "", "versioning=", nil)
	if err != nil {
		return false, false, err
	}
	if denied(st, body) {
		return false, false, fmt.Errorf("%w", ErrDenied)
	}
	if st/100 != 2 {
		return false, false, nil
	}
	s := string(body)
	switch {
	case strings.Contains(s, "<Status>Enabled</Status>"):
		return true, true, nil
	case strings.Contains(s, "<Status>Suspended</Status>"), strings.Contains(s, "VersioningConfiguration"):
		return false, true, nil
	}
	return false, false, nil
}

// List returns the keys that start with prefix (at most 1000).
func (c Client) List(ctx context.Context, prefix string) ([]string, error) {
	q := url.Values{"list-type": {"2"}, "prefix": {prefix}}.Encode()
	st, body, err := c.do(ctx, "GET", "", q, nil)
	if err != nil {
		return nil, err
	}
	if denied(st, body) {
		return nil, fmt.Errorf("%w", ErrDenied)
	}
	if st/100 != 2 {
		return nil, fmt.Errorf("the storage answered %d when listing %q", st, prefix)
	}
	var keys []string
	for _, m := range keyRe.FindAllSubmatch(body, -1) {
		keys = append(keys, xmlUnescape(string(m[1])))
	}
	return keys, nil
}

var keyRe = regexp.MustCompile(`<Key>([^<]*)</Key>`)

func xmlUnescape(s string) string {
	return strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'").Replace(s)
}

// Get downloads one object into memory (up to 256 MiB). It is meant for
// the copies of the index, which are a few megabytes.
func (c Client) Get(ctx context.Context, key string) ([]byte, error) {
	st, body, err := c.doBig(ctx, "GET", key)
	if err != nil {
		return nil, err
	}
	if denied(st, body) {
		return nil, fmt.Errorf("%w", ErrDenied)
	}
	if st == 404 {
		return nil, fmt.Errorf("%q is not in the bucket", key)
	}
	if st/100 != 2 {
		return nil, fmt.Errorf("the storage answered %d when reading %q", st, key)
	}
	return body, nil
}

// CreateBucket makes the bucket if it is not there (used by tests and by
// people who want setup to prepare the destination).
func (c Client) CreateBucket(ctx context.Context) error {
	st, body, err := c.do(ctx, "PUT", "", "", nil)
	if err != nil {
		return err
	}
	if denied(st, body) {
		return fmt.Errorf("%w", ErrDenied)
	}
	if st/100 != 2 && !strings.Contains(string(body), "BucketAlreadyOwnedByYou") {
		return fmt.Errorf("the storage answered %d when creating the bucket", st)
	}
	return nil
}
